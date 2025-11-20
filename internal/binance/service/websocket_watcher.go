// internal/binance/service/websocket_watcher.go
package service

import (
	"context"
	"fmt"
	"log"
	"sizehunt/internal/binance/entity"
	binance_repository "sizehunt/internal/binance/repository"
	"sizehunt/internal/config"
	subscriptionservice "sizehunt/internal/subscription/service"
	"strconv"
	"sync"
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

	// Запоминаем изначальный объём
	signal.OriginalQty = signal.MinQuantity
	signal.LastQty = signal.MinQuantity

	// Добавляем сигнал в список для его символа
	w.signalsBySymbol[signal.Symbol] = append(w.signalsBySymbol[signal.Symbol], signal)
	_, wasActive := w.activeSymbols[signal.Symbol]
	w.activeSymbols[signal.Symbol] = true // Помечаем символ как активный

	log.Printf("MarketDepthWatcher: Added signal %d for user %d, symbol %s, price %.8f, closeMarket %s", signal.ID, signal.UserID, signal.Symbol, signal.TargetPrice, signal.CloseMarket)

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
		if len(data.Data.Bids) > 0 && len(data.Data.Asks) > 0 {
			bidPrice, _ := strconv.ParseFloat(data.Data.Bids[0][0], 64)
			bidQty, _ := strconv.ParseFloat(data.Data.Bids[0][1], 64)
			askPrice, _ := strconv.ParseFloat(data.Data.Asks[0][0], 64)
			askQty, _ := strconv.ParseFloat(data.Data.Asks[0][1], 64)
			log.Printf("MarketDepthWatcher: Received data for %s (%s), first bid: %.8f (%.4f), first ask: %.8f (%.4f)", data.Data.Symbol, w.market, bidPrice, bidQty, askPrice, askQty)
		}
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

	for _, signal := range signalsForSymbol {
		log.Printf("MarketDepthWatcher: Checking signal %d for symbol %s at price %.8f", signal.ID, signal.Symbol, signal.TargetPrice)

		found, currentQty := w.findOrderAtPrice(ob, signal.TargetPrice)
		if !found {
			if signal.TriggerOnCancel {
				log.Printf("MarketDepthWatcher: Signal %d: Order at %.8f disappeared (was %.4f), triggering cancel", signal.ID, signal.TargetPrice, signal.LastQty)
				order := &entity.Order{
					Price:    signal.TargetPrice,
					Quantity: signal.LastQty,
					Side:     "UNKNOWN",
				}
				if w.onTrigger != nil {
					w.onTrigger(signal, order)
				}
				if signal.AutoClose {
					log.Printf("MarketDepthWatcher: Signal %d: Calling handleAutoClose for cancel", signal.ID)
					w.handleAutoClose(signal, order)
				}
			}
			continue
		}

		if signal.TriggerOnEat {
			eaten := signal.OriginalQty - currentQty
			eatenPercentage := eaten / signal.OriginalQty
			if eatenPercentage >= signal.EatPercentage {
				log.Printf("MarketDepthWatcher: Signal %d: Order at %.8f eaten by %.2f%% (%.4f -> %.4f), triggering eat", signal.ID, signal.TargetPrice, eatenPercentage*100, signal.OriginalQty, currentQty)
				order := &entity.Order{
					Price:    signal.TargetPrice,
					Quantity: currentQty,
					Side:     "UNKNOWN",
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
		signal.LastQty = currentQty
		log.Printf("MarketDepthWatcher: Signal %d: Updated LastQty to %.4f", signal.ID, currentQty)
	}
}

// findOrderAtPrice ищет заявку в локальном OrderBookMap по точному совпадению цены
func (w *MarketDepthWatcher) findOrderAtPrice(ob *OrderBookMap, targetPrice float64) (found bool, qty float64) {
	log.Printf("MarketDepthWatcher: findOrderAtPrice: Looking for exact price %.8f", targetPrice)

	// Ищем в bids
	if qty, ok := ob.Bids[targetPrice]; ok {
		log.Printf("MarketDepthWatcher: findOrderAtPrice: Found exact bid at %.8f with quantity %.4f", targetPrice, qty)
		return true, qty
	}

	// Ищем в asks
	if qty, ok := ob.Asks[targetPrice]; ok {
		log.Printf("MarketDepthWatcher: findOrderAtPrice: Found exact ask at %.8f with quantity %.4f", targetPrice, qty)
		return true, qty
	}

	log.Printf("MarketDepthWatcher: findOrderAtPrice: No exact order found at price %.8f", targetPrice)
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

	log.Printf("MarketDepthWatcher: INFO: Keys decrypted successfully for user %d, calling ClosePosition on %s", signal.UserID, signal.CloseMarket)

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

	// Определяем сторону закрытия. Если мы отслеживали ордер на продажу (ASK), то закрываем покупкой (BUY) и наоборот.
	// Если не можем определить сторону, предполагаем, что нужно закрыть ордер на продажу (BUY).
	closeSide := "BUY" // Предполагаем, что отслеживаем BID (покупка), значит закрываем SELL ордер покупкой

	err = manager.ClosePosition(signal.Symbol, closeSide, fmt.Sprintf("%.6f", order.Quantity), signal.CloseMarket)
	if err != nil {
		log.Printf("MarketDepthWatcher: ERROR: ClosePosition failed for user %d: %v", signal.UserID, err)
		return
	}
	log.Printf("MarketDepthWatcher: SUCCESS: Position closed for user %d on %s", signal.UserID, signal.CloseMarket)
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
