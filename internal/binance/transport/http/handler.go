// internal/binance/transport/http/handler.go
package http

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"runtime/debug"
	"sizehunt/internal/api/dto"
	"sizehunt/internal/binance/entity"
	"sizehunt/internal/binance/repository"
	"sizehunt/internal/binance/service"
	"sizehunt/internal/config"
	subscriptionservice "sizehunt/internal/subscription/service"
	"sizehunt/pkg/middleware"
	"strconv"
	"time"

	"github.com/adshao/go-binance/v2/futures"
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
	handler := &Handler{
		BinanceService:      watcher,
		KeysRepo:            keysRepo,
		Config:              cfg,
		WebSocketManager:    wsManager,
		SubscriptionService: subService,
	}

	log.Println("BinanceHandler: Initialized successfully")
	return handler
}

func (h *Handler) GetOrderBook(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	defer func() {
		log.Printf("Handler: GetOrderBook completed (total time: %v)", time.Since(startTime))
	}()

	symbol := r.URL.Query().Get("symbol")
	limitStr := r.URL.Query().Get("limit")
	market := r.URL.Query().Get("market") // "spot" или "futures"
	userID := r.Context().Value(middleware.UserIDKey).(int64)

	log.Printf("Handler: GetOrderBook called by user %d, symbol: %s, limit: %s, market: %s",
		userID, symbol, limitStr, market)

	if symbol == "" {
		http.Error(w, "symbol parameter is required", http.StatusBadRequest)
		return
	}

	if market == "" {
		market = "futures"
		log.Printf("Handler: Default market set to %s", market)
	}

	limit := 100
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil {
			limit = l
			log.Printf("Handler: Limit set to %d", limit)
		} else {
			log.Printf("Handler: WARNING: Invalid limit parameter: %s, using default %d", limitStr, limit)
		}
	}

	// Получаем API-ключи пользователя
	keys, err := h.KeysRepo.GetKeys(userID)
	if err != nil {
		log.Printf("Handler: ERROR: GetKeys failed for user %d: %v", userID, err)
		http.Error(w, "API keys not found", http.StatusUnauthorized)
		return
	}

	// Расшифровываем ключи
	secret := h.Config.EncryptionSecret
	apiKey, err := service.DecryptAES(keys.APIKey, secret)
	if err != nil {
		log.Printf("Handler: ERROR: DecryptAES failed for API key for user %d: %v", userID, err)
		http.Error(w, "failed to decrypt API key", http.StatusInternalServerError)
		return
	}

	client := service.NewBinanceHTTPClient(apiKey, "") // Передаём apiKey, secretKey не нужен для публичных запросов
	ob, err := client.GetOrderBook(symbol, limit, market)
	if err != nil {
		log.Printf("Handler: ERROR: GetOrderBook failed for symbol %s, market %s: %v", symbol, market, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("Handler: Successfully retrieved orderbook for %s, market %s, limit %d", symbol, market, limit)
	log.Printf("Handler: Orderbook contains %d bids and %d asks", len(ob.Bids), len(ob.Asks))

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(ob); err != nil {
		log.Printf("Handler: ERROR: Failed to encode response: %v", err)
	}
}

func (h *Handler) SaveKeys(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	defer func() {
		log.Printf("Handler: SaveKeys completed (total time: %v)", time.Since(startTime))
	}()

	userID := r.Context().Value(middleware.UserIDKey).(int64)
	var req dto.SaveKeysRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Printf("Handler: ERROR: Invalid JSON in SaveKeys request: %v", err)
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if err := dto.Validate.Struct(req); err != nil {
		log.Printf("Handler: ERROR: Validation failed in SaveKeys: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	log.Printf("Handler: SaveKeys called for user %d", userID)

	// Шифруем ключи
	secret := h.Config.EncryptionSecret
	encryptedAPIKey, err := service.EncryptAES(req.APIKey, secret)
	if err != nil {
		log.Printf("Handler: ERROR: Failed to encrypt API key for user %d: %v", userID, err)
		http.Error(w, "failed to encrypt API key", http.StatusInternalServerError)
		return
	}

	encryptedSecretKey, err := service.EncryptAES(req.SecretKey, secret)
	if err != nil {
		log.Printf("Handler: ERROR: Failed to encrypt Secret key for user %d: %v", userID, err)
		http.Error(w, "failed to encrypt Secret key", http.StatusInternalServerError)
		return
	}

	// Сохраняем в БД
	if err := h.KeysRepo.SaveKeys(userID, encryptedAPIKey, encryptedSecretKey); err != nil {
		log.Printf("Handler: ERROR: Failed to save keys for user %d: %v", userID, err)
		http.Error(w, "failed to save keys", http.StatusInternalServerError)
		return
	}

	log.Printf("Handler: Successfully saved API keys for user %d", userID)

	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]string{
		"message": "API keys saved successfully",
	}); err != nil {
		log.Printf("Handler: ERROR: Failed to encode SaveKeys response: %v", err)
	}
}

func (h *Handler) DeleteKeys(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	defer func() {
		log.Printf("Handler: DeleteKeys completed (total time: %v)", time.Since(startTime))
	}()

	userID := r.Context().Value(middleware.UserIDKey).(int64)
	log.Printf("Handler: DeleteKeys called for user %d", userID)

	if err := h.KeysRepo.DeleteByUserID(r.Context(), userID); err != nil {
		log.Printf("Handler: ERROR: Failed to delete keys for user %d: %v", userID, err)
		http.Error(w, "failed to delete keys", http.StatusInternalServerError)
		return
	}

	// Также очищаем ресурсы в WebSocketManager
	go h.WebSocketManager.CleanupUserResources(userID)

	log.Printf("Handler: Successfully deleted API keys for user %d", userID)

	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]string{
		"message": "API keys deleted successfully",
	}); err != nil {
		log.Printf("Handler: ERROR: Failed to encode DeleteKeys response: %v", err)
	}
}

