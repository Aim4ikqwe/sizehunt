// internal/binance/transport/http/handler.go
package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
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
	"github.com/go-chi/chi/v5"
	"github.com/sony/gobreaker"
)

type Handler struct {
	BinanceService      *service.Watcher
	KeysRepo            *repository.PostgresKeysRepo
	Config              *config.Config
	WebSocketManager    *service.WebSocketManager
	SubscriptionService *subscriptionservice.Service
	Server              *http.Server
	SignalRepository    repository.SignalRepository
}

func NewBinanceHandler(
	watcher *service.Watcher,
	keysRepo *repository.PostgresKeysRepo,
	cfg *config.Config,
	wsManager *service.WebSocketManager,
	subService *subscriptionservice.Service,
	server *http.Server,
	signalRepo repository.SignalRepository,
) *Handler {
	handler := &Handler{
		BinanceService:      watcher,
		KeysRepo:            keysRepo,
		Config:              cfg,
		WebSocketManager:    wsManager,
		SubscriptionService: subService,
		Server:              server,
		SignalRepository:    signalRepo,
	}
	log.Println("BinanceHandler: Initialized successfully")
	return handler
}

func (h *Handler) GetOrderBook(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
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
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
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
	// 1. Валидация входных данных (быстрые операции)
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
	// 2. Проверка подписки (быстрая операция с БД)
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
	// 3. Получение и расшифровка ключей (относительно быстрые операции)
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
	secretKey, err := service.DecryptAES(keys.SecretKey, secret)
	if err != nil {
		log.Printf("Handler: ERROR: DecryptAES failed for Secret key for user %d: %v", userID, err)
		http.Error(w, "failed to decrypt Secret key", http.StatusInternalServerError)
		return
	}

	// 4. Проверка, есть ли уже активные сигналы для этой монеты у пользователя
	existingSignals, err := h.SignalRepository.GetActiveByUserAndSymbol(r.Context(), userID, req.Symbol)
	if err != nil {
		log.Printf("Handler: ERROR: Failed to check existing signals for user %d, symbol %s: %v", userID, req.Symbol, err)
		http.Error(w, "failed to check existing signals", http.StatusInternalServerError)
		return
	}

	// 5. Деактивируем все существующие сигналы для этой монеты
	for _, existingSignal := range existingSignals {
		if err := h.SignalRepository.Deactivate(r.Context(), existingSignal.ID); err != nil {
			log.Printf("Handler: WARNING: Failed to deactivate existing signal %d: %v", existingSignal.ID, err)
		} else {
			log.Printf("Handler: Deactivated existing signal %d for user %d, symbol %s", existingSignal.ID, userID, req.Symbol)
		}

		// Также удаляем сигнал из WebSocketManager
		go h.WebSocketManager.DeleteUserSignal(userID, existingSignal.ID)
	}

	// 6. Проверка позиции для auto-close (если требуется)
	var positionCheckDuration time.Duration
	var positionAmt float64
	if req.AutoClose {
		if !h.isBinanceAPIAvailable() {
			log.Printf("Handler: Binance API unavailable, cannot create auto-close signal")
			http.Error(w, "Binance API temporarily unavailable. Cannot create auto-close signal.", http.StatusServiceUnavailable)
			return
		}
		if req.Market != "futures" {
			log.Printf("Handler: ERROR: AutoClose requested for non-futures market %s", req.Market)
			http.Error(w, "AutoClose is only supported for futures market", http.StatusBadRequest)
			return
		}
		log.Printf("Handler: AutoClose enabled, checking position for %s", req.Symbol)
		tempFuturesClient := futures.NewClient(apiKey, secretKey)
		posStartTime := time.Now()
		resp, err := tempFuturesClient.NewGetPositionRiskService().Symbol(req.Symbol).Do(r.Context())
		positionCheckDuration = time.Since(posStartTime)
		if err != nil {
			log.Printf("Handler: ERROR: Failed to check position for auto-close: %v (took %v)", err, positionCheckDuration)
			http.Error(w, "failed to verify position", http.StatusInternalServerError)
			return
		}
		if len(resp) == 0 {
			log.Printf("Handler: ERROR: No position data returned for symbol %s", req.Symbol)
			http.Error(w, "no position data available", http.StatusBadRequest)
			return
		}

		positionAmtStr := resp[0].PositionAmt
		positionAmt, err = strconv.ParseFloat(positionAmtStr, 64)
		if err != nil {
			log.Printf("Handler: ERROR: Failed to parse position amount %s: %v", positionAmtStr, err)
			http.Error(w, "invalid position data", http.StatusBadRequest)
			return
		}

		log.Printf("Handler: Current position for %s: %s (%.6f)", req.Symbol, positionAmtStr, positionAmt)

		// Если позиция нулевая, не создаем сигнал с auto-close
		if positionAmt == 0 {
			log.Printf("Handler: ERROR: Cannot create auto-close signal: position is zero for %s", req.Symbol)
			http.Error(w, "cannot create auto-close signal: position is zero", http.StatusBadRequest)
			return
		}

		log.Printf("Handler: Position for %s is %.6f, allowing auto-close signal (check took %v)",
			req.Symbol, positionAmt, positionCheckDuration)
	}

	// 7. Получение ордербука (сетевая операция, но необходима)
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

	// 8. Поиск заявки на целевой цене
	initialQty, initialSide := h.findOrderAtPrice(ob, req.TargetPrice, req.MinQuantity)
	if initialQty == 0 {
		log.Printf("Handler: ERROR: No order found at price %.8f", req.TargetPrice)
		http.Error(w, fmt.Sprintf("no order found at price %.8f", req.TargetPrice), http.StatusBadRequest)
		return
	}
	log.Printf("Handler: Found order at target price: %.8f, quantity: %.4f, side: %s",
		req.TargetPrice, initialQty, initialSide)

	// 9. Создание структуры сигнала для БД
	signalDB := &repository.SignalDB{
		UserID:          userID,
		Symbol:          req.Symbol,
		TargetPrice:     req.TargetPrice,
		MinQuantity:     req.MinQuantity,
		TriggerOnCancel: req.TriggerOnCancel,
		TriggerOnEat:    req.TriggerOnEat,
		EatPercentage:   req.EatPercentage,
		OriginalQty:     initialQty,
		LastQty:         initialQty,
		AutoClose:       req.AutoClose,
		CloseMarket:     req.Market,
		WatchMarket:     req.Market,
		OriginalSide:    initialSide,
		CreatedAt:       time.Now(),
		IsActive:        true,
	}

	// 10. Сохранение в БД (блокирующая операция, но необходима перед ответом)
	saveStartTime := time.Now()
	if err := h.SignalRepository.Save(r.Context(), signalDB); err != nil {
		log.Printf("Handler: ERROR: Failed to save signal to database: %v (took %v)", err, time.Since(saveStartTime))
		http.Error(w, "failed to save signal", http.StatusInternalServerError)
		return
	}
	saveDuration := time.Since(saveStartTime)
	log.Printf("Handler: Signal saved to database with ID %d (took %v)", signalDB.ID, saveDuration)

	// 11. Получение watcher'а с минимальным временем удержания блокировки
	log.Printf("Handler: Getting or creating watcher for user %d, symbol %s, market %s, autoClose %v",
		userID, req.Symbol, req.Market, req.AutoClose)
	watcherStartTime := time.Now()
	watcher, err := h.WebSocketManager.GetOrCreateWatcherForUser(userID, req.Symbol, req.Market, req.AutoClose)
	watcherDuration := time.Since(watcherStartTime)
	if err != nil {
		// При ошибке получения watcher'а - удаляем сигнал из БД
		if delErr := h.SignalRepository.Delete(r.Context(), signalDB.ID); delErr != nil {
			log.Printf("Handler: ERROR: Failed to clean up signal %d after watcher error: %v", signalDB.ID, delErr)
		}
		log.Printf("Handler: ERROR: Failed to get watcher for user %d, symbol %s: %v (took %v)",
			userID, req.Symbol, err, watcherDuration)
		http.Error(w, "failed to create watcher", http.StatusInternalServerError)
		return
	}
	log.Printf("Handler: Watcher obtained successfully (took %v)", watcherDuration)

	// 12. Создание сигнала для внутренней обработки
	signal := &service.Signal{
		ID:              signalDB.ID,
		UserID:          signalDB.UserID,
		Symbol:          signalDB.Symbol,
		TargetPrice:     signalDB.TargetPrice,
		MinQuantity:     signalDB.MinQuantity,
		TriggerOnCancel: signalDB.TriggerOnCancel,
		TriggerOnEat:    signalDB.TriggerOnEat,
		EatPercentage:   signalDB.EatPercentage,
		OriginalQty:     signalDB.OriginalQty,
		LastQty:         signalDB.LastQty,
		AutoClose:       signalDB.AutoClose,
		CloseMarket:     signalDB.CloseMarket,
		WatchMarket:     signalDB.WatchMarket,
		OriginalSide:    signalDB.OriginalSide,
		CreatedAt:       signalDB.CreatedAt,
	}

	// 13. Формирование ответа до добавления сигнала в watcher
	response := map[string]interface{}{
		"message":          "signal created successfully",
		"signal_id":        signal.ID,
		"symbol":           req.Symbol,
		"target_price":     req.TargetPrice,
		"initial_quantity": initialQty,
		"side":             initialSide,
	}

	// 14. Отправка ответа клиенту
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("Handler: ERROR: Failed to encode CreateSignal response: %v", err)
	}

	// 15. Асинхронное добавление сигнала в watcher
	log.Printf("Handler: Adding signal %d to watcher for user %d, symbol %s, initialQty %.4f, side %s (async)",
		signal.ID, userID, req.Symbol, initialQty, initialSide)
	go func() {
		addSignalStartTime := time.Now()
		watcher.AddSignal(signal)
		addSignalDuration := time.Since(addSignalStartTime)
		log.Printf("Handler: Signal %d added to watcher successfully (async, took %v)", signal.ID, addSignalDuration)
	}()
}

