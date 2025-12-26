package service

import (
	"context"
	"fmt"
	"log"
	"sizehunt/internal/bybit/entity"
	bybit_repository "sizehunt/internal/bybit/repository"
	"sizehunt/internal/config"
	subscriptionservice "sizehunt/internal/subscription/service"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Signal представляет сигнал для мониторинга Bybit ордербука
type Signal struct {
	ID              int64
	UserID          int64
	Symbol          string // BTCUSDT
	Category        string // spot, linear, inverse
	TargetPrice     float64
	MinQuantity     float64
	TriggerOnCancel bool
	TriggerOnEat    bool
	EatPercentage   float64 // 0.5 = 50%
	OriginalQty     float64 // объём заявки при создании
	LastQty         float64 // текущий объём
	AutoClose       bool
	WatchCategory   string    // категория для мониторинга
	CloseCategory   string    // категория для закрытия
	OriginalSide    string    // "buy" или "sell"
	TriggerTime     time.Time // Время срабатывания триггера
	CreatedAt       time.Time // Время создания сигнала
}

// OrderBookMap представляет локальный ордербук для одного символа
type OrderBookMap struct {
	Bids           map[float64]float64 // цена -> объём
	Asks           map[float64]float64 // цена -> объём
	LastSeqID      int64
	LastUpdateTime time.Time
}

// MarketDepthWatcher отвечает за мониторинг ордербука Bybit
type MarketDepthWatcher struct {
	client              *BybitWebSocketClient
	signalsBySymbol     map[string][]*Signal     // symbol -> []signals
	orderBooks          map[string]*OrderBookMap // symbol -> OrderBookMap
	mu                  sync.RWMutex
	onTrigger           func(signal *Signal, order *entity.Order)
	subscriptionService *subscriptionservice.Service
	keysRepo            *bybit_repository.PostgresKeysRepo
	config              *config.Config
	activeSymbols       map[string]string // symbol -> category
	ctx                 context.Context
	started             bool
	httpClient          *BybitHTTPClient
	creationTime        time.Time
	lastActivityTime    time.Time
	signalRepository    bybit_repository.SignalRepository
	WebSocketManager    *WebSocketManager
	UserID              int64
	positionWatcher     *PositionWatcher
	pendingAutoCloseOps int32 // счётчик активных auto-close операций
}

// NewMarketDepthWatcher создает новый MarketDepthWatcher для Bybit
func NewMarketDepthWatcher(
	ctx context.Context,
	subService *subscriptionservice.Service,
	keysRepo *bybit_repository.PostgresKeysRepo,
	cfg *config.Config,
	httpClient *BybitHTTPClient,
	signalRepo bybit_repository.SignalRepository,
	wsManager *WebSocketManager,
	userID int64,
) *MarketDepthWatcher {
	watcher := &MarketDepthWatcher{
		client:              nil,
		signalsBySymbol:     make(map[string][]*Signal),
		orderBooks:          make(map[string]*OrderBookMap),
		activeSymbols:       make(map[string]string),
		mu:                  sync.RWMutex{},
		onTrigger:           nil,
		subscriptionService: subService,
		keysRepo:            keysRepo,
		config:              cfg,
		ctx:                 ctx,
		started:             false,
		httpClient:          httpClient,
		creationTime:        time.Now(),
		lastActivityTime:    time.Now(),
		signalRepository:    signalRepo,
		WebSocketManager:    wsManager,
		UserID:              userID,
	}

	log.Printf("BybitMarketDepthWatcher: Created new watcher for user %d (creation time: %v)", userID, watcher.creationTime)
	return watcher
}

// AddSignal добавляет сигнал для мониторинга
func (w *MarketDepthWatcher) AddSignal(signal *Signal) {
	startTime := time.Now()
	defer func() {
		log.Printf("BybitMarketDepthWatcher: AddSignal completed (total time: %v)", time.Since(startTime))
	}()

	w.mu.Lock()
	defer w.mu.Unlock()

	signal.CreatedAt = time.Now()
	w.lastActivityTime = time.Now()

	log.Printf("BybitMarketDepthWatcher: Adding signal %d for user %d, symbol %s, price %.8f, originalQty %.4f, side %s",
		signal.ID, signal.UserID, signal.Symbol, signal.TargetPrice, signal.OriginalQty, signal.OriginalSide)

	// Добавляем сигнал в список для его символа
	currentSignals := w.signalsBySymbol[signal.Symbol]
	w.signalsBySymbol[signal.Symbol] = append(currentSignals, signal)
	log.Printf("BybitMarketDepthWatcher: Signal %d added. Total signals for %s: %d",
		signal.ID, signal.Symbol, len(w.signalsBySymbol[signal.Symbol]))

	_, wasActive := w.activeSymbols[signal.Symbol]
	w.activeSymbols[signal.Symbol] = signal.WatchCategory

	// Инициализируем локальный стакан для символа
	ob, ok := w.orderBooks[signal.Symbol]
	if !ok {
		ob = &OrderBookMap{
			Bids:           make(map[float64]float64),
			Asks:           make(map[float64]float64),
			LastUpdateTime: time.Now(),
		}
		w.orderBooks[signal.Symbol] = ob
		log.Printf("BybitMarketDepthWatcher: Created new orderbook for symbol %s", signal.Symbol)
	}

	// Инициализируем уровень в ордербуке
	if signal.OriginalQty > 0 {
		switch signal.OriginalSide {
		case "buy":
			ob.Bids[signal.TargetPrice] = signal.OriginalQty
		case "sell":
			ob.Asks[signal.TargetPrice] = signal.OriginalQty
		}
		ob.LastUpdateTime = time.Now()
	}

	// Запускаем соединение если это первый активный символ
	if !wasActive && !w.started {
		log.Printf("BybitMarketDepthWatcher: First signal for symbol %s, connection will be started asynchronously", signal.Symbol)
		go func() {
			if err := w.StartConnection(); err != nil {
				log.Printf("BybitMarketDepthWatcher: ERROR: Failed to start connection: %v", err)
				w.mu.Lock()
				w.started = false
				w.mu.Unlock()
			}
		}()
	}
}

// RemoveSignal удаляет сигнал из мониторинга
func (w *MarketDepthWatcher) RemoveSignal(id int64) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.lastActivityTime = time.Now()
	w.removeSignalByIDLocked(id)

	// Проверяем нужно ли остановить PositionWatcher
	w.checkAndStopPositionWatcher()
}

