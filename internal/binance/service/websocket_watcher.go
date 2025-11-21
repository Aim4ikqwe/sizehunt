// internal/binance/service/websocket_watcher.go
package service

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	"sizehunt/internal/binance/entity"
	binance_repository "sizehunt/internal/binance/repository"
	"sizehunt/internal/config"
	subscriptionservice "sizehunt/internal/subscription/service"
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
	// --- КОНЕЦ ДОБАВЛЕННЫХ ПОЛЕЙ ---
	AutoCloseExecuted bool
}

// OrderBookMap представляет локальный ордербук для одного символа
type OrderBookMap struct {
	Bids map[float64]float64 // цена -> объём
	Asks map[float64]float64 // цена -> объём
	// Добавим LastUpdateID для проверки согласованности
	LastUpdateID int64
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
}

func NewMarketDepthWatcher(
	ctx context.Context, // Принимаем контекст
	market string,
	subService *subscriptionservice.Service,
	keysRepo *binance_repository.PostgresKeysRepo,
	cfg *config.Config,
) *MarketDepthWatcher {
	return &MarketDepthWatcher{
		signalsBySymbol:     make(map[string][]*Signal),
		orderBooks:          make(map[string]*OrderBookMap),
		activeSymbols:       make(map[string]bool),
		subscriptionService: subService,
		keysRepo:            keysRepo,
		config:              cfg,
		market:              market,
		ctx:                 ctx,   // Сохраняем контекст
		started:             false, // Инициализируем как false
	}
}

func (w *MarketDepthWatcher) AddSignal(signal *Signal) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Проверяем, что сигнал для правильного рынка
	if signal.WatchMarket != w.market {
		log.Printf("MarketDepthWatcher: ERROR: Attempting to add signal for market %s to watcher for market %s", signal.WatchMarket, w.market)
		return
	}

	// НЕ устанавливаем OriginalQty и LastQty как MinQuantity
	// Они уже установлены в handler.go с реальным значением из HTTP-запроса
	// signal.OriginalQty = signal.MinQuantity // <-- УДАЛЕНО
	// signal.LastQty = signal.MinQuantity    // <-- УДАЛЕНО

	// Добавляем сигнал в список для его символа
	w.signalsBySymbol[signal.Symbol] = append(w.signalsBySymbol[signal.Symbol], signal)
	_, wasActive := w.activeSymbols[signal.Symbol]
	w.activeSymbols[signal.Symbol] = true // Помечаем символ как активный

	log.Printf("MarketDepthWatcher: Added signal %d for user %d, symbol %s, price %.8f, originalQty %.4f, closeMarket %s", signal.ID, signal.UserID, signal.Symbol, signal.TargetPrice, signal.OriginalQty, signal.CloseMarket)

	// Инициализируем локальный стакан для символа, если его ещё нет
	ob, ok := w.orderBooks[signal.Symbol]
	if !ok {
		ob = &OrderBookMap{
			Bids: make(map[float64]float64),
			Asks: make(map[float64]float64),
		}
		w.orderBooks[signal.Symbol] = ob
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
			// Если OriginalSide неизвестен, добавляем в обе стороны, как раньше.
			// Это менее точно, но покрывает неожиданные случаи.
			ob.Bids[signal.TargetPrice] = signal.OriginalQty
			ob.Asks[signal.TargetPrice] = signal.OriginalQty
			log.Printf("MarketDepthWatcher: Initialized local orderbook for symbol %s at price %.8f with qty %.4f in BOTH Bids and Asks (unknown side)", signal.Symbol, signal.TargetPrice, signal.OriginalQty)
		}
	} else {
		log.Printf("MarketDepthWatcher: Local orderbook for symbol %s at price %.8f already initialized or OriginalQty is 0, bids: %.4f, asks: %.4f", signal.Symbol, signal.TargetPrice, ob.Bids[signal.TargetPrice], ob.Asks[signal.TargetPrice])
	}

	// Если символ был неактивен, возможно, нужно инициировать подключение
	if !wasActive && !w.started {
		log.Printf("MarketDepthWatcher: First signal for symbol %s, attempting to start connection.", signal.Symbol)
		// Вызываем Start *внутри той же блокировки*, чтобы избежать гонки
		// или запускаем его в отдельной горутине с проверкой started
		if !w.started {
			w.started = true
			go func() {
				if err := w.startConnection(); err != nil {
					log.Printf("MarketDepthWatcher: Failed to start connection after adding first symbol: %v", err)
					// Возможно, нужно сбросить started = false и обработать ошибку
				}
			}()
		}
	}
}

