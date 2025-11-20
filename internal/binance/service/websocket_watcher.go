package service

import (
	"context"
	"fmt"
	"log"
	"math"
	"strconv"
	"sync"

	"sizehunt/internal/binance/entity"
	binance_repository "sizehunt/internal/binance/repository"
	"sizehunt/internal/config"
	subscriptionservice "sizehunt/internal/subscription/service"
)

type WebSocketWatcher struct {
	client              *WebSocketClient
	signals             map[int64]*Signal // signalID -> signal
	mu                  sync.RWMutex
	onTrigger           func(signal *Signal, order *entity.Order)
	subscriptionService *subscriptionservice.Service
	keysRepo            *binance_repository.PostgresKeysRepo
	config              *config.Config
}

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
}

func NewWebSocketWatcher(
	subService *subscriptionservice.Service,
	keysRepo *binance_repository.PostgresKeysRepo,
	cfg *config.Config,
) *WebSocketWatcher {
	return &WebSocketWatcher{
		signals:             make(map[int64]*Signal),
		subscriptionService: subService,
		keysRepo:            keysRepo,
		config:              cfg,
	}
}

func (w *WebSocketWatcher) AddSignal(signal *Signal) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Запоминаем изначальный объём
	signal.OriginalQty = signal.MinQuantity
	signal.LastQty = signal.MinQuantity

	w.signals[signal.ID] = signal
	log.Printf("WebSocketWatcher: Added signal %d for user %d, symbol %s, price %.8f", signal.ID, signal.UserID, signal.Symbol, signal.TargetPrice)
}

func (w *WebSocketWatcher) RemoveSignal(id int64) {
	w.mu.Lock()
	defer w.mu.Unlock()

	delete(w.signals, id)
	log.Printf("WebSocketWatcher: Removed signal %d", id)
}

func (w *WebSocketWatcher) Start(ctx context.Context, symbol string, market string) error {
	var wsURL string
	switch market {
	case "futures":
		wsURL = fmt.Sprintf("wss://fstream.binance.com/ws/%s@depth", symbol)
	default:
		wsURL = fmt.Sprintf("wss://stream.binance.com/ws/%s@depth", symbol)
	}

	log.Printf("WebSocketWatcher: Connecting to WebSocket: %s", wsURL)

	client := NewWebSocketClient(wsURL)

	client.OnData = func(data *DepthStreamData) {
		// Исправленный лог
		bidPrice, _ := strconv.ParseFloat(data.Data.Bids[0][0], 64)
		bidQty, _ := strconv.ParseFloat(data.Data.Bids[0][1], 64)
		askPrice, _ := strconv.ParseFloat(data.Data.Asks[0][0], 64)
		askQty, _ := strconv.ParseFloat(data.Data.Asks[0][1], 64)

		log.Printf("WebSocketWatcher: Received data for %s, first bid: %.8f (%.4f), first ask: %.8f (%.4f)",
			data.Data.Symbol, bidPrice, bidQty, askPrice, askQty)
		w.processDepthUpdate(data)
	}

	if err := client.Connect(ctx); err != nil {
		log.Printf("WebSocketWatcher: Failed to connect to %s: %v", wsURL, err)
		return err
	}

	// Храним клиента, если нужно
	w.client = client

	// Запускаем в горутине, чтобы не блокировать
	go func() {
		<-ctx.Done()
		client.Close()
		log.Printf("WebSocketWatcher: Closed connection for %s", symbol)
	}()

	log.Printf("WebSocketWatcher: Successfully connected to WebSocket for %s", symbol)
	return nil
}

func (w *WebSocketWatcher) processDepthUpdate(data *DepthStreamData) {
	w.mu.RLock()
	defer w.mu.RUnlock()

	log.Printf("WebSocketWatcher: Processing depth update for %s, signals count: %d", data.Data.Symbol, len(w.signals))

	for _, signal := range w.signals {
		if signal.Symbol != data.Data.Symbol {
			continue
		}

		log.Printf("WebSocketWatcher: Checking signal %d for symbol %s at price %.8f", signal.ID, signal.Symbol, signal.TargetPrice)

		// Проверяем, есть ли заявка на нужной цене
		found, currentQty := w.findOrderAtPrice(data.Data, signal.TargetPrice)

		if !found {
			// Заявка исчезла
			if signal.TriggerOnCancel {
				log.Printf("WebSocketWatcher: Signal %d: Order at %.8f disappeared (was %.4f), triggering cancel", signal.ID, signal.TargetPrice, signal.LastQty)
				order := &entity.Order{
					Price:    signal.TargetPrice,
					Quantity: signal.LastQty,
					Side:     "UNKNOWN", // не знаем
				}
				if w.onTrigger != nil {
					w.onTrigger(signal, order)
				}
				if signal.AutoClose {
					log.Printf("WebSocketWatcher: Signal %d: Calling handleAutoClose for cancel", signal.ID)
					w.handleAutoClose(signal, order)
				}
			}
			continue
		}

		// Проверяем разъедание
		if signal.TriggerOnEat {
			eaten := signal.OriginalQty - currentQty
			eatenPercentage := eaten / signal.OriginalQty

			if eatenPercentage >= signal.EatPercentage {
				log.Printf("WebSocketWatcher: Signal %d: Order at %.8f eaten by %.2f%% (%.4f -> %.4f), triggering eat", signal.ID, signal.TargetPrice, eatenPercentage*100, signal.OriginalQty, currentQty)
				order := &entity.Order{
					Price:    signal.TargetPrice,
					Quantity: currentQty,
					Side:     "UNKNOWN",
				}
				if w.onTrigger != nil {
					w.onTrigger(signal, order)
				}
				if signal.AutoClose {
					log.Printf("WebSocketWatcher: Signal %d: Calling handleAutoClose for eat", signal.ID)
					w.handleAutoClose(signal, order)
				}
			}
		}

		// Обновляем последний объём
		signal.LastQty = currentQty
		log.Printf("WebSocketWatcher: Signal %d: Updated LastQty to %.4f", signal.ID, currentQty)
	}
}

