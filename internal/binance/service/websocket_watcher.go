package service

import (
	"context"
	"fmt"
	"log"
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
	subscriptionService *subscriptionservice.Service         // subscription.Service
	keysRepo            *binance_repository.PostgresKeysRepo // repository.PostgresKeysRepo
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
}

func (w *WebSocketWatcher) RemoveSignal(id int64) {
	w.mu.Lock()
	defer w.mu.Unlock()

	delete(w.signals, id)
}

func (w *WebSocketWatcher) Start(ctx context.Context, symbol string, market string) error {
	var wsURL string
	switch market {
	case "futures":
		wsURL = fmt.Sprintf("wss://fstream.binance.com/ws/%s@depth", symbol)
	default:
		wsURL = fmt.Sprintf("wss://stream.binance.com/ws/%s@depth", symbol)
	}

	client := NewWebSocketClient(wsURL)

	client.OnData = func(data *DepthStreamData) {
		w.processDepthUpdate(data)
	}

	if err := client.Connect(ctx); err != nil {
		return err
	}

	// Запускаем в горутине, чтобы не блокировать
	go func() {
		<-ctx.Done()
		client.Close()
	}()

	return nil
}

func (w *WebSocketWatcher) processDepthUpdate(data *DepthStreamData) {
	w.mu.RLock()
	defer w.mu.RUnlock()

	for _, signal := range w.signals {
		if signal.Symbol != data.Data.Symbol {
			continue
		}

		// Проверяем, есть ли заявка на нужной цене
		found, currentQty := w.findOrderAtPrice(data.Data, signal.TargetPrice)

		if !found {
			// Заявка исчезла
			if signal.TriggerOnCancel {
				order := &entity.Order{
					Price:    signal.TargetPrice,
					Quantity: signal.LastQty,
					Side:     "UNKNOWN", // не знаем
				}
				if w.onTrigger != nil {
					w.onTrigger(signal, order)
				}
				if signal.AutoClose {
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
				order := &entity.Order{
					Price:    signal.TargetPrice,
					Quantity: currentQty,
					Side:     "UNKNOWN",
				}
				if w.onTrigger != nil {
					w.onTrigger(signal, order)
				}
				if signal.AutoClose {
					w.handleAutoClose(signal, order)
				}
			}
		}

		// Обновляем последний объём
		signal.LastQty = currentQty
	}
}

func (w *WebSocketWatcher) handleAutoClose(signal *Signal, order *entity.Order) {
	// Проверяем подписку
	subscribed, err := w.subscriptionService.IsUserSubscribed(context.Background(), signal.UserID)
	if err != nil {
		log.Printf("Error checking subscription for user %d: %v", signal.UserID, err)
		return
	}
	if !subscribed {
		log.Printf("User %d is not subscribed, skipping auto-close", signal.UserID)
		return
	}

	// Получаем API-ключи
	keys, err := w.keysRepo.GetKeys(signal.UserID)
	if err != nil {
		log.Printf("Error getting API keys for user %d: %v", signal.UserID, err)
		return
	}

	secret := w.config.EncryptionSecret
	apiKey, err := DecryptAES(keys.APIKey, secret)
	if err != nil {
		log.Printf("Error decrypting API key for user %d: %v", signal.UserID, err)
		return
	}

	secretKey, err := DecryptAES(keys.SecretKey, secret)
	if err != nil {
		log.Printf("Error decrypting Secret key for user %d: %v", signal.UserID, err)
		return
	}

	manager := NewOrderManager(apiKey, secretKey, "https://fapi.binance.com")
	err = manager.ClosePosition(signal.Symbol, "BUY", fmt.Sprintf("%.6f", order.Quantity))
	if err != nil {
		log.Printf("Error closing position for user %d: %v", signal.UserID, err)
		return
	}

	log.Printf("Position closed for user %d", signal.UserID)
}

func (w *WebSocketWatcher) findOrderAtPrice(update DepthUpdate, price float64) (found bool, qty float64) {
	// Ищем в bids
	for _, bid := range update.Bids {
		bidPrice, _ := strconv.ParseFloat(bid[0], 64)
		if bidPrice == price {
			qty, _ = strconv.ParseFloat(bid[1], 64)
			return true, qty
		}
	}

	// Ищем в asks
	for _, ask := range update.Asks {
		askPrice, _ := strconv.ParseFloat(ask[0], 64)
		if askPrice == price {
			qty, _ = strconv.ParseFloat(ask[1], 64)
			return true, qty
		}
	}

	return false, 0
}

func (w *WebSocketWatcher) SetOnTrigger(fn func(signal *Signal, order *entity.Order)) {
	w.onTrigger = fn
}
