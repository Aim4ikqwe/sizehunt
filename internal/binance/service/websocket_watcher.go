// internal/binance/service/websocket_watcher.go
package service

import (
	"context"

	"log"
	"runtime"
	"strconv"
	"sync"
	"time"

	"sizehunt/internal/binance/entity"
	binance_repository "sizehunt/internal/binance/repository"
	"sizehunt/internal/config"
	subscriptionservice "sizehunt/internal/subscription/service"

	"github.com/adshao/go-binance/v2/futures"
	"github.com/sony/gobreaker"
)

type Signal struct {
	ID              int64
	UserID          int64
	Symbol          string
	TargetPrice     float64
	MinQuantity     float64
	TriggerOnCancel bool
	TriggerOnEat    bool
	EatPercentage   float64 // 0.5 = 50%
	OriginalQty     float64 // объём заявки при создании
	LastQty         float64 // текущий объём
	AutoClose       bool
	CloseMarket     string // "spot" или "futures" - рынок для закрытия
	WatchMarket     string // "spot" или "futures" - рынок для мониторинга
	// --- ДОБАВЛЕННЫЕ ПОЛЯ ---
	OriginalSide     string    // "BUY" или "SELL" - сторона заявки при создании
	TriggerTime      time.Time // Время срабатывания триггера (установлено в processDepthUpdate)
	BinanceEventTime time.Time // Время события на бирже (из WebSocket)
	CreatedAt        time.Time // Время создания сигнала
}

// OrderBookMap представляет локальный ордербук для одного символа
type OrderBookMap struct {
	Bids map[float64]float64 // цена -> объём
	Asks map[float64]float64 // цена -> объём
	// Добавим LastUpdateID для проверки согласованности
	LastUpdateID   int64
	LastUpdateTime time.Time
}

// MarketDepthWatcher отвечает за один рынок (spot или futures) и управляет сигналами и локальным ордербуком для разных символов на этом рынке
type MarketDepthWatcher struct {
	client              *WebSocketClient
	signalsBySymbol     map[string][]*Signal     // symbol -> []signals
	orderBooks          map[string]*OrderBookMap // symbol -> OrderBookMap (локальный ордербук)
	mu                  sync.RWMutex
	onTrigger           func(signal *Signal, order *entity.Order)
	subscriptionService *subscriptionservice.Service
	keysRepo            *binance_repository.PostgresKeysRepo
	config              *config.Config
	activeSymbols       map[string]bool // символы, за которыми мы сейчас следим
	market              string          // "spot" или "futures"
	ctx                 context.Context // Контекст для всего watcher'а
	started             bool            // Флаг, указывающий, был ли запущен watcher
	futuresClient       *futures.Client
	positionWatcher     *PositionWatcher
	userDataStream      *UserDataStream
	creationTime        time.Time // время создания watcher'а
	lastActivityTime    time.Time // время последней активности
	signalRepository    binance_repository.SignalRepository
	WebSocketManager    *WebSocketManager
	UserID              int64
}

func NewMarketDepthWatcher(
	ctx context.Context,
	market string,
	subService *subscriptionservice.Service,
	keysRepo *binance_repository.PostgresKeysRepo,
	cfg *config.Config,
	futuresClient *futures.Client,
	positionWatcher *PositionWatcher,
	userDataStream *UserDataStream,
	signalRepo binance_repository.SignalRepository,
	wsManager *WebSocketManager,
	userID int64,
) *MarketDepthWatcher {
	// Инициализируем мапы
	signalsBySymbol := make(map[string][]*Signal)
	orderBooks := make(map[string]*OrderBookMap)
	activeSymbols := make(map[string]bool)

	watcher := &MarketDepthWatcher{
		client:              nil,
		signalsBySymbol:     signalsBySymbol,
		orderBooks:          orderBooks,
		activeSymbols:       activeSymbols,
		mu:                  sync.RWMutex{},
		onTrigger:           nil,
		subscriptionService: subService,
		keysRepo:            keysRepo,
		config:              cfg,
		market:              market,
		ctx:                 ctx,
		started:             false,
		futuresClient:       futuresClient,
		positionWatcher:     positionWatcher,
		userDataStream:      userDataStream,
		creationTime:        time.Now(),
		lastActivityTime:    time.Now(),
		signalRepository:    signalRepo,
		WebSocketManager:    wsManager,
		UserID:              userID,
	}

	log.Printf("MarketDepthWatcher: Created new watcher for market %s (creation time: %v)", market, watcher.creationTime)
	return watcher
}