func (w *MarketDepthWatcher) removeSignalByIDLocked(id int64) {
	for symbol, signals := range w.signalsBySymbol {
		for i, signal := range signals {
			if signal.ID == id {
				// Удаляем сигнал из слайса
				w.signalsBySymbol[symbol] = append(signals[:i], signals[i+1:]...)
				log.Printf("BybitMarketDepthWatcher: Removed signal %d for symbol %s", id, symbol)

				// Если больше нет сигналов для этого символа - очищаем
				if len(w.signalsBySymbol[symbol]) == 0 {
					delete(w.signalsBySymbol, symbol)
					delete(w.activeSymbols, symbol)
					delete(w.orderBooks, symbol)

					// Если это был последний символ - останавливаем WebSocket
					if len(w.activeSymbols) == 0 && w.client != nil {
						log.Printf("BybitMarketDepthWatcher: No active symbols left, closing WebSocket connection")
						w.client.Close()
						w.client = nil
						w.started = false
					}
				}

				// Обновляем сигнал в БД асинхронно
				go func() {
					if w.signalRepository != nil {
						ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
						defer cancel()

						signalDB := &bybit_repository.SignalDB{
							ID:       id,
							IsActive: false,
							LastQty:  signal.LastQty,
						}

						if err := w.signalRepository.Update(ctx, signalDB); err != nil {
							log.Printf("BybitMarketDepthWatcher: WARNING: Failed to update signal %d in database: %v", id, err)
						} else {
							log.Printf("BybitMarketDepthWatcher: Signal %d deactivated in database", id)
						}
					}
				}()

				return
			}
		}
	}
	log.Printf("BybitMarketDepthWatcher: WARNING: Signal %d not found for removal", id)
}

