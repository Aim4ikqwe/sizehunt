// internal/binance/transport/http/handler.go
package http

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"sizehunt/internal/api/dto"
	"sizehunt/internal/binance/repository"
	"sizehunt/internal/binance/service"
	"sizehunt/internal/config"
	subscriptionservice "sizehunt/internal/subscription/service"
	"sizehunt/pkg/middleware"
	"strconv"
	"time"
)

type Handler struct {
	BinanceService      *service.Watcher
	KeysRepo            *repository.PostgresKeysRepo
	Config              *config.Config
	WebSocketManager    *service.WebSocketManager // Теперь это WebSocketManager
	SubscriptionService *subscriptionservice.Service
}

func NewBinanceHandler(
	watcher *service.Watcher,
	keysRepo *repository.PostgresKeysRepo,
	cfg *config.Config,
	wsManager *service.WebSocketManager,
	subService *subscriptionservice.Service,
) *Handler {
	return &Handler{
		BinanceService:      watcher,
		KeysRepo:            keysRepo,
		Config:              cfg,
		WebSocketManager:    wsManager,
		SubscriptionService: subService,
	}
}

func (h *Handler) GetOrderBook(w http.ResponseWriter, r *http.Request) {
	symbol := r.URL.Query().Get("symbol")
	limitStr := r.URL.Query().Get("limit")
	market := r.URL.Query().Get("market")
	if symbol == "" {
		http.Error(w, "symbol is required", http.StatusBadRequest)
		return
	}
	limit := 100
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}

	userID := r.Context().Value(middleware.UserIDKey).(int64)
	keys, err := h.KeysRepo.GetKeys(userID)
	if err != nil {
		log.Printf("API keys not found for user %d, attempting public request", userID)
		client := service.NewBinanceHTTPClient("", "")
		ob, err := client.GetOrderBook(symbol, limit, market)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ob)
		return
	}

	secret := h.Config.EncryptionSecret
	apiKey, err := service.DecryptAES(keys.APIKey, secret)
	if err != nil {
		http.Error(w, "failed to decrypt API key", http.StatusInternalServerError)
		return
	}
	secretKey, err := service.DecryptAES(keys.SecretKey, secret)
	if err != nil {
		http.Error(w, "failed to decrypt Secret key", http.StatusInternalServerError)
		return
	}

	client := service.NewBinanceHTTPClient(apiKey, secretKey)
	ob, err := client.GetOrderBook(symbol, limit, market)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ob)
}

func (h *Handler) SaveKeys(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(middleware.UserIDKey).(int64)
	var req dto.SaveKeysRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := dto.Validate.Struct(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	secret := h.Config.EncryptionSecret
	encryptedAPIKey, err := service.EncryptAES(req.APIKey, secret)
	if err != nil {
		http.Error(w, "failed to encrypt API key", http.StatusInternalServerError)
		return
	}
	encryptedSecretKey, err := service.EncryptAES(req.SecretKey, secret)
	if err != nil {
		http.Error(w, "failed to encrypt Secret key", http.StatusInternalServerError)
		return
	}

	if err := h.KeysRepo.SaveKeys(userID, encryptedAPIKey, encryptedSecretKey); err != nil {
		http.Error(w, "failed to save keys", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"message":"API keys saved successfully"}`))
}

func (h *Handler) DeleteKeys(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(middleware.UserIDKey).(int64)
	if err := h.KeysRepo.DeleteByUserID(r.Context(), userID); err != nil {
		http.Error(w, "failed to delete keys", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"message":"API keys deleted successfully"}`))
}