func (w *MarketDepthWatcher) AddSignal(signal *Signal) {
	startTime := time.Now()
	defer func() {
		log.Printf("MarketDepthWatcher: AddSignal completed (total time: %v)", time.Since(startTime))
	}()

	w.mu.Lock()
	defer w.mu.Unlock()

	signal.CreatedAt = time.Now()
	w.lastActivityTime = time.Now()

	// Проверяем, что сигнал для правильного рынка
	if signal.WatchMarket != w.market {
		log.Printf("MarketDepthWatcher: ERROR: Attempting to add signal for market %s to watcher for market %s", signal.WatchMarket, w.market)
		return
	}

	log.Printf("MarketDepthWatcher: Adding signal %d for user %d, symbol %s, price %.8f, originalQty %.4f, closeMarket %s, side %s",
		signal.ID, signal.UserID, signal.Symbol, signal.TargetPrice, signal.OriginalQty, signal.CloseMarket, signal.OriginalSide)

	// Добавляем сигнал в список для его символа
	currentSignals := w.signalsBySymbol[signal.Symbol]
	w.signalsBySymbol[signal.Symbol] = append(currentSignals, signal)
	log.Printf("MarketDepthWatcher: Signal %d added. Total signals for %s: %d",
		signal.ID, signal.Symbol, len(w.signalsBySymbol[signal.Symbol]))

	_, wasActive := w.activeSymbols[signal.Symbol]
	w.activeSymbols[signal.Symbol] = true // Помечаем символ как активный

	// Инициализируем локальный стакан для символа, если его ещё нет
	ob, ok := w.orderBooks[signal.Symbol]
	if !ok {
		ob = &OrderBookMap{
			Bids:           make(map[float64]float64),
			Asks:           make(map[float64]float64),
			LastUpdateTime: time.Now(),
		}
		w.orderBooks[signal.Symbol] = ob
		log.Printf("MarketDepthWatcher: Created new orderbook for symbol %s", signal.Symbol)
	}

	// Проверяем, существует ли уровень на TargetPrice
	_, bidExists := ob.Bids[signal.TargetPrice]
	_, askExists := ob.Asks[signal.TargetPrice]

	// Если уровень не существует, устанавливаем его
	if !bidExists && !askExists && signal.OriginalQty > 0 {
		switch signal.OriginalSide {
		case "BUY":
			ob.Bids[signal.TargetPrice] = signal.OriginalQty
			log.Printf("MarketDepthWatcher: Initialized local orderbook for symbol %s at price %.8f with qty %.4f in Bids", signal.Symbol, signal.TargetPrice, signal.OriginalQty)
		case "SELL":
			ob.Asks[signal.TargetPrice] = signal.OriginalQty
			log.Printf("MarketDepthWatcher: Initialized local orderbook for symbol %s at price %.8f with qty %.4f in Asks", signal.Symbol, signal.TargetPrice, signal.OriginalQty)
		default:
			// Если OriginalSide неизвестен, добавляем в обе стороны
			ob.Bids[signal.TargetPrice] = signal.OriginalQty
			ob.Asks[signal.TargetPrice] = signal.OriginalQty
			log.Printf("MarketDepthWatcher: Initialized local orderbook for symbol %s at price %.8f with qty %.4f in BOTH Bids and Asks (unknown side)", signal.Symbol, signal.TargetPrice, signal.OriginalQty)
		}
		ob.LastUpdateTime = time.Now()
	} else {
		existingBidQty := ob.Bids[signal.TargetPrice]
		existingAskQty := ob.Asks[signal.TargetPrice]
		log.Printf("MarketDepthWatcher: Local orderbook for symbol %s at price %.8f already initialized or OriginalQty is 0, bids: %.4f, asks: %.4f",
			signal.Symbol, signal.TargetPrice, existingBidQty, existingAskQty)
	}

	// Если символ был неактивен и соединение не установлено, запускаем его
	if !wasActive && !w.started {
		log.Printf("MarketDepthWatcher: First signal for symbol %s, connection will be started asynchronously", signal.Symbol)
		go func() {
			if err := w.StartConnection(); err != nil {
				log.Printf("MarketDepthWatcher: ERROR: Failed to start connection after adding first symbol: %v", err)
				w.mu.Lock()
				w.started = false
				w.mu.Unlock()
			} else {
				log.Printf("MarketDepthWatcher: Connection started successfully for symbol %s", signal.Symbol)
			}
		}()
	}
}