// checkAndStopPositionWatcher останавливает PositionWatcher если нет auto-close сигналов И нет активных операций
func (w *MarketDepthWatcher) checkAndStopPositionWatcher() {
	// Не останавливаем, если есть активные auto-close операции
	if w.pendingAutoCloseOps > 0 {
		log.Printf("BybitMarketDepthWatcher: %d pending auto-close operations, keeping PositionWatcher alive", w.pendingAutoCloseOps)
		return
	}

	hasAutoClose := false
	for _, signals := range w.signalsBySymbol {
		for _, signal := range signals {
			if signal.AutoClose {
				hasAutoClose = true
				break
			}
		}
		if hasAutoClose {
			break
		}
	}

	if !hasAutoClose && w.positionWatcher != nil && w.positionWatcher.IsRunning() {
		log.Printf("BybitMarketDepthWatcher: No auto-close signals left, stopping PositionWatcher for user %d", w.UserID)
		w.positionWatcher.Stop()
		w.positionWatcher = nil
	}
}

// StartConnection запускает WebSocket соединение
func (w *MarketDepthWatcher) StartConnection() error {
	startTime := time.Now()
	defer func() {
		log.Printf("BybitMarketDepthWatcher: StartConnection completed (total time: %v)", time.Since(startTime))
	}()

	w.mu.RLock()
	if w.started {
		w.mu.RUnlock()
		return nil
	}
	w.mu.RUnlock()

	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return nil
	}
	w.started = true
	w.mu.Unlock()

	log.Printf("BybitMarketDepthWatcher: Starting connection for user %d", w.UserID)

	// Получаем прокси адрес
	var proxyAddr string
	if w.WebSocketManager != nil {
		if addr, ok := w.WebSocketManager.GetProxyAddressForUser(w.UserID); ok {
			proxyAddr = addr
		}
	}

	// Группируем символы по категориям
	symbolsByCategory := make(map[string][]string)
	w.mu.RLock()
	for symbol, category := range w.activeSymbols {
		symbolsByCategory[category] = append(symbolsByCategory[category], symbol)
	}
	w.mu.RUnlock()

	if len(symbolsByCategory) == 0 {
		log.Printf("BybitMarketDepthWatcher: No symbols to watch, skipping connection")
		w.mu.Lock()
		w.started = false
		w.mu.Unlock()
		return nil
	}

	// Создаем клиент для первой категории (обычно linear)
	// TODO: поддержка нескольких категорий одновременно
	var category string
	var symbols []string
	for cat, syms := range symbolsByCategory {
		category = cat
		symbols = syms
		break
	}

	var client *BybitWebSocketClient
	if proxyAddr != "" {
		client = NewBybitWebSocketClientWithProxy(proxyAddr)
	} else {
		client = NewBybitWebSocketClient()
	}

	client.OnData = func(data *BybitDepthStreamData) {
		w.lastActivityTime = time.Now()
		w.processDepthUpdate(data)
	}

	// Подключаемся
	if err := client.ConnectPublic(w.ctx, symbols, category); err != nil {
		log.Printf("BybitMarketDepthWatcher: Failed to connect: %v", err)
		w.mu.Lock()
		w.started = false
		w.mu.Unlock()
		return err
	}

	w.mu.Lock()
	w.client = client
	w.mu.Unlock()

	log.Printf("BybitMarketDepthWatcher: Successfully connected for user %d (took %v)", w.UserID, time.Since(startTime))

	// Запускаем мониторинг активности
	go w.monitorActivity()

	// Запускаем поддержку HTTP соединения
	go w.keepConnectionsAlive()

	return nil
}