func (w *MarketDepthWatcher) RemoveSignal(id int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Ищем и удаляем сигнал по ID
	for symbol, signals := range w.signalsBySymbol {
		for i, signal := range signals {
			if signal.ID == id {
				// Удаляем сигнал из слайса
				w.signalsBySymbol[symbol] = append(signals[:i], signals[i+1:]...)
				log.Printf("MarketDepthWatcher: Removed signal %d", id)
				// Если больше сигналов для этого символа нет, удаляем его из activeSymbols
				if len(w.signalsBySymbol[symbol]) == 0 {
					delete(w.activeSymbols, symbol)
				}
				return
			}
		}
	}
	log.Printf("MarketDepthWatcher: Signal %d not found for removal", id)
}

// startConnection вызывается только один раз при добавлении первого символа
func (w *MarketDepthWatcher) startConnection() error {
	client := NewWebSocketClient()
	client.OnData = func(data *UnifiedDepthStreamData) {
		// Убираем логирование каждого пришедшего апдейта
		// if len(data.Data.Bids) > 0 && len(data.Data.Asks) > 0 {
		// 	bidPrice, _ := strconv.ParseFloat(data.Data.Bids[0][0], 64)
		// 	bidQty, _ := strconv.ParseFloat(data.Data.Bids[0][1], 64)
		// 	askPrice, _ := strconv.ParseFloat(data.Data.Asks[0][0], 64)
		// 	askQty, _ := strconv.ParseFloat(data.Data.Asks[0][1], 64)
		// 	log.Printf("MarketDepthWatcher: Received data for %s (%s), first bid: %.8f (%.4f), first ask: %.8f (%.4f)", data.Data.Symbol, w.market, bidPrice, bidQty, askPrice, askQty)
		// }
		w.processDepthUpdate(data)
	}

	// Подписываемся на все активные символы
	symbols := make([]string, 0, len(w.activeSymbols))
	for symbol := range w.activeSymbols {
		symbols = append(symbols, symbol)
	}

	var err error
	switch w.market {
	case "spot":
		if len(symbols) == 0 {
			log.Printf("MarketDepthWatcher: No symbols to watch for spot market, skipping connection.")
			return nil
		}
		err = client.ConnectForSpotCombined(w.ctx, symbols)
	case "futures":
		// Для futures WsCombinedDepthServe принимает map[string]string
		symbolLevels := make(map[string]string)
		for symbol := range w.activeSymbols {
			symbolLevels[symbol] = "" // "" означает дифф-поток без фиксированного уровня
		}
		if len(symbolLevels) == 0 {
			log.Printf("MarketDepthWatcher: No symbols to watch for futures market, skipping connection.")
			return nil
		}
		err = client.ConnectForFuturesCombined(w.ctx, symbolLevels)
	default:
		return fmt.Errorf("unsupported market for MarketDepthWatcher: %s", w.market)
	}
	if err != nil {
		log.Printf("MarketDepthWatcher: Failed to connect to combined WebSocket for %s: %v", w.market, err)
		// Сбросим флаг started, если не удалось подключиться
		w.mu.Lock()
		w.started = false
		w.mu.Unlock()
		return err
	}
	w.client = client
	log.Printf("MarketDepthWatcher: Successfully connected to combined WebSocket for market: %s", w.market)
	return nil
}