func (w *MarketDepthWatcher) RemoveSignal(id int64) {
	startTime := time.Now()
	defer func() {
		log.Printf("MarketDepthWatcher: RemoveSignal completed (total time: %v)", time.Since(startTime))
	}()

	w.mu.Lock()
	defer w.mu.Unlock()

	w.lastActivityTime = time.Now()
	w.removeSignalByIDLocked(id)
}
func (w *MarketDepthWatcher) removeSignalByIDLocked(id int64) {
	for symbol, signals := range w.signalsBySymbol {
		for i, signal := range signals {
			if signal.ID == id {
				// Удаляем сигнал из слайса
				w.signalsBySymbol[symbol] = append(signals[:i], signals[i+1:]...)
				log.Printf("MarketDepthWatcher: Removed signal %d for symbol %s", id, symbol)

				// Если больше нет сигналов для этого символа - помечаем символ как неактивный
				if len(w.signalsBySymbol[symbol]) == 0 {
					delete(w.signalsBySymbol, symbol)
					delete(w.activeSymbols, symbol)

					// Очищаем ордербук для символа
					if _, exists := w.orderBooks[symbol]; exists {
						delete(w.orderBooks, symbol)
						log.Printf("MarketDepthWatcher: Orderbook cleaned up for symbol %s", symbol)
					}

					// Если это был последний символ - останавливаем WebSocket
					if len(w.activeSymbols) == 0 {
						if w.client != nil {
							log.Printf("MarketDepthWatcher: No active symbols left, closing WebSocket connection for market %s", w.market)
							w.client.Close()
							w.client = nil
						}
						w.started = false
						log.Printf("MarketDepthWatcher: WebSocket connection closed and marked as not started")
					}
				}

				// Обновляем сигнал в БД асинхронно
				go func() {
					if w.signalRepository != nil {
						ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
						defer cancel()

						signalDB := &binance_repository.SignalDB{
							ID:       id,
							IsActive: false,
							LastQty:  signal.LastQty,
						}

						if err := w.signalRepository.Update(ctx, signalDB); err != nil {
							log.Printf("MarketDepthWatcher: WARNING: Failed to update signal %d in database: %v", id, err)
						} else {
							log.Printf("MarketDepthWatcher: Signal %d deactivated in database", id)
						}
					}
				}()

				return
			}
		}
	}
	log.Printf("MarketDepthWatcher: WARNING: Signal %d not found for removal", id)
}

// startConnection вызывается только один раз при добавлении первого символа

func (w *MarketDepthWatcher) StartConnection() error {
	startTime := time.Now()
	defer func() {
		log.Printf("MarketDepthWatcher: StartConnection completed (total time: %v)", time.Since(startTime))
	}()

	// Проверяем, не запущен ли уже watcher
	w.mu.RLock()
	if w.started {
		w.mu.RUnlock()
		log.Printf("MarketDepthWatcher: Connection already started for market %s", w.market)
		return nil
	}
	w.mu.RUnlock()

	// Блокируем только для установки флага started
	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return nil
	}
	w.started = true
	w.mu.Unlock()

	log.Printf("MarketDepthWatcher: Starting connection for market %s", w.market)

	// Получаем прокси адрес для пользователя
	var proxyAddr string
	if w.WebSocketManager != nil {
		if addr, ok := w.WebSocketManager.GetProxyAddressForUser(w.UserID); ok {
			proxyAddr = addr
		}
	}

	// Создаем клиент с поддержкой прокси
	var client *WebSocketClient
	if proxyAddr != "" {
		client = NewWebSocketClientWithProxy(proxyAddr)
	} else {
		client = NewWebSocketClient()
	}

	client.OnData = func(data *UnifiedDepthStreamData) {
		w.lastActivityTime = time.Now()
		w.processDepthUpdateAsync(data)
	}

	// Получаем список активных символов без блокировки
	w.mu.RLock()
	symbols := make([]string, 0, len(w.activeSymbols))
	for symbol := range w.activeSymbols {
		symbols = append(symbols, symbol)
	}
	w.mu.RUnlock()

	if len(symbols) == 0 {
		log.Printf("MarketDepthWatcher: WARNING: No symbols to watch for market %s, skipping connection.", w.market)
		w.mu.Lock()
		w.started = false
		w.mu.Unlock()
		return nil
	}

	var err error
	switch w.market {
	case "spot":
		log.Printf("MarketDepthWatcher: Connecting to spot combined WebSocket for %d symbols", len(symbols))
		err = client.ConnectForSpotCombined(w.ctx, symbols)
	case "futures":
		symbolLevels := make(map[string]string)
		for _, symbol := range symbols {
			symbolLevels[symbol] = "" // "" означает дифф-поток без фиксированного уровня
		}
		log.Printf("MarketDepthWatcher: Connecting to futures combined WebSocket for %d symbols", len(symbolLevels))
		err = client.ConnectForFuturesCombined(w.ctx, symbolLevels)
	}

	if err != nil {
		log.Printf("MarketDepthWatcher: ERROR: Failed to connect to combined WebSocket for %s: %v", w.market, err)
		w.mu.Lock()
		w.started = false
		w.mu.Unlock()
		return err
	}

	w.mu.Lock()
	w.client = client
	w.mu.Unlock()

	log.Printf("MarketDepthWatcher: Successfully connected to combined WebSocket for market: %s (took %v)",
		w.market, time.Since(startTime))

	// Запускаем горутину для мониторинга активности
	go w.monitorActivity()

	return nil
}