func (w *WebSocketWatcher) handleAutoClose(signal *Signal, order *entity.Order) {
	log.Printf("WebSocketWatcher: handleAutoClose called for signal %d, user %d", signal.ID, signal.UserID)

	// Проверяем подписку
	subscribed, err := w.subscriptionService.IsUserSubscribed(context.Background(), signal.UserID)
	if err != nil {
		log.Printf("WebSocketWatcher: ERROR: IsUserSubscribed failed for user %d: %v", signal.UserID, err)
		return
	}
	if !subscribed {
		log.Printf("WebSocketWatcher: INFO: User %d is not subscribed, skipping auto-close", signal.UserID)
		return
	}

	log.Printf("WebSocketWatcher: INFO: User %d is subscribed, proceeding with auto-close", signal.UserID)

	// Получаем API-ключи
	keys, err := w.keysRepo.GetKeys(signal.UserID)
	if err != nil {
		log.Printf("WebSocketWatcher: ERROR: GetKeys failed for user %d: %v", signal.UserID, err)
		return
	}

	secret := w.config.EncryptionSecret
	apiKey, err := DecryptAES(keys.APIKey, secret)
	if err != nil {
		log.Printf("WebSocketWatcher: ERROR: DecryptAES failed for user %d: %v", signal.UserID, err)
		return
	}

	secretKey, err := DecryptAES(keys.SecretKey, secret)
	if err != nil {
		log.Printf("WebSocketWatcher: ERROR: DecryptAES failed for user %d: %v", signal.UserID, err)
		return
	}

	log.Printf("WebSocketWatcher: INFO: Keys decrypted successfully for user %d, calling ClosePosition", signal.UserID)

	manager := NewOrderManager(apiKey, secretKey, "https://fapi.binance.com")
	err = manager.ClosePosition(signal.Symbol, "BUY", fmt.Sprintf("%.6f", order.Quantity))
	if err != nil {
		log.Printf("WebSocketWatcher: ERROR: ClosePosition failed for user %d: %v", signal.UserID, err)
		return
	}

	log.Printf("WebSocketWatcher: SUCCESS: Position closed for user %d", signal.UserID)
}

func (w *WebSocketWatcher) findOrderAtPrice(update DepthUpdate, price float64) (found bool, qty float64) {
	log.Printf("WebSocketWatcher: findOrderAtPrice: Looking for price %.8f", price)

	tolerance := 0.0001 // погрешность для сравнения цен

	// Ищем в bids
	for _, bid := range update.Bids {
		bidPrice, _ := strconv.ParseFloat(bid[0], 64)
		if math.Abs(bidPrice-price) < tolerance {
			qty, _ = strconv.ParseFloat(bid[1], 64)
			log.Printf("WebSocketWatcher: findOrderAtPrice: Found bid at %.8f with quantity %.4f", bidPrice, qty)
			return true, qty
		}
	}

	// Ищем в asks
	for _, ask := range update.Asks {
		askPrice, _ := strconv.ParseFloat(ask[0], 64)
		if math.Abs(askPrice-price) < tolerance {
			qty, _ = strconv.ParseFloat(ask[1], 64)
			log.Printf("WebSocketWatcher: findOrderAtPrice: Found ask at %.8f with quantity %.4f", askPrice, qty)
			return true, qty
		}
	}

	log.Printf("WebSocketWatcher: findOrderAtPrice: No order found at price %.8f", price)
	return false, 0
}

func (w *WebSocketWatcher) SetOnTrigger(fn func(signal *Signal, order *entity.Order)) {
	w.onTrigger = fn
}