func (h *Handler) CreateSignal(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	defer func() {
		log.Printf("Handler: CreateSignal completed (total time: %v)", time.Since(startTime))
	}()

	// Восстанавливаем панику для предотвращения падения сервера
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Handler: PANIC RECOVERED in CreateSignal: %v\n%s", r, debug.Stack())
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}
	}()

	userID := r.Context().Value(middleware.UserIDKey).(int64)
	var req dto.CreateSignalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Printf("Handler: ERROR: Invalid JSON in CreateSignal request: %v", err)
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if err := dto.Validate.Struct(req); err != nil {
		log.Printf("Handler: ERROR: Validation failed in CreateSignal: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	log.Printf("Handler: CreateSignal called for user %d, symbol %s, price %.8f, market %s, autoClose %v",
		userID, req.Symbol, req.TargetPrice, req.Market, req.AutoClose)

	// Проверяем подписку
	subscribed, err := h.SubscriptionService.IsUserSubscribed(r.Context(), userID)
	if err != nil {
		log.Printf("Handler: ERROR: IsUserSubscribed failed for user %d: %v", userID, err)
		http.Error(w, "failed to check subscription", http.StatusInternalServerError)
		return
	}

	if !subscribed {
		log.Printf("Handler: User %d is not subscribed, blocking CreateSignal", userID)
		http.Error(w, "subscription required", http.StatusForbidden)
		return
	}

	// Получаем API-ключи пользователя
	keys, err := h.KeysRepo.GetKeys(userID)
	if err != nil {
		log.Printf("Handler: ERROR: GetKeys failed for user %d: %v", userID, err)
		http.Error(w, "API keys not found", http.StatusUnauthorized)
		return
	}

	// Расшифровываем ключи
	secret := h.Config.EncryptionSecret
	apiKey, err := service.DecryptAES(keys.APIKey, secret)
	if err != nil {
		log.Printf("Handler: ERROR: DecryptAES failed for API key for user %d: %v", userID, err)
		http.Error(w, "failed to decrypt API key", http.StatusInternalServerError)
		return
	}

	secretKey, err := service.DecryptAES(keys.SecretKey, secret)
	if err != nil {
		log.Printf("Handler: ERROR: DecryptAES failed for Secret key for user %d: %v", userID, err)
		http.Error(w, "failed to decrypt Secret key", http.StatusInternalServerError)
		return
	}

	// 🔥 Проверка: если AutoClose включён — убедимся, что позиция НЕ нулевая
	if req.AutoClose {
		if req.Market != "futures" {
			log.Printf("Handler: ERROR: AutoClose requested for non-futures market %s", req.Market)
			http.Error(w, "AutoClose is only supported for futures market", http.StatusBadRequest)
			return
		}

		log.Printf("Handler: AutoClose enabled, checking position for %s", req.Symbol)

		tempFuturesClient := futures.NewClient(apiKey, secretKey)
		posStartTime := time.Now()
		resp, err := tempFuturesClient.NewGetPositionRiskService().Symbol(req.Symbol).Do(r.Context())
		posDuration := time.Since(posStartTime)

		if err != nil {
			log.Printf("Handler: ERROR: Failed to check position for auto-close: %v (took %v)", err, posDuration)
			http.Error(w, "failed to verify position", http.StatusInternalServerError)
			return
		}

		log.Printf("Handler: Position check completed (took %v)", posDuration)

		if len(resp) == 0 {
			log.Printf("Handler: ERROR: No position data returned for symbol %s", req.Symbol)
			http.Error(w, "no position data available", http.StatusBadRequest)
			return
		}

		positionAmt := resp[0].PositionAmt
		log.Printf("Handler: Current position for %s: %s", req.Symbol, positionAmt)

		if positionAmt == "0" || positionAmt == "0.00000000" || positionAmt == "0.0" {
			log.Printf("Handler: ERROR: Cannot create auto-close signal: position is zero for %s", req.Symbol)
			http.Error(w, "cannot create auto-close signal: position is zero", http.StatusBadRequest)
			return
		}

		log.Printf("Handler: Position for %s is %s, allowing auto-close signal", req.Symbol, positionAmt)
	}

	// Создаём HTTP-клиент для получения начального состояния
	client := service.NewBinanceHTTPClient(apiKey, secretKey)
	const initialBookLimit = 1000
	bookStartTime := time.Now()
	ob, err := client.GetOrderBook(req.Symbol, initialBookLimit, req.Market)
	bookDuration := time.Since(bookStartTime)

	if err != nil {
		log.Printf("Handler: ERROR: GetOrderBook failed for symbol %s, market %s: %v (took %v)",
			req.Symbol, req.Market, err, bookDuration)
		http.Error(w, fmt.Sprintf("failed to fetch initial order book: %v", err), http.StatusInternalServerError)
		return
	}

	log.Printf("Handler: Orderbook fetched successfully (took %v), contains %d bids and %d asks",
		bookDuration, len(ob.Bids), len(ob.Asks))

	var initialQty float64 = 0
	var initialSide string = "UNKNOWN"

	// Ищем заявку на нужной цене в bids
	for _, bid := range ob.Bids {
		if bid.Price == req.TargetPrice {
			initialQty = bid.Quantity
			initialSide = bid.Side
			log.Printf("Handler: Found initial bid at %.8f with quantity %.4f", req.TargetPrice, initialQty)
			break
		}
	}

	// Если не нашли в bids, ищем в asks
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

	if initialQty == 0 {
		log.Printf("Handler: ERROR: No order found at price %.8f", req.TargetPrice)

		// Для отладки выводим ближайшие цены
		if len(ob.Bids) > 0 {
			log.Printf("Handler: Closest bid price: %.8f", ob.Bids[0].Price)
		}
		if len(ob.Asks) > 0 {
			log.Printf("Handler: Closest ask price: %.8f", ob.Asks[0].Price)
		}

		http.Error(w, fmt.Sprintf("no order found at price %.8f", req.TargetPrice), http.StatusBadRequest)
		return
	}

	if initialQty < req.MinQuantity {
		log.Printf("Handler: ERROR: Initial quantity %.4f at price %.8f is less than min quantity %.4f",
			initialQty, req.TargetPrice, req.MinQuantity)
		http.Error(w, fmt.Sprintf("initial quantity %.4f at price %.8f is less than min quantity %.4f",
			initialQty, req.TargetPrice, req.MinQuantity), http.StatusBadRequest)
		return
	}

	log.Printf("Handler: Found order at target price: %.8f, quantity: %.4f, side: %s",
		req.TargetPrice, initialQty, initialSide)

	// 🔥 КЛЮЧЕВОЕ ИЗМЕНЕНИЕ: используем мультиюзерный метод
	log.Printf("Handler: Getting or creating watcher for user %d, symbol %s, market %s, autoClose %v",
		userID, req.Symbol, req.Market, req.AutoClose)

	watcherStartTime := time.Now()
	watcher, err := h.WebSocketManager.GetOrCreateWatcherForUser(userID, req.Symbol, req.Market, req.AutoClose)
	watcherDuration := time.Since(watcherStartTime)

	if err != nil {
		log.Printf("Handler: ERROR: Failed to get watcher for user %d, symbol %s: %v (took %v)",
			userID, req.Symbol, err, watcherDuration)
		http.Error(w, "failed to create watcher", http.StatusInternalServerError)
		return
	}

	log.Printf("Handler: Watcher obtained successfully (took %v)", watcherDuration)

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
		CloseMarket:     req.Market,
		WatchMarket:     req.Market,
		OriginalQty:     initialQty,
		LastQty:         initialQty,
		OriginalSide:    initialSide,
	}

	log.Printf("Handler: Adding signal %d to watcher for user %d, symbol %s, initialQty %.4f, side %s",
		signal.ID, userID, req.Symbol, initialQty, initialSide)

	addSignalStartTime := time.Now()
	watcher.AddSignal(signal)
	addSignalDuration := time.Since(addSignalStartTime)

	log.Printf("Handler: Signal %d added successfully (took %v)", signal.ID, addSignalDuration)

	response := map[string]interface{}{
		"message":          "signal created successfully",
		"signal_id":        signal.ID,
		"symbol":           req.Symbol,
		"target_price":     req.TargetPrice,
		"initial_quantity": initialQty,
		"side":             initialSide,
	}

	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("Handler: ERROR: Failed to encode CreateSignal response: %v", err)
	}
}