func (w *MarketDepthWatcher) monitorActivity() {
	log.Printf("MarketDepthWatcher: Activity monitoring started for market %s", w.market)
	ticker := time.NewTicker(10 * time.Second) // Сокращено до 10 секунд
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
				log.Printf("MarketDepthWatcher: Not started yet, skipping activity check")
				continue
			}
			log.Printf("MarketDepthWatcher: Last activity was %v ago for market %s", inactiveDuration, w.market)
			if inactiveDuration > 30*time.Second { // Сокращено до 30 секунд
				log.Printf("MarketDepthWatcher: WARNING: No activity for %v, market %s might be disconnected",
					inactiveDuration, w.market)
				// Попробуем переподключиться
				w.mu.Lock()
				if w.started && w.client != nil {
					log.Printf("MarketDepthWatcher: Attempting to reconnect due to inactivity")
					w.client.Close()
					w.client = nil
					w.started = false
					// Перезапускаем соединение
					go func() {
						if err := w.StartConnection(); err != nil {
							log.Printf("MarketDepthWatcher: ERROR: Reconnection failed: %v", err)
						} else {
							log.Printf("MarketDepthWatcher: Successfully reconnected after inactivity")
						}
					}()
				}
				w.mu.Unlock()
			}
		case <-w.ctx.Done():
			log.Printf("MarketDepthWatcher: Activity monitoring stopped due to context cancellation")
			return
		}
	}
}

