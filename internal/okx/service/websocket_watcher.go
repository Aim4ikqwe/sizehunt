package service

import (
	"context"
	"fmt"
	"log"
	"sizehunt/internal/config"
	"sizehunt/internal/okx/entity"
	okx_repository "sizehunt/internal/okx/repository"
	subscriptionservice "sizehunt/internal/subscription/service"
	"strconv"
	"sync"
	"time"
)

// Signal представляет сигнал для мониторинга OKX ордербука
type Signal struct {
	ID              int64
	UserID          int64
	InstID          string // OKX instrument ID (BTC-USDT-SWAP)
	InstType        string // SPOT, SWAP, FUTURES, OPTION
	TargetPrice     float64
	MinQuantity     float64
	TriggerOnCancel bool
	TriggerOnEat    bool
	EatPercentage   float64 // 0.5 = 50%
	OriginalQty     float64 // объём заявки при создании
	LastQty         float64 // текущий объём
	AutoClose       bool
	OriginalSide    string    // "buy" или "sell"
	TriggerTime     time.Time // Время срабатывания триггера
	OKXEventTime    time.Time // Время события на бирже
	CreatedAt       time.Time // Время создания сигнала
	CloseInstID     string    // ID инструмента для закрытия (опционально)
	CloseInstType   string    // Тип инструмента для закрытия (опционально)
}

// OrderBookMap представляет локальный ордербук для одного инструмента
type OrderBookMap struct {
	Bids           map[float64]float64 // цена -> объём
	Asks           map[float64]float64 // цена -> объём
	LastSeqID      int64
	LastUpdateTime time.Time
}

// MarketDepthWatcher отвечает за мониторинг ордербука OKX
type MarketDepthWatcher struct {
	client              *WebSocketClient
	signalsByInstID     map[string][]*Signal     // instID -> []signals
	orderBooks          map[string]*OrderBookMap // instID -> OrderBookMap
	mu                  sync.RWMutex
	onTrigger           func(signal *Signal, order *entity.Order)
	subscriptionService *subscriptionservice.Service
	keysRepo            *okx_repository.PostgresKeysRepo
	config              *config.Config
	activeInstruments   map[string]bool // инструменты, за которыми следим
	ctx                 context.Context
	started             bool
	httpClient          *OKXHTTPClient
	creationTime        time.Time
	lastActivityTime    time.Time
	signalRepository    okx_repository.SignalRepository
	WebSocketManager    *WebSocketManager
	UserID              int64
	positionWatcher     *PositionWatcher  // WebSocket для отслеживания позиций
	tradingWS           *TradingWebSocket // WebSocket для размещения ордеров
}