// processDepthUpdate обрабатывает обновление ордербука
func (w *MarketDepthWatcher) processDepthUpdate(data *BybitDepthStreamData) {
	// Извлекаем символ из топика: orderbook.500.BTCUSDT
	parts := strings.Split(data.Topic, ".")
	if len(parts) < 3 {
		return
	}
	symbol := parts[2]

	w.mu.Lock()
	defer w.mu.Unlock()

	w.lastActivityTime = time.Now()

	// Получаем или создаём локальный стакан
	ob, ok := w.orderBooks[symbol]
	if !ok {
		ob = &OrderBookMap{
			Bids:           make(map[float64]float64),
			Asks:           make(map[float64]float64),
			LastUpdateTime: time.Now(),
		}
		w.orderBooks[symbol] = ob
	}

	// Обновляем bids
	for _, bid := range data.Data.B {
		if len(bid) >= 2 {
			price, _ := strconv.ParseFloat(bid[0], 64)
			qty, _ := strconv.ParseFloat(bid[1], 64)
			if qty == 0 {
				delete(ob.Bids, price)
			} else {
				ob.Bids[price] = qty
			}
		}
	}

	// Обновляем asks
	for _, ask := range data.Data.A {
		if len(ask) >= 2 {
			price, _ := strconv.ParseFloat(ask[0], 64)
			qty, _ := strconv.ParseFloat(ask[1], 64)
			if qty == 0 {
				delete(ob.Asks, price)
			} else {
				ob.Asks[price] = qty
			}
		}
	}

	ob.LastSeqID = data.Data.Seq
	ob.LastUpdateTime = time.Now()

	// Обрабатываем сигналы для этого символа
	signalsForSymbol, ok := w.signalsBySymbol[symbol]
	if !ok {
		return
	}

	var signalsToRemove []int64

	for _, signal := range signalsForSymbol {
		if signal.UserID != w.UserID {
			continue
		}

		found, currentQty := w.findOrderAtPrice(ob, signal.TargetPrice)

		// Проверка на отмену
		if signal.TriggerOnCancel {
			if !found && signal.OriginalQty > 0 {
				triggerTime := time.Now()
				signal.TriggerTime = triggerTime
				log.Printf("BybitMarketDepthWatcher: Signal %d: Order at %.8f disappeared (was %.4f), triggering cancel",
					signal.ID, signal.TargetPrice, signal.LastQty)
				order := &entity.Order{
					Price:    signal.TargetPrice,
					Quantity: signal.LastQty,
					Side:     signal.OriginalSide,
					Symbol:   symbol,
				}
				if w.onTrigger != nil {
					w.onTrigger(signal, order)
				}
				signalsToRemove = append(signalsToRemove, signal.ID)

				if signal.AutoClose {
					sigCopy := *signal
					orderCopy := *order

					var cachedPos *Position
					if w.positionWatcher != nil && w.positionWatcher.IsRunning() {
						if pos, exists := w.positionWatcher.GetPosition(symbol); exists {
							posCopy := *pos
							cachedPos = &posCopy
						}
					}

					// Увеличиваем счётчик активных операций
					w.pendingAutoCloseOps++
					log.Printf("BybitMarketDepthWatcher: Started auto-close operation, pending ops: %d", w.pendingAutoCloseOps)

					go func() {
						defer func() {
							w.mu.Lock()
							w.pendingAutoCloseOps--
							log.Printf("BybitMarketDepthWatcher: Completed auto-close operation, pending ops: %d", w.pendingAutoCloseOps)
							// Проверяем нужно ли остановить PositionWatcher после завершения
							w.checkAndStopPositionWatcher()
							w.mu.Unlock()
						}()
						w.handleAutoClose(&sigCopy, &orderCopy, cachedPos)
					}()
				}
				continue
			}
		}

		// Проверка на разъедание
		if signal.TriggerOnEat && found {
			eaten := signal.OriginalQty - currentQty
			eatenPercentage := 0.0
			if signal.OriginalQty > 0 {
				eatenPercentage = eaten / signal.OriginalQty
			}
			if eatenPercentage >= signal.EatPercentage {
				triggerTime := time.Now()
				signal.TriggerTime = triggerTime
				log.Printf("BybitMarketDepthWatcher: Signal %d: Order at %.8f eaten by %.2f%% (%.4f -> %.4f), triggering eat",
					signal.ID, signal.TargetPrice, eatenPercentage*100, signal.OriginalQty, currentQty)
				order := &entity.Order{
					Price:    signal.TargetPrice,
					Quantity: currentQty,
					Side:     signal.OriginalSide,
					Symbol:   symbol,
				}
				if w.onTrigger != nil {
					w.onTrigger(signal, order)
				}
				signalsToRemove = append(signalsToRemove, signal.ID)

				if signal.AutoClose {
					sigCopy := *signal
					orderCopy := *order

					var cachedPos *Position
					if w.positionWatcher != nil && w.positionWatcher.IsRunning() {
						if pos, exists := w.positionWatcher.GetPosition(symbol); exists {
							posCopy := *pos
							cachedPos = &posCopy
						}
					}

					// Увеличиваем счётчик активных операций
					w.pendingAutoCloseOps++
					log.Printf("BybitMarketDepthWatcher: Started auto-close operation, pending ops: %d", w.pendingAutoCloseOps)

					go func() {
						defer func() {
							w.mu.Lock()
							w.pendingAutoCloseOps--
							log.Printf("BybitMarketDepthWatcher: Completed auto-close operation, pending ops: %d", w.pendingAutoCloseOps)
							w.checkAndStopPositionWatcher()
							w.mu.Unlock()
						}()
						w.handleAutoClose(&sigCopy, &orderCopy, cachedPos)
					}()
				}
				continue
			}
		}

		// Обновляем LastQty
		if found {
			signal.LastQty = currentQty
		} else if signal.LastQty > 0 {
			signal.LastQty = 0
		}
	}

	// Удаляем сработавшие сигналы
	for _, id := range signalsToRemove {
		w.removeSignalByIDLocked(id)
	}

	// Проверяем нужно ли остановить PositionWatcher после удаления сигналов
	if len(signalsToRemove) > 0 {
		w.checkAndStopPositionWatcher()
	}
}