func (w *MarketDepthWatcher) processDepthUpdate(data *UnifiedDepthStreamData) {
	startTime := time.Now()
	defer func() {
		log.Printf("MarketDepthWatcher: processDepthUpdate for %s completed (total time: %v)",
			data.Data.Symbol, time.Since(startTime))
	}()

	symbol := data.Data.Symbol

	w.mu.RLock()
	// Проверяем, активен ли символ и есть ли сигналы
	_, _ = w.orderBooks[symbol]
	signalsCount := len(w.signalsBySymbol[symbol])
	active := w.activeSymbols[symbol]
	w.mu.RUnlock()

	// Если символ не активен или нет сигналов, пропускаем обработку
	if !active || signalsCount == 0 {
		log.Printf("MarketDepthWatcher: Skipping update for %s - no active signals", symbol)
		return
	}

	// Обрабатываем данные
	binanceEventTime := time.UnixMilli(data.Data.EventTime)
	receivedTime := time.Now()
	networkLatency := receivedTime.Sub(binanceEventTime)

	log.Printf("MarketDepthWatcher: Processing depth update for %s (%s), EventTime: %v, Received: %v, Latency: %v",
		symbol, w.market, binanceEventTime, receivedTime, networkLatency)

	// Обрабатываем сигналы для символа
	var signalsToRemove []int64

	w.mu.Lock()
	defer w.mu.Unlock()

	w.lastActivityTime = time.Now()

	// Получаем или создаём локальный стакан для символа
	ob, ok := w.orderBooks[symbol]
	if !ok {
		ob = &OrderBookMap{
			Bids:           make(map[float64]float64),
			Asks:           make(map[float64]float64),
			LastUpdateTime: time.Now(),
		}
		w.orderBooks[symbol] = ob
		log.Printf("MarketDepthWatcher: Created new orderbook for symbol %s during update processing", symbol)
	}

	// Обновляем bids
	for _, bidUpdate := range data.Data.Bids {
		price, _ := strconv.ParseFloat(bidUpdate[0], 64)
		qty, _ := strconv.ParseFloat(bidUpdate[1], 64)
		if qty == 0 {
			delete(ob.Bids, price)
		} else {
			ob.Bids[price] = qty
		}
	}

	// Обновляем asks
	for _, askUpdate := range data.Data.Asks {
		price, _ := strconv.ParseFloat(askUpdate[0], 64)
		qty, _ := strconv.ParseFloat(askUpdate[1], 64)
		if qty == 0 {
			delete(ob.Asks, price)
		} else {
			ob.Asks[price] = qty
		}
	}

	ob.LastUpdateID = data.Data.LastUpdateID
	ob.LastUpdateTime = time.Now()

	log.Printf("MarketDepthWatcher: Orderbook updated for %s. Total bids: %d, total asks: %d",
		symbol, len(ob.Bids), len(ob.Asks))

	// Обрабатываем сигналы
	signalsForSymbol, ok := w.signalsBySymbol[symbol]
	if !ok {
		log.Printf("MarketDepthWatcher: No signals found for symbol %s", symbol)
		return
	}

	log.Printf("MarketDepthWatcher: Found %d signals to process for symbol %s", len(signalsForSymbol), symbol)

	for _, signal := range signalsForSymbol {
		found, currentQty := w.findOrderAtPrice(ob, signal.TargetPrice)
		log.Printf("MarketDepthWatcher: Processing signal %d for price %.8f: found=%v, currentQty=%.4f, originalQty=%.4f",
			signal.ID, signal.TargetPrice, found, currentQty, signal.OriginalQty)

		// Проверка на отмену
		if signal.TriggerOnCancel {
			if !found && signal.OriginalQty > 0 {
				triggerTime := time.Now()
				signal.TriggerTime = triggerTime
				signal.BinanceEventTime = binanceEventTime
				log.Printf("MarketDepthWatcher: Signal %d: Order at %.8f disappeared (was %.4f), triggering cancel at %v",
					signal.ID, signal.TargetPrice, signal.LastQty, triggerTime)
				order := &entity.Order{
					Price:    signal.TargetPrice,
					Quantity: signal.LastQty,
					Side:     signal.OriginalSide,
				}
				if w.onTrigger != nil {
					log.Printf("MarketDepthWatcher: Calling onTrigger callback for signal %d", signal.ID)
					w.onTrigger(signal, order)
				}
				signalsToRemove = append(signalsToRemove, signal.ID)

				if signal.AutoClose {
					log.Printf("MarketDepthWatcher: Signal %d: Scheduling handleAutoClose for cancel (async, after signal removal)", signal.ID)
					sigCopy := *signal
					orderCopy := *order
					go func() {
						w.handleAutoClose(&sigCopy, &orderCopy)
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
			log.Printf("MarketDepthWatcher: Signal %d: Order at %.8f eaten by %.2f%% (%.4f -> %.4f), threshold: %.2f%%",
				signal.ID, signal.TargetPrice, eatenPercentage*100, signal.OriginalQty, currentQty, signal.EatPercentage*100)
			if eatenPercentage >= signal.EatPercentage {
				triggerTime := time.Now()
				signal.TriggerTime = triggerTime
				signal.BinanceEventTime = binanceEventTime
				log.Printf("MarketDepthWatcher: Signal %d: Order at %.8f eaten by %.2f%% (%.4f -> %.4f), triggering eat at %v",
					signal.ID, signal.TargetPrice, eatenPercentage*100, signal.OriginalQty, currentQty, triggerTime)
				order := &entity.Order{
					Price:    signal.TargetPrice,
					Quantity: currentQty,
					Side:     signal.OriginalSide,
				}
				if w.onTrigger != nil {
					log.Printf("MarketDepthWatcher: Calling onTrigger callback for signal %d", signal.ID)
					w.onTrigger(signal, order)
				}
				signalsToRemove = append(signalsToRemove, signal.ID)

				if signal.AutoClose {
					log.Printf("MarketDepthWatcher: Signal %d: Scheduling handleAutoClose for eat (async, after signal removal)", signal.ID)
					sigCopy := *signal
					orderCopy := *order
					go func() {
						w.handleAutoClose(&sigCopy, &orderCopy)
					}()
				}
				continue
			}
		}

		// Обновляем LastQty, если заявка найдена
		if found {
			if signal.LastQty != currentQty {
				change := currentQty - signal.LastQty
				log.Printf("MarketDepthWatcher: Signal %d: Order quantity updated: %.4f -> %.4f (change: %.4f)",
					signal.ID, signal.LastQty, currentQty, change)
			}
			signal.LastQty = currentQty
		} else {
			// Если заявка не найдена, но была найдена ранее, устанавливаем 0
			if signal.LastQty > 0 {
				log.Printf("MarketDepthWatcher: Signal %d: Order disappeared, setting LastQty to 0", signal.ID)
				signal.LastQty = 0
			}
		}
	}

	// Удаляем все сработавшие сигналы
	for _, id := range signalsToRemove {
		log.Printf("MarketDepthWatcher: Removing triggered signal %d immediately after trigger", id)
		w.removeSignalByIDLocked(id)
	}

	log.Printf("MarketDepthWatcher: Depth update processing completed for %s. Removed %d signals.",
		symbol, len(signalsToRemove))
}

// 🔥 НОВЫЙ МЕТОД: удаление сигнала по ID с полной очисткой ресурсов
func (w *MarketDepthWatcher) removeSignalByID(id int64) {
	for symbol, signals := range w.signalsBySymbol {
		for i, signal := range signals {
			if signal.ID == id {
				// Удаляем сигнал из слайса
				w.signalsBySymbol[symbol] = append(signals[:i], signals[i+1:]...)
				log.Printf("MarketDepthWatcher: Signal %d completely removed from monitoring", id)
				go func() {
					if w.signalRepository != nil {
						signalDB := &binance_repository.SignalDB{
							ID:       id,
							IsActive: false,
							LastQty:  signal.LastQty, // Сохраняем последнее состояние
						}

						if err := w.signalRepository.Update(context.Background(), signalDB); err != nil {
							log.Printf("MarketDepthWatcher: WARNING: Failed to update signal %d in database: %v", id, err)
						} else {
							log.Printf("MarketDepthWatcher: Signal %d deactivated in database", id)
						}
					}
				}()
			}
			// Если больше нет сигналов для этого символа - очищаем ресурсы
			if len(w.signalsBySymbol[symbol]) == 0 {
				delete(w.signalsBySymbol, symbol)
				delete(w.activeSymbols, symbol)
				delete(w.orderBooks, symbol)
				log.Printf("MarketDepthWatcher: All resources cleaned up for symbol %s after last signal removal", symbol)

				// Если это был последний символ - останавливаем WebSocket
				if len(w.activeSymbols) == 0 && w.client != nil {
					log.Printf("MarketDepthWatcher: No active symbols left, closing WebSocket connection")
					w.client.Close()
					w.client = nil
					w.started = false
				}
			}
			return
		}
	}

	log.Printf("MarketDepthWatcher: Signal %d not found for removal", id)
}

// findOrderAtPrice ищет заявку в локальном OrderBookMap по точному совпадению цены
func (w *MarketDepthWatcher) findOrderAtPrice(ob *OrderBookMap, targetPrice float64) (found bool, qty float64) {
	// log.Printf("MarketDepthWatcher: findOrderAtPrice: Looking for exact price %.8f", targetPrice) // <-- УБРАНО
	// Ищем в bids
	if qty, ok := ob.Bids[targetPrice]; ok {
		// log.Printf("MarketDepthWatcher: findOrderAtPrice: Found exact bid at %.8f with quantity %.4f", targetPrice, qty) // <-- УБРАНО
		return true, qty
	}
	// Ищем в asks
	if qty, ok := ob.Asks[targetPrice]; ok {
		// log.Printf("MarketDepthWatcher: findOrderAtPrice: Found exact ask at %.8f with quantity %.4f", targetPrice, qty) // <-- УБРАНО
		return true, qty
	}
	// log.Printf("MarketDepthWatcher: findOrderAtPrice: No exact order found at price %.8f", targetPrice) // <-- УБРАНО
	return false, 0
}

func (w *MarketDepthWatcher) handleAutoClose(signal *Signal, order *entity.Order) {
	startTime := time.Now()
	defer func() {
		log.Printf("MarketDepthWatcher: handleAutoClose for signal %d completed (total time: %v)",
			signal.ID, time.Since(startTime))
	}()
	if signal.CloseMarket != "futures" {
		log.Printf("MarketDepthWatcher: ERROR: handleAutoClose for non-futures market %s not implemented", signal.CloseMarket)
		return
	}
	log.Printf("MarketDepthWatcher: handleAutoClose called for signal %d, user %d", signal.ID, signal.UserID)
	// Проверка подписки (остаётся)
	subscribed, err := w.subscriptionService.IsUserSubscribed(context.Background(), signal.UserID)
	if err != nil {
		log.Printf("MarketDepthWatcher: ERROR: IsUserSubscribed failed for user %d: %v", signal.UserID, err)
		return
	}
	if !subscribed {
		log.Printf("MarketDepthWatcher: INFO: User %d is not subscribed, skipping auto-close", signal.UserID)
		return
	}
	// Создание OrderManager с уже существующими зависимостями
	manager := NewOrderManager(w.futuresClient, w.positionWatcher)
	// Вызов нового метода CloseFullPosition (без side!)
	log.Printf("MarketDepthWatcher: Attempting to close position for %s", signal.Symbol)
	err = manager.CloseFullPosition(signal.Symbol)
	if err != nil {
		log.Printf("MarketDepthWatcher: ERROR: CloseFullPosition failed for user %d: %v", signal.UserID, err)
		return
	}
	endTime := time.Since(startTime)
	log.Printf("MarketDepthWatcher: operation ended %v", endTime)
	timeNow := time.Now()
	log.Printf("timeNow: %v", timeNow)
	log.Printf("MarketDepthWatcher: SUCCESS: FULL Position closed for user %d on %s", signal.UserID, signal.CloseMarket)

	// 🔥 КЛЮЧЕВОЕ ИЗМЕНЕНИЕ: Не останавливаем userDataStream здесь!
	// Вместо этого вызываем проверку на уровне WebSocketManager
	if w.WebSocketManager != nil {
		log.Printf("MarketDepthWatcher: Scheduling check for UserDataStream stop for user %d", signal.UserID)
		go func() {
			// Небольшая задержка для обеспечения обновления состояния сигналов
			time.Sleep(500 * time.Millisecond)
			w.WebSocketManager.CheckAndStopUserDataStream(signal.UserID)
		}()
	}
}

func (w *MarketDepthWatcher) SetOnTrigger(fn func(signal *Signal, order *entity.Order)) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.onTrigger = fn
	log.Printf("MarketDepthWatcher: OnTrigger callback set")
}

// GetOrderBook возвращает копию локального ордербука для символа
func (w *MarketDepthWatcher) GetOrderBook(symbol string) *OrderBookMap {
	startTime := time.Now()
	defer func() {
		log.Printf("MarketDepthWatcher: GetOrderBook for %s took %v", symbol, time.Since(startTime))
	}()

	w.mu.RLock()
	defer w.mu.RUnlock()

	ob, ok := w.orderBooks[symbol]
	if !ok {
		log.Printf("MarketDepthWatcher: WARNING: Orderbook not found for symbol %s", symbol)
		return nil
	}

	// Создаём копию для потенциальной передачи в handler
	copiedOB := &OrderBookMap{
		Bids:           make(map[float64]float64),
		Asks:           make(map[float64]float64),
		LastUpdateID:   ob.LastUpdateID,
		LastUpdateTime: ob.LastUpdateTime,
	}

	for price, qty := range ob.Bids {
		copiedOB.Bids[price] = qty
	}

	for price, qty := range ob.Asks {
		copiedOB.Asks[price] = qty
	}

	log.Printf("MarketDepthWatcher: GetOrderBook for %s: %d bids, %d asks",
		symbol, len(copiedOB.Bids), len(copiedOB.Asks))

	return copiedOB
}

// internal/binance/service/websocket_watcher.go

func (w *MarketDepthWatcher) RemoveAllSignalsForSymbol(symbol string) {
	startTime := time.Now()
	defer func() {
		log.Printf("MarketDepthWatcher: RemoveAllSignalsForSymbol for %s completed (total time: %v)",
			symbol, time.Since(startTime))
	}()

	w.mu.Lock()
	defer w.mu.Unlock()

	if signals, exists := w.signalsBySymbol[symbol]; exists {
		log.Printf("MarketDepthWatcher: Removing %d signals for symbol %s", len(signals), symbol)
		for _, sig := range signals {
			w.removeSignalByIDLocked(sig.ID)
		}
	} else {
		log.Printf("MarketDepthWatcher: No signals found for symbol %s to remove", symbol)
	}
}

// GetStatus возвращает статус watcher'а для мониторинга
func (w *MarketDepthWatcher) GetStatus() map[string]interface{} {
	w.mu.RLock()
	defer w.mu.RUnlock()

	status := make(map[string]interface{})

	status["market"] = w.market
	status["started"] = w.started
	status["creation_time"] = w.creationTime
	status["last_activity_time"] = w.lastActivityTime
	status["inactive_duration"] = time.Since(w.lastActivityTime)

	symbolsStatus := make(map[string]interface{})
	for symbol := range w.activeSymbols {
		symbolsStatus[symbol] = map[string]int{
			"signal_count": len(w.signalsBySymbol[symbol]),
			"bid_count":    len(w.orderBooks[symbol].Bids),
			"ask_count":    len(w.orderBooks[symbol].Asks),
		}
	}

	status["symbols"] = symbolsStatus
	status["total_signals"] = w.countTotalSignals()
	status["goroutines"] = runtime.NumGoroutine()

	return status
}

func (w *MarketDepthWatcher) countTotalSignals() int {
	total := 0
	for _, signals := range w.signalsBySymbol {
		total += len(signals)
	}
	return total
}
func (w *MarketDepthWatcher) GetAllSignalsForSymbol(symbol string) []*Signal {
	w.mu.RLock()
	defer w.mu.RUnlock()

	signals, exists := w.signalsBySymbol[symbol]
	if !exists {
		return []*Signal{}
	}

	// Создаем копию для безопасного возврата
	result := make([]*Signal, len(signals))
	copy(result, signals)
	return result
}
func (w *MarketDepthWatcher) HasSignal(signalID int64) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()

	for _, signals := range w.signalsBySymbol {
		for _, s := range signals {
			if s.ID == signalID {
				return true
			}
		}
	}

	return false
}
func (w *MarketDepthWatcher) GetAllSignals() []*Signal {
	w.mu.RLock()
	defer w.mu.RUnlock()

	var allSignals []*Signal
	for _, signals := range w.signalsBySymbol {
		allSignals = append(allSignals, signals...)
	}

	return allSignals
}
func (w *MarketDepthWatcher) AddSignalAsync(signal *Signal) {
	// Сохраняем сигнал в БД в отдельной горутине
	if w.signalRepository != nil {
		go func() {
			signalDB := &binance_repository.SignalDB{
				UserID:          signal.UserID,
				Symbol:          signal.Symbol,
				TargetPrice:     signal.TargetPrice,
				MinQuantity:     signal.MinQuantity,
				TriggerOnCancel: signal.TriggerOnCancel,
				TriggerOnEat:    signal.TriggerOnEat,
				EatPercentage:   signal.EatPercentage,
				OriginalQty:     signal.OriginalQty,
				LastQty:         signal.LastQty,
				AutoClose:       signal.AutoClose,
				CloseMarket:     signal.CloseMarket,
				WatchMarket:     signal.WatchMarket,
				OriginalSide:    signal.OriginalSide,
				CreatedAt:       signal.CreatedAt,
			}

			startTime := time.Now()
			if err := w.signalRepository.Save(context.Background(), signalDB); err != nil {
				log.Printf("MarketDepthWatcher: ERROR: Failed to save signal to database: %v", err)
			} else {
				signal.ID = signalDB.ID
				log.Printf("MarketDepthWatcher: Signal saved to database with ID %d (took %v)", signal.ID, time.Since(startTime))
			}
		}()
	}

	// Добавляем сигнал в структуры данных
	w.AddSignal(signal)
}
func (w *MarketDepthWatcher) processDepthUpdateAsync(data *UnifiedDepthStreamData) {
	go func() {
		startTime := time.Now()
		defer func() {
			log.Printf("MarketDepthWatcher: processDepthUpdateAsync for %s completed (total time: %v)",
				data.Data.Symbol, time.Since(startTime))
		}()

		w.processDepthUpdate(data)
	}()
}
func (w *Watcher) GetFuturesCB() *gobreaker.CircuitBreaker {
	if httpClient, ok := w.client.(*BinanceHTTPClient); ok {
		return httpClient.futuresCB
	}
	return nil
}