// NewMarketDepthWatcher создает новый MarketDepthWatcher для OKX
func NewMarketDepthWatcher(
	ctx context.Context,
	subService *subscriptionservice.Service,
	keysRepo *okx_repository.PostgresKeysRepo,
	cfg *config.Config,
	httpClient *OKXHTTPClient,
	signalRepo okx_repository.SignalRepository,
	wsManager *WebSocketManager,
	userID int64,
) *MarketDepthWatcher {
	watcher := &MarketDepthWatcher{
		client:              nil,
		signalsByInstID:     make(map[string][]*Signal),
		orderBooks:          make(map[string]*OrderBookMap),
		activeInstruments:   make(map[string]bool),
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

	log.Printf("OKXMarketDepthWatcher: Created new watcher for user %d (creation time: %v)", userID, watcher.creationTime)
	return watcher
}

// AddSignal добавляет сигнал для мониторинга
func (w *MarketDepthWatcher) AddSignal(signal *Signal) {
	startTime := time.Now()
	defer func() {
		log.Printf("OKXMarketDepthWatcher: AddSignal completed (total time: %v)", time.Since(startTime))
	}()

	w.mu.Lock()
	defer w.mu.Unlock()

	signal.CreatedAt = time.Now()
	w.lastActivityTime = time.Now()

	log.Printf("OKXMarketDepthWatcher: Adding signal %d for user %d, instID %s, price %.8f, originalQty %.4f, side %s",
		signal.ID, signal.UserID, signal.InstID, signal.TargetPrice, signal.OriginalQty, signal.OriginalSide)

	// Добавляем сигнал в список для его инструмента
	currentSignals := w.signalsByInstID[signal.InstID]
	w.signalsByInstID[signal.InstID] = append(currentSignals, signal)
	log.Printf("OKXMarketDepthWatcher: Signal %d added. Total signals for %s: %d",
		signal.ID, signal.InstID, len(w.signalsByInstID[signal.InstID]))

	_, wasActive := w.activeInstruments[signal.InstID]
	w.activeInstruments[signal.InstID] = true

	// Инициализируем локальный стакан для инструмента
	ob, ok := w.orderBooks[signal.InstID]
	if !ok {
		ob = &OrderBookMap{
			Bids:           make(map[float64]float64),
			Asks:           make(map[float64]float64),
			LastUpdateTime: time.Now(),
		}
		w.orderBooks[signal.InstID] = ob
		log.Printf("OKXMarketDepthWatcher: Created new orderbook for instrument %s", signal.InstID)
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

	// Запускаем соединение если это первый активный инструмент
	if !wasActive && !w.started {
		log.Printf("OKXMarketDepthWatcher: First signal for instrument %s, connection will be started asynchronously", signal.InstID)
		go func() {
			if err := w.StartConnection(); err != nil {
				log.Printf("OKXMarketDepthWatcher: ERROR: Failed to start connection: %v", err)
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
	for instID, signals := range w.signalsByInstID {
		for i, signal := range signals {
			if signal.ID == id {
				// Удаляем сигнал из слайса
				w.signalsByInstID[instID] = append(signals[:i], signals[i+1:]...)
				log.Printf("OKXMarketDepthWatcher: Removed signal %d for instrument %s", id, instID)

				// Если больше нет сигналов для этого инструмента - очищаем
				if len(w.signalsByInstID[instID]) == 0 {
					delete(w.signalsByInstID, instID)
					delete(w.activeInstruments, instID)
					delete(w.orderBooks, instID)

					// Если это был последний инструмент - останавливаем WebSocket
					if len(w.activeInstruments) == 0 && w.client != nil {
						log.Printf("OKXMarketDepthWatcher: No active instruments left, closing WebSocket connection")
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

						signalDB := &okx_repository.SignalDB{
							ID:       id,
							IsActive: false,
							LastQty:  signal.LastQty,
						}

						if err := w.signalRepository.Update(ctx, signalDB); err != nil {
							log.Printf("OKXMarketDepthWatcher: WARNING: Failed to update signal %d in database: %v", id, err)
						} else {
							log.Printf("OKXMarketDepthWatcher: Signal %d deactivated in database", id)
						}
					}
				}()

				return
			}
		}
	}
	log.Printf("OKXMarketDepthWatcher: WARNING: Signal %d not found for removal", id)
}

// checkAndStopPositionWatcher останавливает PositionWatcher если нет auto-close сигналов
func (w *MarketDepthWatcher) checkAndStopPositionWatcher() {
	// Проверяем есть ли хотя бы один auto-close сигнал
	hasAutoClose := false
	for _, signals := range w.signalsByInstID {
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

	// Если нет auto-close сигналов, останавливаем PositionWatcher
	if !hasAutoClose && w.positionWatcher != nil && w.positionWatcher.IsRunning() {
		log.Printf("OKXMarketDepthWatcher: No auto-close signals left, stopping PositionWatcher for user %d", w.UserID)
		w.positionWatcher.Stop()
		w.positionWatcher = nil
	}
}

// StartConnection запускает WebSocket соединение
func (w *MarketDepthWatcher) StartConnection() error {
	startTime := time.Now()
	defer func() {
		log.Printf("OKXMarketDepthWatcher: StartConnection completed (total time: %v)", time.Since(startTime))
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

	log.Printf("OKXMarketDepthWatcher: Starting connection for user %d", w.UserID)

	// Получаем прокси адрес
	var proxyAddr string
	if w.WebSocketManager != nil {
		if addr, ok := w.WebSocketManager.GetProxyAddressForUser(w.UserID); ok {
			proxyAddr = addr
		}
	}

	// Создаем WebSocket клиент
	var client *WebSocketClient
	if proxyAddr != "" {
		client = NewWebSocketClientWithProxy(proxyAddr)
	} else {
		client = NewWebSocketClient()
	}

	client.OnData = func(data *UnifiedDepthStreamData) {
		w.lastActivityTime = time.Now()
		w.processDepthUpdate(data)
	}

	// Получаем список активных инструментов
	w.mu.RLock()
	instIDs := make([]string, 0, len(w.activeInstruments))
	for instID := range w.activeInstruments {
		instIDs = append(instIDs, instID)
	}
	w.mu.RUnlock()

	if len(instIDs) == 0 {
		log.Printf("OKXMarketDepthWatcher: No instruments to watch, skipping connection")
		w.mu.Lock()
		w.started = false
		w.mu.Unlock()
		return nil
	}

	// Подключаемся
	if err := client.ConnectPublic(w.ctx, instIDs); err != nil {
		log.Printf("OKXMarketDepthWatcher: Failed to connect: %v", err)
		w.mu.Lock()
		w.started = false
		w.mu.Unlock()
		return err
	}

	w.mu.Lock()
	w.client = client
	w.mu.Unlock()

	log.Printf("OKXMarketDepthWatcher: Successfully connected for user %d (took %v)", w.UserID, time.Since(startTime))

	// Запускаем мониторинг активности
	go w.monitorActivity()

	// Запускаем поддержку HTTP соединения
	go w.keepConnectionsAlive()

	return nil
}

// processDepthUpdate обрабатывает обновление ордербука
func (w *MarketDepthWatcher) processDepthUpdate(data *UnifiedDepthStreamData) {
	instID := data.Arg.InstID

	w.mu.Lock()
	defer w.mu.Unlock()

	w.lastActivityTime = time.Now()

	// Получаем или создаём локальный стакан
	ob, ok := w.orderBooks[instID]
	if !ok {
		ob = &OrderBookMap{
			Bids:           make(map[float64]float64),
			Asks:           make(map[float64]float64),
			LastUpdateTime: time.Now(),
		}
		w.orderBooks[instID] = ob
	}

	if len(data.Data) == 0 {
		return
	}

	depthData := data.Data[0]

	// Обновляем bids
	for _, bid := range depthData.Bids {
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
	for _, ask := range depthData.Asks {
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

	ob.LastSeqID = depthData.SeqID
	ob.LastUpdateTime = time.Now()

	// Обрабатываем сигналы ТОЛЬКО для текущего пользователя
	signalsForInst, ok := w.signalsByInstID[instID]
	if !ok {
		return
	}

	var signalsToRemove []int64

	for _, signal := range signalsForInst {
		// Убедимся, что сигнал принадлежит текущему пользователю
		if signal.UserID != w.UserID {
			continue
		}

		found, currentQty := w.findOrderAtPrice(ob, signal.TargetPrice)

		// Проверка на отмену
		if signal.TriggerOnCancel {
			if !found && signal.OriginalQty > 0 {
				triggerTime := time.Now()
				signal.TriggerTime = triggerTime
				log.Printf("OKXMarketDepthWatcher: Signal %d: Order at %.8f disappeared (was %.4f), triggering cancel",
					signal.ID, signal.TargetPrice, signal.LastQty)
				order := &entity.Order{
					Price:        signal.TargetPrice,
					Quantity:     signal.LastQty,
					Side:         signal.OriginalSide,
					InstrumentID: instID,
				}
				if w.onTrigger != nil {
					w.onTrigger(signal, order)
				}
				signalsToRemove = append(signalsToRemove, signal.ID)

				if signal.AutoClose {
					sigCopy := *signal
					orderCopy := *order

					// Определяем ID инструмента для закрытия
					targetCloseInstID := signal.InstID
					if signal.CloseInstID != "" {
						targetCloseInstID = signal.CloseInstID
					}

					// Захватываем данные позиции СИНХРОННО, пока watcher еще работает
					var cachedPos *Position
					if w.positionWatcher != nil && w.positionWatcher.IsRunning() {
						if pos, exists := w.positionWatcher.GetPosition(targetCloseInstID); exists {
							posCopy := *pos // Копируем структуру
							cachedPos = &posCopy
						}
					}

					go func() {
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
				log.Printf("OKXMarketDepthWatcher: Signal %d: Order at %.8f eaten by %.2f%% (%.4f -> %.4f), triggering eat",
					signal.ID, signal.TargetPrice, eatenPercentage*100, signal.OriginalQty, currentQty)
				order := &entity.Order{
					Price:        signal.TargetPrice,
					Quantity:     currentQty,
					Side:         signal.OriginalSide,
					InstrumentID: instID,
				}
				if w.onTrigger != nil {
					w.onTrigger(signal, order)
				}
				signalsToRemove = append(signalsToRemove, signal.ID)

				if signal.AutoClose {
					sigCopy := *signal
					orderCopy := *order

					// Определяем ID инструмента для закрытия
					targetCloseInstID := signal.InstID
					if signal.CloseInstID != "" {
						targetCloseInstID = signal.CloseInstID
					}

					// Захватываем данные позиции СИНХРОННО
					var cachedPos *Position
					if w.positionWatcher != nil && w.positionWatcher.IsRunning() {
						if pos, exists := w.positionWatcher.GetPosition(targetCloseInstID); exists {
							posCopy := *pos
							cachedPos = &posCopy
						}
					}

					go func() {
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

// findOrderAtPrice ищет заявку в локальном ордербуке по точному совпадению цены
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
		log.Printf("OKXMarketDepthWatcher: handleAutoClose for signal %d completed (total time: %v)",
			signal.ID, time.Since(startTime))
	}()

	log.Printf("OKXMarketDepthWatcher: handleAutoClose called for signal %d, user %d, instID %s",
		signal.ID, signal.UserID, signal.InstID)

	// Проверка подписки
	subscribed, err := w.subscriptionService.IsUserSubscribed(context.Background(), signal.UserID)
	if err != nil {
		log.Printf("OKXMarketDepthWatcher: ERROR: IsUserSubscribed failed for user %d: %v", signal.UserID, err)
		return
	}
	if !subscribed {
		log.Printf("OKXMarketDepthWatcher: INFO: User %d is not subscribed, skipping auto-close", signal.UserID)
		return
	}

	// Получаем информацию о позиции
	var posToClose *Position

	// Определяем инструмент для закрытия
	closeInstID := signal.InstID
	if signal.CloseInstID != "" {
		closeInstID = signal.CloseInstID
	}
	// InstType нам пока не критичен для поиска позиции в кэше по ID, но пригодится если будем делать full close

	// 1. Используем захваченную позицию (самый быстрый и надежный вариант)
	if cachedPos != nil {
		posToClose = cachedPos
		log.Printf("OKXMarketDepthWatcher: Using SYNCHRONOUSLY captured position: pos=%.6f, side=%s",
			posToClose.Pos, posToClose.PosSide)
	} else {
		// 2. Если не захватили, пробуем получить из PositionWatcher
		if w.positionWatcher != nil && w.positionWatcher.IsRunning() {
			if pos, exists := w.positionWatcher.GetPosition(closeInstID); exists {
				posToClose = pos
				log.Printf("OKXMarketDepthWatcher: Position from WebSocket (async lookup): %s pos=%.6f, side=%s",
					closeInstID, posToClose.Pos, posToClose.PosSide)
			} else {
				log.Printf("OKXMarketDepthWatcher: Position not found in WebSocket cache for %s", closeInstID)
			}
		}
	}

	// Проверяем есть ли позиция для закрытия
	if posToClose == nil || posToClose.Pos == 0 {
		// 3. Fallback на REST API (медленно, но надежно)
		log.Printf("OKXMarketDepthWatcher: No cached position, fetching via REST API for %s", closeInstID)
		if w.httpClient != nil {
			posResp, err := w.httpClient.GetPositionRisk(closeInstID)
			if err == nil && len(posResp.Data) > 0 {
				p := posResp.Data[0]
				qty, _ := strconv.ParseFloat(p.Pos, 64)
				avgPx, _ := strconv.ParseFloat(p.AvgPx, 64)
				upl, _ := strconv.ParseFloat(p.Upl, 64)

				posToClose = &Position{
					InstID:  p.InstID,
					Pos:     qty,
					PosSide: p.PosSide,
					MgnMode: p.MgnMode,
					AvgPx:   avgPx,
					Upl:     upl,
				}
				log.Printf("OKXMarketDepthWatcher: Position from REST API: %s pos=%.6f, side=%s",
					closeInstID, posToClose.Pos, posToClose.PosSide)
			}
		}
	}

	if posToClose == nil || posToClose.Pos == 0 {
		log.Printf("OKXMarketDepthWatcher: No position to close (tried Cache, WS, REST) for %s", closeInstID)
		return
	}

	// Закрытие позиции через HTTP REST API
	orderStartTime := time.Now()

	log.Printf("OKXMarketDepthWatcher: Closing position for %s (size: %.6f, side: %s, mode: %s)",
		closeInstID, posToClose.Pos, posToClose.PosSide, posToClose.MgnMode)

	manager := NewOrderManager(w.httpClient)
	// Используем ClosePositionWithSize который НЕ делает лишний fetch
	closeErr := manager.ClosePositionWithSize(closeInstID, posToClose.Pos, posToClose.PosSide, posToClose.MgnMode)

	orderDuration := time.Since(orderStartTime)

	if closeErr != nil {
		log.Printf("OKXMarketDepthWatcher: ERROR: Position close failed for user %d: %v (order execution time: %v)",
			signal.UserID, closeErr, orderDuration)
		return
	}

	totalDuration := time.Since(startTime)
	log.Printf("OKXMarketDepthWatcher: ✅ SUCCESS: Position closed for user %d on %s (size: %.6f) | Order time: %v | Total time: %v",
		signal.UserID, closeInstID, posToClose.Pos, orderDuration, totalDuration)

	// Плавная остановка прокси
	if w.WebSocketManager != nil {
		go func() {
			time.Sleep(1 * time.Second)
			w.WebSocketManager.GracefulStopProxyForUser(signal.UserID)
		}()
	}
}

// fallbackHTTPClose использует HTTP REST API для закрытия позиции
func (w *MarketDepthWatcher) fallbackHTTPClose(signal *Signal) error {
	if w.httpClient == nil {
		return fmt.Errorf("HTTP client not available")
	}
	manager := NewOrderManager(w.httpClient)
	return manager.CloseFullPosition(signal.InstID, signal.InstType)
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
				log.Printf("OKXMarketDepthWatcher: WARNING: No activity for %v", inactiveDuration)
				w.mu.Lock()
				if w.started && w.client != nil {
					log.Printf("OKXMarketDepthWatcher: Attempting to reconnect due to inactivity")
					w.client.Close()
					w.client = nil
					w.started = false
					go func() {
						if err := w.StartConnection(); err != nil {
							log.Printf("OKXMarketDepthWatcher: ERROR: Reconnection failed: %v", err)
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
	// Пингуем каждые 15 секунд (агрессивный keep-alive)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	log.Printf("OKXMarketDepthWatcher: HTTP Keep-Alive pinger started for user %d", w.UserID)

	// Делаем первый пинг сразу чтобы прогреть соединение
	go func() {
		w.mu.RLock()
		client := w.httpClient
		w.mu.RUnlock()
		if client != nil {
			if err := client.Ping(); err != nil {
				log.Printf("OKXMarketDepthWatcher: Initial keep-alive ping failed: %v", err)
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
				log.Printf("OKXMarketDepthWatcher: Pinger stopped as watcher is not started")
				return
			}

			if client != nil {
				// Отправляем легкий запрос для поддержки соединения
				if err := client.Ping(); err != nil {
					log.Printf("OKXMarketDepthWatcher: Keep-alive ping failed: %v", err)
				}
				// Логировать успешный пинг не будем, чтобы не засорять логи
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