func (h *Handler) GetOrderAtPrice(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	defer func() {
		log.Printf("Handler: GetOrderAtPrice completed (total time: %v)", time.Since(startTime))
	}()

	symbol := r.URL.Query().Get("symbol")
	priceStr := r.URL.Query().Get("price")
	market := r.URL.Query().Get("market") // "spot" или "futures"

	if symbol == "" || priceStr == "" {
		log.Printf("Handler: ERROR: Missing required parameters in GetOrderAtPrice: symbol=%s, price=%s", symbol, priceStr)
		http.Error(w, "symbol and price are required", http.StatusBadRequest)
		return
	}

	price, err := strconv.ParseFloat(priceStr, 64)
	if err != nil {
		log.Printf("Handler: ERROR: Invalid price parameter in GetOrderAtPrice: %s", priceStr)
		http.Error(w, "invalid price", http.StatusBadRequest)
		return
	}

	if market == "" {
		market = "futures" // по умолчанию
		log.Printf("Handler: Default market set to %s for GetOrderAtPrice", market)
	}

	// Получаем userID
	userID := r.Context().Value(middleware.UserIDKey).(int64)
	log.Printf("Handler: GetOrderAtPrice called by user %d, symbol=%s, price=%.8f, market=%s",
		userID, symbol, price, market)

	// Проверяем подписку
	subscribed, err := h.SubscriptionService.IsUserSubscribed(r.Context(), userID)
	if err != nil {
		log.Printf("Handler: ERROR: IsUserSubscribed failed for user %d: %v", userID, err)
		http.Error(w, "failed to check subscription", http.StatusInternalServerError)
		return
	}

	if !subscribed {
		log.Printf("Handler: User %d is not subscribed, blocking GetOrderAtPrice", userID)
		http.Error(w, "subscription required", http.StatusForbidden)
		return
	}

	// Получаем API-ключи
	keys, err := h.KeysRepo.GetKeys(userID)
	if err != nil {
		log.Printf("Handler: ERROR: GetKeys failed for user %d: %v", userID, err)
		http.Error(w, "API keys not found", http.StatusUnauthorized)
		return
	}

	secret := h.Config.EncryptionSecret
	apiKey, err := service.DecryptAES(keys.APIKey, secret)
	if err != nil {
		log.Printf("Handler: ERROR: DecryptAES failed for API key for user %d: %v", userID, err)
		http.Error(w, "failed to decrypt API key", http.StatusInternalServerError)
		return
	}

	// Получаем ордербук
	client := service.NewBinanceHTTPClient(apiKey, "")   // apiKey, secretKey не нужен для публичных запросов
	ob, err := client.GetOrderBook(symbol, 1000, market) // 1000 — максимум
	if err != nil {
		log.Printf("Handler: ERROR: GetOrderBook failed in GetOrderAtPrice: %v", err)
		http.Error(w, "failed to get order book", http.StatusInternalServerError)
		return
	}

	log.Printf("Handler: Orderbook retrieved for GetOrderAtPrice, contains %d bids and %d asks",
		len(ob.Bids), len(ob.Asks))

	// Ищем заявку на нужной цене
	var foundOrder *entity.Order
	for _, bid := range ob.Bids {
		if bid.Price == price {
			foundOrder = &bid
			log.Printf("Handler: Found bid order at price %.8f with quantity %.4f", price, bid.Quantity)
			break
		}
	}

	if foundOrder == nil {
		for _, ask := range ob.Asks {
			if ask.Price == price {
				foundOrder = &ask
				log.Printf("Handler: Found ask order at price %.8f with quantity %.4f", price, ask.Quantity)
				break
			}
		}
	}

	response := make(map[string]interface{})
	response["price"] = price
	response["market"] = market
	response["symbol"] = symbol

	if foundOrder == nil {
		response["is_present"] = false
		response["message"] = "no order found at this price"
		log.Printf("Handler: No order found at price %.8f for symbol %s", price, symbol)
	} else {
		response["is_present"] = true
		response["quantity"] = foundOrder.Quantity
		response["side"] = foundOrder.Side
		log.Printf("Handler: Found order at price %.8f: side=%s, quantity=%.4f",
			price, foundOrder.Side, foundOrder.Quantity)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("Handler: ERROR: Failed to encode response in GetOrderAtPrice: %v", err)
	}
}

// generateID — генератор ID (можно использовать UUID или просто счётчик)
func generateID() int64 {
	// Реализуй как хочешь: UUID, auto-increment, etc.
	// Пока просто временный вариант
	return time.Now().UnixNano() % 1000000
}
