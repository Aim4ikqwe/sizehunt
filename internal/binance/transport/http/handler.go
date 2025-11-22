// internal/binance/transport/http/handler.go
package http

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sizehunt/internal/api/dto"
	"sizehunt/internal/binance/entity"
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
	WebSocketManager    *service.WebSocketManager
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
	market := r.URL.Query().Get("market") // "spot" или "futures"

	limit := 100
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil {
			limit = l
		}
	}

	// Получаем userID из JWT
	userID := r.Context().Value(middleware.UserIDKey).(int64)
	// Получаем API-ключи пользователя
	keys, err := h.KeysRepo.GetKeys(userID)
	if err != nil {
		http.Error(w, "API keys not found", http.StatusUnauthorized)
		return
	}
	// Расшифровываем ключи
	secret := h.Config.EncryptionSecret
	apiKey, err := service.DecryptAES(keys.APIKey, secret)
	if err != nil {
		http.Error(w, "failed to decrypt API key", http.StatusInternalServerError)
		return
	}

	client := service.NewBinanceHTTPClient(apiKey, "") // Передаём apiKey, secretKey не нужен для публичных запросов
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

	// Шифруем ключи
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

	// Сохраняем в БД
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

	log.Printf("Handler: CreateSignal called for user %d, symbol %s, price %.8f", userID, req.Symbol, req.TargetPrice)

	// Проверяем подписку
	subscribed, err := h.SubscriptionService.IsUserSubscribed(r.Context(), userID)
	if err != nil {
		log.Printf("Handler: IsUserSubscribed failed for user %d: %v", userID, err)
		http.Error(w, "failed to check subscription", http.StatusInternalServerError)
		return
	}
	if !subscribed {
		http.Error(w, "subscription required", http.StatusForbidden)
		return
	}

	// Получаем API-ключи пользователя
	keys, err := h.KeysRepo.GetKeys(userID)
	if err != nil {
		log.Printf("Handler: GetKeys failed for user %d: %v", userID, err)
		http.Error(w, "API keys not found", http.StatusUnauthorized)
		return
	}

	// Расшифровываем ключи
	secret := h.Config.EncryptionSecret
	apiKey, err := service.DecryptAES(keys.APIKey, secret)
	if err != nil {
		log.Printf("Handler: DecryptAES failed for API key for user %d: %v", userID, err)
		http.Error(w, "failed to decrypt API key", http.StatusInternalServerError)
		return
	}
	secretKey, err := service.DecryptAES(keys.SecretKey, secret)
	if err != nil {
		log.Printf("Handler: DecryptAES failed for Secret key for user %d: %v", userID, err)
		http.Error(w, "failed to decrypt Secret key", http.StatusInternalServerError)
		return
	}

	// Создаём HTTP-клиент для получения начального состояния
	client := service.NewBinanceHTTPClient(apiKey, secretKey)
	const initialBookLimit = 1000
	ob, err := client.GetOrderBook(req.Symbol, initialBookLimit, req.Market)
	if err != nil {
		log.Printf("Handler: GetOrderBook failed for symbol %s, market %s: %v", req.Symbol, req.Market, err)
		http.Error(w, fmt.Sprintf("failed to fetch initial order book: %v", err), http.StatusInternalServerError)
		return
	}

	var initialQty float64 = 0
	var initialSide string = "UNKNOWN"
	for _, bid := range ob.Bids {
		if bid.Price == req.TargetPrice {
			initialQty = bid.Quantity
			initialSide = bid.Side
			log.Printf("Handler: Found initial bid at %.8f with quantity %.4f", req.TargetPrice, initialQty)
			break
		}
	}
	if initialQty == 0 {
		for _, ask := range ob.Asks {
			if ask.Price == req.TargetPrice {
				initialQty = ask.Quantity
				initialSide = ask.Side
				log.Printf("Handler: Found initial ask at %.8f with quantity %.4f", req.TargetPrice, initialQty)
				break
			}
		}
	}

	if initialQty < req.MinQuantity {
		log.Printf("Handler: Initial quantity %.4f at price %.8f is less than min quantity %.4f", initialQty, req.TargetPrice, req.MinQuantity)
		http.Error(w, fmt.Sprintf("initial quantity %.4f at price %.8f is less than min quantity %.4f", initialQty, req.TargetPrice, req.MinQuantity), http.StatusBadRequest)
		return
	}

	// 🔥 КЛЮЧЕВОЕ ИЗМЕНЕНИЕ: используем мультиюзерный метод
	watcher, err := h.WebSocketManager.GetOrCreateWatcherForUser(userID, req.Symbol, req.Market, req.AutoClose)
	if err != nil {
		log.Printf("Handler: Failed to get watcher for user %d, symbol %s: %v", userID, req.Symbol, err)
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
		CloseMarket:     req.Market, // закрытие на том же рынке
		WatchMarket:     req.Market,
		OriginalQty:     initialQty,
		LastQty:         initialQty,
		OriginalSide:    initialSide,
	}

	log.Printf("Handler: Adding signal %d to watcher for user %d, symbol %s, initialQty %.4f", signal.ID, userID, req.Symbol, initialQty)
	watcher.AddSignal(signal)
	log.Printf("Handler: Signal %d created successfully for user %d", signal.ID, userID)

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"message":"signal created"}`))
}

func (h *Handler) GetOrderAtPrice(w http.ResponseWriter, r *http.Request) {
	symbol := r.URL.Query().Get("symbol")
	priceStr := r.URL.Query().Get("price")
	market := r.URL.Query().Get("market") // "spot" или "futures"

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
		market = "futures" // по умолчанию
	}

	// Получаем userID
	userID := r.Context().Value(middleware.UserIDKey).(int64)

	// Проверяем подписку
	subscribed, err := h.SubscriptionService.IsUserSubscribed(r.Context(), userID)
	if err != nil {
		http.Error(w, "failed to check subscription", http.StatusInternalServerError)
		return
	}
	if !subscribed {
		http.Error(w, "subscription required", http.StatusForbidden)
		return
	}

	// Получаем API-ключи
	keys, err := h.KeysRepo.GetKeys(userID)
	if err != nil {
		http.Error(w, "API keys not found", http.StatusUnauthorized)
		return
	}
	secret := h.Config.EncryptionSecret
	apiKey, err := service.DecryptAES(keys.APIKey, secret)
	if err != nil {
		http.Error(w, "failed to decrypt API key", http.StatusInternalServerError)
		return
	}

	// Получаем ордербук
	client := service.NewBinanceHTTPClient(apiKey, "")   // apiKey, secretKey не нужен для публичных запросов
	ob, err := client.GetOrderBook(symbol, 1000, market) // 1000 — максимум
	if err != nil {
		http.Error(w, "failed to get order book", http.StatusInternalServerError)
		return
	}

	// Ищем заявку на нужной цене
	var foundOrder *entity.Order
	for _, bid := range ob.Bids {
		if bid.Price == price {
			foundOrder = &bid
			break
		}
	}
	if foundOrder == nil {
		for _, ask := range ob.Asks {
			if ask.Price == price {
				foundOrder = &ask
				break
			}
		}
	}

	if foundOrder == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"price":      price,
			"is_present": false,
			"message":    "no order found at this price",
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"price":      foundOrder.Price,
		"is_present": true,
		"quantity":   foundOrder.Quantity,
		"side":       foundOrder.Side,
	})

	log.Printf("Get order at price %s with price %s", price, foundOrder.Price)
	fmt.Printf("Geto order at price %s with price %s\n", price, foundOrder.Price)
}

// generateID — генератор ID (можно использовать UUID или просто счётчик)
func generateID() int64 {
	// Реализуй как хочешь: UUID, auto-increment, etc.
	// Пока просто временный вариант
	return time.Now().UnixNano() % 1000000
}