// findOrderAtPrice ищет заявку на целевой цене в ордербуке
func (h *Handler) findOrderAtPrice(ob *service.OrderBook, targetPrice, minQuantity float64) (float64, string) {
	// Ищем в bids
	for _, bid := range ob.Bids {
		if bid.Price == targetPrice {
			if bid.Quantity >= minQuantity {
				return bid.Quantity, bid.Side
			}
			log.Printf("Handler: Found bid at %.8f but quantity %.4f is less than required %.4f",
				targetPrice, bid.Quantity, minQuantity)
			return 0, ""
		}
	}

	// Если не нашли в bids, ищем в asks
	for _, ask := range ob.Asks {
		if ask.Price == targetPrice {
			if ask.Quantity >= minQuantity {
				return ask.Quantity, ask.Side
			}
			log.Printf("Handler: Found ask at %.8f but quantity %.4f is less than required %.4f",
				targetPrice, ask.Quantity, minQuantity)
			return 0, ""
		}
	}

	// Для отладки выводим ближайшие цены
	if len(ob.Bids) > 0 {
		log.Printf("Handler: Closest bid price: %.8f", ob.Bids[0].Price)
	}
	if len(ob.Asks) > 0 {
		log.Printf("Handler: Closest ask price: %.8f", ob.Asks[0].Price)
	}

	return 0, ""
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

type SignalResponse struct {
	ID              int64     `json:"id"`
	Symbol          string    `json:"symbol"`
	TargetPrice     float64   `json:"target_price"`
	MinQuantity     float64   `json:"min_quantity"`
	TriggerOnCancel bool      `json:"trigger_on_cancel"`
	TriggerOnEat    bool      `json:"trigger_on_eat"`
	EatPercentage   float64   `json:"eat_percentage"`
	OriginalQty     float64   `json:"original_qty"`
	LastQty         float64   `json:"last_qty"`
	AutoClose       bool      `json:"auto_close"`
	CloseMarket     string    `json:"close_market"`
	WatchMarket     string    `json:"watch_market"`
	OriginalSide    string    `json:"original_side"`
	CreatedAt       time.Time `json:"created_at"`
}

func (h *Handler) GetSignals(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(middleware.UserIDKey).(int64)
	log.Printf("Handler: GetSignals called for user %d", userID)

	signals := h.WebSocketManager.GetUserSignals(userID)

	log.Printf("Handler: Found %d signals for user %d", len(signals), userID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(signals)
}

func (h *Handler) DeleteSignal(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(middleware.UserIDKey).(int64)
	signalIDStr := chi.URLParam(r, "id")

	signalID, err := strconv.ParseInt(signalIDStr, 10, 64)
	if err != nil {
		log.Printf("Handler: ERROR: Invalid signal ID format: %s", signalIDStr)
		http.Error(w, "invalid signal ID format", http.StatusBadRequest)
		return
	}

	log.Printf("Handler: DeleteSignal called for user %d, signal ID %d", userID, signalID)

	err = h.WebSocketManager.DeleteUserSignal(userID, signalID)
	if err != nil {
		log.Printf("Handler: ERROR: Failed to delete signal %d for user %d: %v", signalID, userID, err)
		http.Error(w, "signal not found", http.StatusNotFound)
		return
	}

	log.Printf("Handler: Signal %d successfully deleted for user %d", signalID, userID)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"message": "signal deleted successfully"})
}