func (w *MarketDepthWatcher) processDepthUpdate(data *UnifiedDepthStreamData) {
	symbol := data.Data.Symbol
	w.mu.Lock() // Блокируем на время обновления стакана и обработки сигналов
	defer w.mu.Unlock()

	// --- ИЗМЕРЕНИЕ ЗАДЕРЖКИ СЕТЕВОГО СОБЫТИЯ (опционально) ---
	// binanceEventTime := time.UnixMilli(data.Data.EventTime)
	// receivedTime := time.Now()
	// networkLatency := receivedTime.Sub(binanceEventTime)
	// log.Printf("MarketDepthWatcher: Network+Processing latency for %s: %v (EventTime: %v, Received: %v)",
	// 	symbol, networkLatency, binanceEventTime, receivedTime)
	// --- КОНЕЦ ИЗМЕРЕНИЯ ---

	// Получаем или создаём локальный стакан для символа
	ob, ok := w.orderBooks[symbol]
	if !ok {
		ob = &OrderBookMap{
			Bids: make(map[float64]float64),
			Asks: make(map[float64]float64),
		}
		w.orderBooks[symbol] = ob
	}

	// Применяем обновления к локальному стакану
	for _, bidUpdate := range data.Data.Bids {
		price, _ := strconv.ParseFloat(bidUpdate[0], 64)
		qty, _ := strconv.ParseFloat(bidUpdate[1], 64)
		if qty == 0 {
			delete(ob.Bids, price) // Удаляем уровень, если объём 0
		} else {
			ob.Bids[price] = qty // Обновляем или добавляем уровень
		}
	}
	for _, askUpdate := range data.Data.Asks {
		price, _ := strconv.ParseFloat(askUpdate[0], 64)
		qty, _ := strconv.ParseFloat(askUpdate[1], 64)
		if qty == 0 {
			delete(ob.Asks, price) // Удаляем уровень, если объём 0
		} else {
			ob.Asks[price] = qty // Обновляем или добавляем уровень
		}
	}

	// Обновляем LastUpdateID (опционально, для проверки согласованности)
	// ob.LastUpdateID = data.Data.LastUpdateID // UnifiedDepthEvent должен содержать LastUpdateID, если нужно

	// Обрабатываем сигналы
	signalsForSymbol, ok := w.signalsBySymbol[symbol]
	if !ok {
		return // Нет сигналов для этого символа
	}
	// Преобразуем EventTime из миллисекунд в time.Time
	binanceEventTime := time.UnixMilli(data.Data.EventTime)

	for _, signal := range signalsForSymbol {
		if signal.AutoCloseExecuted {
			continue // Пропускаем сигнал, если автозакрытие уже выполнено
		}
		// log.Printf("MarketDepthWatcher: Checking signal %d for symbol %s at price %.8f, originalQty %.4f", signal.ID, signal.Symbol, signal.TargetPrice, signal.OriginalQty) // <-- УБРАНО
		found, currentQty := w.findOrderAtPrice(ob, signal.TargetPrice)
		// log.Printf("MarketDepthWatcher: Signal %d: findOrderAtPrice returned found=%v, currentQty=%.4f", signal.ID, found, currentQty) // <-- УБРАНО

		// Проверка на отмену (disappeared)
		if signal.TriggerOnCancel {
			if !found {
				// Если заявка исчезла (не найдена), и она была изначально (OriginalQty > 0)
				// Или, если она исчезла, когда была (LastQty > 0)
				// Лучше проверить OriginalQty, т.к. LastQty обновляется.
				// Если OriginalQty > 0, и сейчас !found, это отмена.
				if signal.OriginalQty > 0 {
					triggerTime := time.Now() // ЗАПИСЬ ВРЕМЕНИ СРАБАТЫВАНИЯ ТРИГГЕРА
					signal.TriggerTime = triggerTime
					signal.BinanceEventTime = binanceEventTime // <-- ЗАПИСЬ ВРЕМЕНИ СОБЫТИЯ НА БИРЖЕ
					log.Printf("MarketDepthWatcher: Signal %d: Order at %.8f disappeared (was %.4f), triggering cancel at %v (Binance EventTime: %v)", signal.ID, signal.TargetPrice, signal.LastQty, triggerTime, binanceEventTime)
					order := &entity.Order{
						Price:    signal.TargetPrice,
						Quantity: signal.LastQty, // Используем LastQty как "последний известный объём перед исчезновением"
						Side:     "UNKNOWN",      // Сторона неизвестна
					}
					if w.onTrigger != nil {
						w.onTrigger(signal, order)
					}
					if signal.AutoClose {
						log.Printf("MarketDepthWatcher: Signal %d: Calling handleAutoClose for cancel", signal.ID)
						w.handleAutoClose(signal, order)
					}
					continue // Переходим к следующему сигналу, т.к. этот сработал
				}
			}
		}

		// Проверка на поедание (eaten)
		if signal.TriggerOnEat && found {
			// Только если заявка найдена
			eaten := signal.OriginalQty - currentQty
			eatenPercentage := 0.0
			if signal.OriginalQty > 0 {
				eatenPercentage = eaten / signal.OriginalQty
			}
			if eatenPercentage >= signal.EatPercentage {
				triggerTime := time.Now() // ЗАПИСЬ ВРЕМЕНИ СРАБАТЫВАНИЯ ТРИГГЕРА
				signal.TriggerTime = triggerTime
				signal.BinanceEventTime = binanceEventTime // <-- ЗАПИСЬ ВРЕМЕНИ СОБЫТИЯ НА БИРЖЕ
				log.Printf("MarketDepthWatcher: Signal %d: Order at %.8f eaten by %.2f%% (%.4f -> %.4f), triggering eat at %v (Binance EventTime: %v)", signal.ID, signal.TargetPrice, eatenPercentage*100, signal.OriginalQty, currentQty, triggerTime, binanceEventTime)
				order := &entity.Order{
					Price:    signal.TargetPrice,
					Quantity: currentQty,
					Side:     "UNKNOWN", // Сторона неизвестна
				}
				if w.onTrigger != nil {
					w.onTrigger(signal, order)
				}
				if signal.AutoClose {
					log.Printf("MarketDepthWatcher: Signal %d: Calling handleAutoClose for eat", signal.ID)
					w.handleAutoClose(signal, order)
				}
			}
		}
		// Обновляем LastQty, если заявка была найдена
		if found {
			signal.LastQty = currentQty
			// log.Printf("MarketDepthWatcher: Signal %d: Updated LastQty to %.4f", signal.ID, currentQty) // <-- УБРАНО
		} else {
			// Если не найдена, но была, LastQty можно не обновлять, или обновить на 0?
			// В текущей логике, если !found, LastQty не обновляется.
			// Это может быть проблемой, если заявка исчезает.
			// Но срабатывание на cancel обрабатывает это.
			// Возможно, стоит обновить на 0, если !found и была раньше.
			// signal.LastQty = 0 // Это может сбить с толку, если !found, но OriginalQty > 0.
			// Оставим как есть, обновляем только если found.
		}
	}
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
	// Блокировка не нужна, так как вызывается изнутри processDepthUpdate под блокировкой, или из другого места
	log.Printf("MarketDepthWatcher: handleAutoClose called for signal %d, user %d", signal.ID, signal.UserID)
	subscribed, err := w.subscriptionService.IsUserSubscribed(context.Background(), signal.UserID)
	if err != nil {
		log.Printf("MarketDepthWatcher: ERROR: IsUserSubscribed failed for user %d: %v", signal.UserID, err)
		return
	}
	if !subscribed {
		log.Printf("MarketDepthWatcher: INFO: User %d is not subscribed, skipping auto-close", signal.UserID)
		return
	}
	log.Printf("MarketDepthWatcher: INFO: User %d is subscribed, proceeding with auto-close", signal.UserID)

	keys, err := w.keysRepo.GetKeys(signal.UserID)
	if err != nil {
		log.Printf("MarketDepthWatcher: ERROR: GetKeys failed for user %d: %v", signal.UserID, err)
		return
	}
	secret := w.config.EncryptionSecret
	apiKey, err := DecryptAES(keys.APIKey, secret)
	if err != nil {
		log.Printf("MarketDepthWatcher: ERROR: DecryptAES failed for user %d: %v", signal.UserID, err)
		return
	}
	secretKey, err := DecryptAES(keys.SecretKey, secret)
	if err != nil {
		log.Printf("MarketDepthWatcher: ERROR: DecryptAES failed for user %d: %v", signal.UserID, err)
		return
	}

	log.Printf("MarketDepthWatcher: INFO: Keys decrypted successfully for user %d, calling CloseFullPosition on %s", signal.UserID, signal.CloseMarket)

	var manager *OrderManager
	switch signal.CloseMarket {
	case "futures":
		manager = NewOrderManager(apiKey, secretKey)
	case "spot":
		manager = NewOrderManager(apiKey, secretKey) // Используем разные базовые URL внутри OrderManager
	default:
		log.Printf("MarketDepthWatcher: ERROR: Unsupported close market: %s", signal.CloseMarket)
		return
	}

	// --- ОПРЕДЕЛЕНИЕ СТОРОНЫ ЗАКРЫТИЯ ---
	// OriginalSide - это сторона заявки, которую мы отслеживали (ASK или BUY).
	// Если мы отслеживали ASK (SELL), то, вероятно, кто-то открыл SHORT. Закрываем SHORT покупкой (BUY).
	// Если мы отслеживали BID (BUY), то, вероятно, кто-то открыл LONG. Закрываем LONG продажей (SELL).
	closeSide := "BUY" // Значение по умолчанию
	switch signal.OriginalSide {
	case "SELL": // Мы отслеживали A (ASK)
		closeSide = "BUY" // Чтобы закрыть short, покупаем
	case "BUY": // Мы отслеживали B (BID)
		closeSide = "SELL" // Чтобы закрыть long, продаем
	default:
		log.Printf("MarketDepthWatcher: WARNING: Unknown OriginalSide '%s' for signal %d, defaulting to BUY", signal.OriginalSide, signal.ID)
		// Можно оставить "BUY" или вернуть ошибку
	}
	// --- КОНЕЦ ОПРЕДЕЛЕНИЯ СТОРОНЫ ---

	log.Printf("MarketDepthWatcher: INFO: Attempting to close FULL position for signal %d by placing %s order", signal.ID, closeSide)

	// --- ИЗМЕРЕНИЕ LATENCY (от Binance EventTime до конца handleAutoClose) ---
	startTime := time.Now()
	// --- КОНЕЦ ИЗМЕРЕНИЯ ---

	// --- ВЫЗОВ НОВОГО МЕТОДА ---
	err = manager.CloseFullPosition(signal.Symbol, closeSide, signal.CloseMarket)
	if err != nil {
		log.Printf("MarketDepthWatcher: ERROR: CloseFullPosition failed for user %d: %v", signal.UserID, err)
		return
	}
	// --- КОНЕЦ ВЫЗОВА ---
	signal.AutoCloseExecuted = true

	// --- ЛОГИРОВАНИЕ LATENCY ---
	latencyFromBinanceEvent := time.Since(signal.BinanceEventTime) // от момента события на бирже до конца handleAutoClose
	latencyFromTrigger := time.Since(signal.TriggerTime)           // от момента срабатывания триггера до конца handleAutoClose
	latencyHandlerOnly := time.Since(startTime)                    // от начала handleAutoClose до конца
	log.Printf("MarketDepthWatcher: Auto-close latency for signal %d:", signal.ID)
	log.Printf("  - From Binance EventTime to end of handleAutoClose: %v", latencyFromBinanceEvent)
	log.Printf("  - From TriggerTime to end of handleAutoClose: %v", latencyFromTrigger)
	log.Printf("  - Time spent in handleAutoClose: %v", latencyHandlerOnly)
	// --- КОНЕЦ ЛОГИРОВАНИЯ ---

	log.Printf("MarketDepthWatcher: SUCCESS: FULL Position closed for user %d on %s", signal.UserID, signal.CloseMarket)
}

func (w *MarketDepthWatcher) SetOnTrigger(fn func(signal *Signal, order *entity.Order)) {
	w.onTrigger = fn
}

// GetOrderBook возвращает копию локального ордербука для символа
func (w *MarketDepthWatcher) GetOrderBook(symbol string) *OrderBookMap {
	w.mu.RLock()
	defer w.mu.RUnlock()
	ob, ok := w.orderBooks[symbol]
	if !ok {
		return nil
	}
	// Создаём копию для потенциальной передачи в handler
	copiedOB := &OrderBookMap{
		Bids: make(map[float64]float64),
		Asks: make(map[float64]float64),
	}
	for price, qty := range ob.Bids {
		copiedOB.Bids[price] = qty
	}
	for price, qty := range ob.Asks {
		copiedOB.Asks[price] = qty
	}
	return copiedOB
}