func (h *Handler) CreateSignal(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(middleware.UserIDKey).(int64)
	var req dto.CreateSignalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := dto.Validate.Struct(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	log.Printf("Handler: CreateSignal called for user %d, symbol %s, price %.8f, watchMarket %s, closeMarket %s", userID, req.Symbol, req.TargetPrice, req.Market, req.CloseMarket)

	if req.Market == "" {
		req.Market = "futures"
	}
	if req.CloseMarket == "" {
		req.CloseMarket = req.Market // Закрытие на том же рынке, где и мониторинг
	}

	// Получаем watcher для нужного рынка
	watcher, err := h.WebSocketManager.GetOrCreateWatcher(req.Symbol, req.Market)
	if err != nil {
		log.Printf("Handler: Failed to get or create watcher for %s on %s: %v", req.Symbol, req.Market, err)
		http.Error(w, "failed to create watcher", http.StatusInternalServerError)
		return
	}

	signal := &service.Signal{
		ID:              generateID(),
		UserID:          userID,
		Symbol:          req.Symbol,
		TargetPrice:     req.TargetPrice,
		MinQuantity:     req.MinQuantity,
		TriggerOnCancel: req.TriggerOnCancel,
		TriggerOnEat:    req.TriggerOnEat,
		EatPercentage:   req.EatPercentage,
		AutoClose:       req.AutoClose,
		CloseMarket:     req.CloseMarket, // Установка рынка закрытия
		WatchMarket:     req.Market,      // Установка рынка мониторинга
	}

	log.Printf("Handler: Adding signal %d to watcher for user %d, symbol %s", signal.ID, signal.UserID, signal.Symbol)
	watcher.AddSignal(signal) // Добавляем сигнал в watcher
	log.Printf("Handler: Signal %d created successfully for user %d", signal.ID, userID)

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"message":"signal created"}`))
}

// GetOrderAtPrice теперь использует локальный ордербук из WebSocket
func (h *Handler) GetOrderAtPrice(w http.ResponseWriter, r *http.Request) {
	symbol := r.URL.Query().Get("symbol")
	priceStr := r.URL.Query().Get("price")
	market := r.URL.Query().Get("market")
	if symbol == "" || priceStr == "" {
		http.Error(w, "symbol and price are required", http.StatusBadRequest)
		return
	}
	price, err := strconv.ParseFloat(priceStr, 64)
	if err != nil {
		http.Error(w, "invalid price", http.StatusBadRequest)
		return
	}
	if market == "" {
		market = "futures"
	}

	userID := r.Context().Value(middleware.UserIDKey).(int64)
	subscribed, err := h.SubscriptionService.IsUserSubscribed(r.Context(), userID)
	if err != nil {
		http.Error(w, "failed to check subscription", http.StatusInternalServerError)
		return
	}
	if !subscribed {
		http.Error(w, "subscription required", http.StatusForbidden)
		return
	}

	// Получаем watcher для нужного рынка
	watcher, err := h.WebSocketManager.GetOrCreateWatcher(symbol, market)
	if err != nil {
		log.Printf("Handler: Failed to get watcher for %s on %s in GetOrderAtPrice: %v", symbol, market, err)
		http.Error(w, "failed to get watcher", http.StatusInternalServerError)
		return
	}

	// Получаем локальный ордербук из watcher'а
	localOB := watcher.GetOrderBook(symbol)
	if localOB == nil {
		// Если локального ордербука нет (например, соединение ещё не установлено или символ не отслеживается)
		// Возвращаем ошибку или пустой результат
		// ВАЖНО: Это может быть не идеально, если символ не отслеживается этим конкретным watcher'ом,
		// но отслеживается другим (например, фьючерсный симол в спотовом watcher'е - не должно быть)
		// или если просто ещё не пришло первое обновление.
		// Для простоты возвращаем "not found".
		log.Printf("Handler: Local order book for %s not available in GetOrderAtPrice", symbol)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"price":      price,
			"is_present": false,
			"message":    "local order book not available or symbol not being watched",
		})
		return
	}

	// Ищем заявку в локальном ордербуке
	found := false
	var foundQty float64
	var foundSide string

	tolerance := 0.0001 // погрешность для сравнения цен

	// Ищем в bids
	for bidPrice, bidQty := range localOB.Bids {
		if math.Abs(bidPrice-price) < tolerance {
			foundQty = bidQty
			foundSide = "BUY"
			found = true
			break
		}
	}

	// Ищем в asks
	if !found {
		for askPrice, askQty := range localOB.Asks {
			if math.Abs(askPrice-price) < tolerance {
				foundQty = askQty
				foundSide = "SELL"
				found = true
				break
			}
		}
	}

	if !found {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"price":      price,
			"is_present": false,
			"message":    "no order found at this price in local order book",
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"price":      price, // или foundPrice, если хотите вернуть найденную цену с учётом tolerance
		"is_present": true,
		"quantity":   foundQty,
		"side":       foundSide,
	})
	log.Printf("Get order at price %s with quantity %f and side %s from local WebSocket book", price, foundQty, foundSide)
	fmt.Printf("Get order at price %s with quantity %f and side %s from local WebSocket book\n", price, foundQty, foundSide)
}

func generateID() int64 {
	return time.Now().UnixNano() % 1000000
}