// findOrderAtPrice ищет заявку в локальном ордербуке
func (w *MarketDepthWatcher) findOrderAtPrice(ob *OrderBookMap, targetPrice float64) (found bool, qty float64) {
	if qty, ok := ob.Bids[targetPrice]; ok {
		return true, qty
	}
	if qty, ok := ob.Asks[targetPrice]; ok {
		return true, qty
	}
	return false, 0
}

// handleAutoClose обрабатывает автоматическое закрытие позиции
func (w *MarketDepthWatcher) handleAutoClose(signal *Signal, order *entity.Order, cachedPos *Position) {
	startTime := time.Now()
	defer func() {
		log.Printf("BybitMarketDepthWatcher: handleAutoClose for signal %d completed (total time: %v)",
			signal.ID, time.Since(startTime))
	}()

	log.Printf("BybitMarketDepthWatcher: handleAutoClose called for signal %d, user %d, symbol %s",
		signal.ID, signal.UserID, signal.Symbol)

	// Проверка подписки
	subscribed, err := w.subscriptionService.IsUserSubscribed(context.Background(), signal.UserID)
	if err != nil {
		log.Printf("BybitMarketDepthWatcher: ERROR: IsUserSubscribed failed for user %d: %v", signal.UserID, err)
		return
	}
	if !subscribed {
		log.Printf("BybitMarketDepthWatcher: INFO: User %d is not subscribed, skipping auto-close", signal.UserID)
		return
	}

	var posToClose *Position
	closeCategory := signal.CloseCategory
	if closeCategory == "" {
		closeCategory = "linear"
	}

	// 1. Используем захваченную позицию
	if cachedPos != nil {
		posToClose = cachedPos
		log.Printf("BybitMarketDepthWatcher: Using SYNCHRONOUSLY captured position: size=%.6f, side=%s",
			posToClose.Size, posToClose.Side)
	} else {
		// 2. Пробуем получить из PositionWatcher
		if w.positionWatcher != nil && w.positionWatcher.IsRunning() {
			if pos, exists := w.positionWatcher.GetPosition(signal.Symbol); exists {
				posToClose = pos
				log.Printf("BybitMarketDepthWatcher: Position from WebSocket: %s size=%.6f, side=%s",
					signal.Symbol, posToClose.Size, posToClose.Side)
			}
		}
	}

	if posToClose == nil || posToClose.Size == 0 {
		// 3. Fallback на REST API
		log.Printf("BybitMarketDepthWatcher: No cached position, fetching via REST API for %s", signal.Symbol)
		if w.httpClient != nil {
			posResp, err := w.httpClient.GetPositionRisk(signal.Symbol, closeCategory)
			if err == nil && len(posResp.Result.List) > 0 {
				p := posResp.Result.List[0]
				size, _ := strconv.ParseFloat(p.Size, 64)
				avgPrice, _ := strconv.ParseFloat(p.AvgPrice, 64)
				uPnl, _ := strconv.ParseFloat(p.UnrealisedPnl, 64)

				posToClose = &Position{
					Symbol:        p.Symbol,
					Side:          p.Side,
					Size:          size,
					AvgPrice:      avgPrice,
					UnrealisedPnl: uPnl,
					Category:      closeCategory,
				}
				log.Printf("BybitMarketDepthWatcher: Position from REST API: %s size=%.6f, side=%s",
					signal.Symbol, posToClose.Size, posToClose.Side)
			}
		}
	}

	if posToClose == nil || posToClose.Size == 0 {
		log.Printf("BybitMarketDepthWatcher: No position to close for %s", signal.Symbol)
		return
	}

	// Закрытие позиции через REST API
	orderStartTime := time.Now()

	log.Printf("BybitMarketDepthWatcher: Closing position for %s (size: %.6f, side: %s)",
		signal.Symbol, posToClose.Size, posToClose.Side)

	manager := NewOrderManager(w.httpClient)
	closeErr := manager.ClosePositionWithSize(signal.Symbol, posToClose.Size, posToClose.Side, closeCategory)

	orderDuration := time.Since(orderStartTime)

	if closeErr != nil {
		log.Printf("BybitMarketDepthWatcher: ERROR: Position close failed for user %d: %v (order execution time: %v)",
			signal.UserID, closeErr, orderDuration)
		return
	}

	totalDuration := time.Since(startTime)
	log.Printf("BybitMarketDepthWatcher: ✅ SUCCESS: Position closed for user %d on %s (size: %.6f) | Order time: %v | Total time: %v",
		signal.UserID, signal.Symbol, posToClose.Size, orderDuration, totalDuration)

	// Плавная остановка прокси
	if w.WebSocketManager != nil {
		go func() {
			time.Sleep(1 * time.Second)
			w.WebSocketManager.GracefulStopProxyForUser(signal.UserID)
		}()
	}
}