var server *http.Server

func (h *Handler) GracefulShutdown(w http.ResponseWriter, r *http.Request) {
	log.Println("Graceful shutdown requested via API endpoint")

	// Отправляем ответ клиенту
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"message": "server is shutting down"})

	// Запускаем shutdown в отдельной горутине
	go func() {
		// Небольшая задержка для отправки ответа
		time.Sleep(100 * time.Millisecond)

		log.Println("Starting graceful shutdown process")

		// Получаем список всех пользователей из WebSocketManager
		userIDs := h.WebSocketManager.GetAllUserIDs()

		for _, userID := range userIDs {
			log.Printf("Cleaning up resources for user %d", userID)
			h.WebSocketManager.CleanupUserResources(userID)
		}

		log.Println("All user resources cleaned up")

		// Создаем контекст с таймаутом для graceful shutdown
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		log.Println("Initiating server shutdown")
		if err := h.Server.Shutdown(ctx); err != nil {
			log.Printf("Server shutdown failed: %v", err)
		}

		// Выходим из приложения после успешного завершения
		os.Exit(0)
	}()
}

// generateID — генератор ID (можно использовать UUID или просто счётчик)
func generateID() int64 {
	// Реализуй как хочешь: UUID, auto-increment, etc.
	// Пока просто временный вариант
	return time.Now().UnixNano() % 1000000
}
func (h *Handler) isBinanceAPIAvailable() bool {
	cb := h.BinanceService.GetFuturesCB() // Нужно добавить такой метод в Watcher
	if cb == nil {
		return true
	}

	state := cb.State()
	return state == gobreaker.StateClosed || state == gobreaker.StateHalfOpen
}