// monitorActivity мониторит активность соединения
func (w *MarketDepthWatcher) monitorActivity() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			w.mu.RLock()
			lastActivity := w.lastActivityTime
			isStarted := w.started
			w.mu.RUnlock()

			inactiveDuration := time.Since(lastActivity)
			if !isStarted {
				continue
			}

			if inactiveDuration > 30*time.Second {
				log.Printf("BybitMarketDepthWatcher: WARNING: No activity for %v", inactiveDuration)
				w.mu.Lock()
				if w.started && w.client != nil {
					log.Printf("BybitMarketDepthWatcher: Attempting to reconnect due to inactivity")
					w.client.Close()
					w.client = nil
					w.started = false
					go func() {
						if err := w.StartConnection(); err != nil {
							log.Printf("BybitMarketDepthWatcher: ERROR: Reconnection failed: %v", err)
						}
					}()
				}
				w.mu.Unlock()
			}
		case <-w.ctx.Done():
			return
		}
	}
}

// keepConnectionsAlive поддерживает HTTP соединение активным
func (w *MarketDepthWatcher) keepConnectionsAlive() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	log.Printf("BybitMarketDepthWatcher: HTTP Keep-Alive pinger started for user %d", w.UserID)

	// Первый пинг сразу
	go func() {
		w.mu.RLock()
		client := w.httpClient
		w.mu.RUnlock()
		if client != nil {
			if err := client.Ping(); err != nil {
				log.Printf("BybitMarketDepthWatcher: Initial keep-alive ping failed: %v", err)
			}
		}
	}()

	for {
		select {
		case <-w.ctx.Done():
			return
		case <-ticker.C:
			w.mu.RLock()
			isStarted := w.started
			client := w.httpClient
			w.mu.RUnlock()

			if !isStarted {
				log.Printf("BybitMarketDepthWatcher: Pinger stopped as watcher is not started")
				return
			}

			if client != nil {
				if err := client.Ping(); err != nil {
					log.Printf("BybitMarketDepthWatcher: Keep-alive ping failed: %v", err)
				}
			}
		}
	}
}

// SetOnTrigger устанавливает callback для срабатывания сигнала
func (w *MarketDepthWatcher) SetOnTrigger(fn func(signal *Signal, order *entity.Order)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onTrigger = fn
}

// Close закрывает все соединения
func (w *MarketDepthWatcher) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.client != nil {
		w.client.Close()
		w.client = nil
	}
	w.started = false
}

// fallbackHTTPClose использует HTTP REST API для закрытия позиции
func (w *MarketDepthWatcher) fallbackHTTPClose(signal *Signal) error {
	if w.httpClient == nil {
		return fmt.Errorf("HTTP client not available")
	}
	manager := NewOrderManager(w.httpClient)
	return manager.CloseFullPosition(signal.Symbol, signal.CloseCategory)
}
