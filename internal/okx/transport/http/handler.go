// internal/okx/transport/http/handler.go
package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sizehunt/internal/api/dto"
	"sizehunt/internal/config"
	"sizehunt/internal/okx/entity"
	"sizehunt/internal/okx/repository"
	"sizehunt/internal/okx/service"
	proxy_service "sizehunt/internal/proxy/service"
	subscriptionservice "sizehunt/internal/subscription/service"
	"sizehunt/pkg/middleware"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"
	"github.com/sony/gobreaker"
)

// Handler обработчик HTTP запросов для OKX
type Handler struct {
	OKXService          *service.Watcher
	KeysRepo            *repository.PostgresKeysRepo
	Config              *config.Config
	WebSocketManager    *service.WebSocketManager
	SubscriptionService *subscriptionservice.Service
	Server              *http.Server
	SignalRepository    repository.SignalRepository
	ProxyService        *proxy_service.ProxyService
}

// NewOKXHandler создает новый обработчик для OKX
func NewOKXHandler(
	watcher *service.Watcher,
	keysRepo *repository.PostgresKeysRepo,
	cfg *config.Config,
	wsManager *service.WebSocketManager,
	subService *subscriptionservice.Service,
	server *http.Server,
	signalRepo repository.SignalRepository,
	proxyService *proxy_service.ProxyService,
) *Handler {
	handler := &Handler{
		OKXService:          watcher,
		KeysRepo:            keysRepo,
		Config:              cfg,
		WebSocketManager:    wsManager,
		SubscriptionService: subService,
		Server:              server,
		SignalRepository:    signalRepo,
		ProxyService:        proxyService,
	}
	log.Println("OKXHandler: Initialized successfully")
	return handler
}

// GetOrderBook возвращает ордербук OKX
func (h *Handler) GetOrderBook(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)

	instID := r.URL.Query().Get("instId")
	limitStr := r.URL.Query().Get("limit")
	instType := r.URL.Query().Get("instType")
	userID := r.Context().Value(middleware.UserIDKey).(int64)

	log.Printf("OKXHandler: GetOrderBook called by user %d, instID: %s, limit: %s, instType: %s",
		userID, instID, limitStr, instType)

	if instID == "" {
		http.Error(w, "instId parameter is required", http.StatusBadRequest)
		return
	}
	if instType == "" {
		instType = "SWAP"
	}
	limit := 100
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil {
			limit = l
		}
	}

	// Проверка прокси
	if _, hasProxy := h.ProxyService.GetProxyAddressForUser(userID); !hasProxy {
		log.Printf("OKXHandler: ERROR: Proxy not configured for user %d", userID)
		http.Error(w, "proxy configuration is required", http.StatusForbidden)
		return
	}

	// Получаем API-ключи
	keys, err := h.KeysRepo.GetKeys(userID)
	if err != nil {
		log.Printf("OKXHandler: ERROR: GetKeys failed for user %d: %v", userID, err)
		http.Error(w, "API keys not found", http.StatusUnauthorized)
		return
	}
	secret := h.Config.EncryptionSecret
	apiKey, err := service.DecryptAES(keys.APIKey, secret)
	if err != nil {
		log.Printf("OKXHandler: ERROR: DecryptAES failed for API key for user %d: %v", userID, err)
		http.Error(w, "failed to decrypt API key", http.StatusInternalServerError)
		return
	}

	// Получаем прокси и создаем клиент
	proxyAddr, _ := h.ProxyService.GetProxyAddressForUser(userID)
	client := service.NewOKXHTTPClientWithProxy(apiKey, "", "", proxyAddr)

	ob, err := client.GetOrderBook(instID, limit, instType)
	if err != nil {
		log.Printf("OKXHandler: ERROR: GetOrderBook failed for instID %s: %v", instID, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("OKXHandler: Successfully retrieved orderbook for %s, %d bids and %d asks", instID, len(ob.Bids), len(ob.Asks))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ob)
}

// SaveKeysRequest структура запроса для сохранения ключей OKX
type SaveKeysRequest struct {
	APIKey     string `json:"api_key" validate:"required,min=16"`
	SecretKey  string `json:"secret_key" validate:"required,min=16"`
	Passphrase string `json:"passphrase" validate:"required,min=6"`
}

// SaveKeys сохраняет API ключи OKX
func (h *Handler) SaveKeys(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(middleware.UserIDKey).(int64)
	var req SaveKeysRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.addErrorResponse(w, http.StatusBadRequest, "invalid JSON format: "+err.Error())
		return
	}

	// Валидация
	if err := dto.Validate.Struct(req); err != nil {
		errMessages := make([]string, 0)
		for _, err := range err.(validator.ValidationErrors) {
			field := strings.ToLower(strings.ReplaceAll(err.Field(), "_", " "))
			if err.Tag() == "required" {
				errMessages = append(errMessages, field+" is required")
			} else {
				errMessages = append(errMessages, field+" is invalid")
			}
		}
		h.addErrorResponse(w, http.StatusBadRequest, strings.Join(errMessages, "; "))
		return
	}

	// Дополнительные проверки
	if strings.Contains(req.APIKey, " ") || strings.Contains(req.SecretKey, " ") || strings.Contains(req.Passphrase, " ") {
		h.addErrorResponse(w, http.StatusBadRequest, "API keys and passphrase must not contain spaces")
		return
	}

	// Шифруем ключи
	secret := h.Config.EncryptionSecret
	encryptedAPIKey, err := service.EncryptAES(req.APIKey, secret)
	if err != nil {
		log.Printf("OKXHandler: ERROR: Failed to encrypt API key for user %d: %v", userID, err)
		http.Error(w, "failed to encrypt API key", http.StatusInternalServerError)
		return
	}
	encryptedSecretKey, err := service.EncryptAES(req.SecretKey, secret)
	if err != nil {
		log.Printf("OKXHandler: ERROR: Failed to encrypt Secret key for user %d: %v", userID, err)
		http.Error(w, "failed to encrypt Secret key", http.StatusInternalServerError)
		return
	}
	encryptedPassphrase, err := service.EncryptAES(req.Passphrase, secret)
	if err != nil {
		log.Printf("OKXHandler: ERROR: Failed to encrypt Passphrase for user %d: %v", userID, err)
		http.Error(w, "failed to encrypt Passphrase", http.StatusInternalServerError)
		return
	}

	// Сохраняем в БД
	if err := h.KeysRepo.SaveKeys(userID, encryptedAPIKey, encryptedSecretKey, encryptedPassphrase); err != nil {
		log.Printf("OKXHandler: ERROR: Failed to save keys for user %d: %v", userID, err)
		http.Error(w, "failed to save keys", http.StatusInternalServerError)
		return
	}

	log.Printf("OKXHandler: Successfully saved API keys for user %d", userID)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"message": "API keys saved successfully",
	})
}

// DeleteKeys удаляет API ключи OKX
func (h *Handler) DeleteKeys(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(middleware.UserIDKey).(int64)
	log.Printf("OKXHandler: DeleteKeys called for user %d", userID)

	if err := h.KeysRepo.DeleteByUserID(r.Context(), userID); err != nil {
		log.Printf("OKXHandler: ERROR: Failed to delete keys for user %d: %v", userID, err)
		http.Error(w, "failed to delete keys", http.StatusInternalServerError)
		return
	}

	// Очищаем ресурсы
	go h.WebSocketManager.CleanupUserResources(userID)

	// Останавливаем прокси
	if h.ProxyService != nil {
		go func() {
			if err := h.ProxyService.StopProxyForUser(r.Context(), userID); err != nil {
				log.Printf("OKXHandler: ERROR: Failed to stop proxy for user %d: %v", userID, err)
			}
		}()
	}

	log.Printf("OKXHandler: Successfully deleted API keys for user %d", userID)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"message": "API keys deleted successfully",
	})
}

// CreateSignalRequest структура запроса для создания сигнала OKX
type CreateSignalRequest struct {
	InstID          string  `json:"inst_id" validate:"required"`
	InstType        string  `json:"inst_type" validate:"required,oneof=SPOT SWAP FUTURES OPTION"`
	TargetPrice     float64 `json:"target_price" validate:"required,gt=0"`
	MinQuantity     float64 `json:"min_quantity" validate:"required,gt=0"`
	TriggerOnCancel bool    `json:"trigger_on_cancel"`
	TriggerOnEat    bool    `json:"trigger_on_eat"`
	EatPercentage   float64 `json:"eat_percentage" validate:"omitempty,gte=0.01,lte=1"`
	AutoClose       bool    `json:"auto_close"`
	CloseInstID     string  `json:"close_inst_id"`   // Опционально
	CloseInstType   string  `json:"close_inst_type"` // Опционально
}

// CreateSignal создает новый сигнал для мониторинга OKX
func (h *Handler) CreateSignal(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	userID := r.Context().Value(middleware.UserIDKey).(int64)
	proxyStarted := false // прокси поднимался в рамках этого запроса
	creationSucceeded := false
	defer func() {
		if proxyStarted && !creationSucceeded {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cleanupCancel()
			if err := h.ProxyService.CheckAndStopProxy(cleanupCtx, userID); err != nil {
				log.Printf("OKXHandler: ERROR: Failed to stop proxy after unsuccessful CreateSignal for user %d: %v", userID, err)
			}
		}
	}()
	defer func() {
		log.Printf("OKXHandler: CreateSignal completed (total time: %v)", time.Since(startTime))
	}()

	// Валидация
	var req CreateSignalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.addErrorResponse(w, http.StatusBadRequest, "invalid JSON format: "+err.Error())
		return
	}

	if err := dto.Validate.Struct(req); err != nil {
		errMessages := make([]string, 0)
		for _, err := range err.(validator.ValidationErrors) {
			field := strings.ToLower(strings.ReplaceAll(err.Field(), "_", " "))
			switch err.Tag() {
			case "required":
				errMessages = append(errMessages, field+" is required")
			case "gt":
				errMessages = append(errMessages, field+" must be greater than 0")
			case "oneof":
				errMessages = append(errMessages, field+" must be one of: "+err.Param())
			default:
				errMessages = append(errMessages, field+" is invalid")
			}
		}
		h.addErrorResponse(w, http.StatusBadRequest, strings.Join(errMessages, "; "))
		return
	}

	if req.TriggerOnEat && req.EatPercentage == 0 {
		h.addErrorResponse(w, http.StatusBadRequest, "eat_percentage is required when trigger_on_eat is enabled")
		return
	}

	// Проверка подписки
	subscribed, err := h.SubscriptionService.IsUserSubscribed(r.Context(), userID)
	if err != nil {
		log.Printf("OKXHandler: ERROR: IsUserSubscribed failed for user %d: %v", userID, err)
		http.Error(w, "failed to check subscription", http.StatusInternalServerError)
		return
	}
	if !subscribed {
		log.Printf("OKXHandler: User %d is not subscribed", userID)
		http.Error(w, "subscription required", http.StatusForbidden)
		return
	}

	// Получение ключей
	keys, err := h.KeysRepo.GetKeys(userID)
	if err != nil {
		log.Printf("OKXHandler: ERROR: GetKeys failed for user %d: %v", userID, err)
		http.Error(w, "API keys not found", http.StatusUnauthorized)
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
	passphrase, err := service.DecryptAES(keys.Passphrase, secret)
	if err != nil {
		http.Error(w, "failed to decrypt Passphrase", http.StatusInternalServerError)
		return
	}

	// Проверка прокси
	hasProxyConfig, err := h.ProxyService.Repo.GetProxyConfig(r.Context(), userID)
	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "proxy configuration is required", http.StatusForbidden)
			return
		}
		http.Error(w, "failed to check proxy configuration", http.StatusInternalServerError)
		return
	}
	if hasProxyConfig == nil {
		http.Error(w, "proxy configuration is required", http.StatusForbidden)
		return
	}

	// Запуск прокси если не запущен
	_, hasProxy := h.ProxyService.GetProxyAddressForUser(userID)
	if !hasProxy {
		log.Printf("OKXHandler: Proxy not running for user %d, attempting to start", userID)
		if err := h.ProxyService.StartProxyForUser(r.Context(), userID); err != nil {
			log.Printf("OKXHandler: ERROR: Failed to start proxy for user %d: %v", userID, err)
			http.Error(w, "failed to start proxy container", http.StatusInternalServerError)
			return
		}
		time.Sleep(500 * time.Millisecond)
		proxyStarted = true
	}

	// Деактивируем существующие сигналы для этого инструмента
	existingSignals, err := h.SignalRepository.GetActiveByUserAndInstID(r.Context(), userID, req.InstID)
	if err != nil {
		log.Printf("OKXHandler: ERROR: Failed to check existing signals: %v", err)
		http.Error(w, "failed to check existing signals", http.StatusInternalServerError)
		return
	}
	for _, existingSignal := range existingSignals {
		if err := h.SignalRepository.Deactivate(r.Context(), existingSignal.ID); err != nil {
			log.Printf("OKXHandler: WARNING: Failed to deactivate existing signal %d: %v", existingSignal.ID, err)
		}
		go h.WebSocketManager.DeleteUserSignal(userID, existingSignal.ID)
	}

	// Проверка позиции для auto-close
	var positionAmt float64
	if req.AutoClose {
		if !h.isOKXAPIAvailable() {
			http.Error(w, "OKX API temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		// Определяем инструмент для проверки позиции
		targetInstID := req.InstID
		if req.CloseInstID != "" {
			targetInstID = req.CloseInstID
		}

		proxyAddr, _ := h.ProxyService.GetProxyAddressForUser(userID)
		tempClient := service.NewOKXHTTPClientWithProxy(apiKey, secretKey, passphrase, proxyAddr)
		posResp, err := tempClient.GetPositionRisk(targetInstID)
		if err != nil {
			log.Printf("OKXHandler: ERROR: Failed to check position: %v", err)
			http.Error(w, "failed to verify position", http.StatusInternalServerError)
			return
		}
		if len(posResp.Data) > 0 {
			positionAmt, _ = strconv.ParseFloat(posResp.Data[0].Pos, 64)
		}
		if positionAmt == 0 {
			http.Error(w, fmt.Sprintf("cannot create auto-close signal: position is zero for %s", targetInstID), http.StatusBadRequest)
			return
		}
		log.Printf("OKXHandler: Position for %s is %.6f, allowing auto-close signal", targetInstID, positionAmt)
	}

	// Получение ордербука
	proxyAddr, _ := h.ProxyService.GetProxyAddressForUser(userID)
	client := service.NewOKXHTTPClientWithProxy(apiKey, secretKey, passphrase, proxyAddr)
	ob, err := client.GetOrderBook(req.InstID, 400, req.InstType)
	if err != nil {
		log.Printf("OKXHandler: ERROR: GetOrderBook failed: %v", err)
		http.Error(w, fmt.Sprintf("failed to fetch order book: %v", err), http.StatusInternalServerError)
		return
	}

	// Поиск заявки на целевой цене
	initialQty, initialSide := h.findOrderAtPrice(ob, req.TargetPrice, req.MinQuantity)
	if initialQty == 0 {
		http.Error(w, fmt.Sprintf("no order found at price %.8f", req.TargetPrice), http.StatusBadRequest)
		return
	}

	// Копируем опциональные поля для закрытия (нужны указатели для БД)
	var closeInstIDPtr, closeInstTypePtr *string
	if req.CloseInstID != "" {
		closeInstIDPtr = &req.CloseInstID
	}
	if req.CloseInstType != "" {
		closeInstTypePtr = &req.CloseInstType
	}

	// Создание сигнала в БД
	signalDB := &repository.SignalDB{
		UserID:          userID,
		InstID:          req.InstID,
		InstType:        req.InstType,
		TargetPrice:     req.TargetPrice,
		MinQuantity:     req.MinQuantity,
		TriggerOnCancel: req.TriggerOnCancel,
		TriggerOnEat:    req.TriggerOnEat,
		EatPercentage:   req.EatPercentage,
		OriginalQty:     initialQty,
		LastQty:         initialQty,
		AutoClose:       req.AutoClose,
		OriginalSide:    initialSide,
		CreatedAt:       time.Now(),
		IsActive:        true,
		CloseInstID:     closeInstIDPtr,
		CloseInstType:   closeInstTypePtr,
	}

	if err := h.SignalRepository.Save(r.Context(), signalDB); err != nil {
		log.Printf("OKXHandler: ERROR: Failed to save signal to database: %v", err)
		http.Error(w, "failed to save signal", http.StatusInternalServerError)
		return
	}
	log.Printf("OKXHandler: Signal saved to database with ID %d", signalDB.ID)

	// Получение watcher'а
	watcher, err := h.WebSocketManager.GetOrCreateWatcherForUser(userID, req.InstID, req.InstType, req.AutoClose)
	if err != nil {
		if delErr := h.SignalRepository.Delete(r.Context(), signalDB.ID); delErr != nil {
			log.Printf("OKXHandler: ERROR: Failed to clean up signal %d: %v", signalDB.ID, delErr)
		}
		log.Printf("OKXHandler: ERROR: Failed to get watcher: %v", err)
		http.Error(w, "failed to create watcher", http.StatusInternalServerError)
		return
	}

	// Создание сигнала для мониторинга
	signal := &service.Signal{
		ID:              signalDB.ID,
		UserID:          signalDB.UserID,
		InstID:          signalDB.InstID,
		InstType:        signalDB.InstType,
		TargetPrice:     signalDB.TargetPrice,
		MinQuantity:     signalDB.MinQuantity,
		TriggerOnCancel: signalDB.TriggerOnCancel,
		TriggerOnEat:    signalDB.TriggerOnEat,
		EatPercentage:   signalDB.EatPercentage,
		OriginalQty:     signalDB.OriginalQty,
		LastQty:         signalDB.LastQty,
		AutoClose:       signalDB.AutoClose,
		OriginalSide:    signalDB.OriginalSide,
		CreatedAt:       signalDB.CreatedAt,
		CloseInstID:     req.CloseInstID,
		CloseInstType:   req.CloseInstType,
	}

	// Ответ клиенту
	response := map[string]interface{}{
		"message":          "signal created successfully",
		"signal_id":        signal.ID,
		"inst_id":          req.InstID,
		"target_price":     req.TargetPrice,
		"initial_quantity": initialQty,
		"side":             initialSide,
	}

	w.WriteHeader(http.StatusCreated)
	creationSucceeded = true
	json.NewEncoder(w).Encode(response)

	// Асинхронное добавление сигнала в watcher
	go func() {
		watcher.AddSignal(signal)
		log.Printf("OKXHandler: Signal %d added to watcher", signal.ID)
	}()
}

// findOrderAtPrice ищет заявку на целевой цене в ордербуке
func (h *Handler) findOrderAtPrice(ob *service.OrderBook, targetPrice, minQuantity float64) (float64, string) {
	for _, bid := range ob.Bids {
		if bid.Price == targetPrice {
			if bid.Quantity >= minQuantity {
				return bid.Quantity, bid.Side
			}
			return 0, ""
		}
	}
	for _, ask := range ob.Asks {
		if ask.Price == targetPrice {
			if ask.Quantity >= minQuantity {
				return ask.Quantity, ask.Side
			}
			return 0, ""
		}
	}
	return 0, ""
}

// GetOrderAtPrice проверяет наличие ордера на цене
func (h *Handler) GetOrderAtPrice(w http.ResponseWriter, r *http.Request) {
	instID := r.URL.Query().Get("instId")
	priceStr := r.URL.Query().Get("price")
	instType := r.URL.Query().Get("instType")

	if priceStr == "" {
		h.addErrorResponse(w, http.StatusBadRequest, "price parameter is required")
		return
	}
	if instID == "" {
		http.Error(w, "instId is required", http.StatusBadRequest)
		return
	}
	price, err := strconv.ParseFloat(priceStr, 64)
	if err != nil {
		http.Error(w, "invalid price", http.StatusBadRequest)
		return
	}
	if instType == "" {
		instType = "SWAP"
	}

	userID := r.Context().Value(middleware.UserIDKey).(int64)

	// Проверка подписки
	subscribed, err := h.SubscriptionService.IsUserSubscribed(r.Context(), userID)
	if err != nil {
		http.Error(w, "failed to check subscription", http.StatusInternalServerError)
		return
	}
	if !subscribed {
		http.Error(w, "subscription required", http.StatusForbidden)
		return
	}

	// Получаем ключи
	keys, err := h.KeysRepo.GetKeys(userID)
	if err != nil {
		http.Error(w, "API keys not found", http.StatusUnauthorized)
		return
	}
	apiKey, _ := service.DecryptAES(keys.APIKey, h.Config.EncryptionSecret)

	proxyAddr, _ := h.ProxyService.GetProxyAddressForUser(userID)
	client := service.NewOKXHTTPClientWithProxy(apiKey, "", "", proxyAddr)

	ob, err := client.GetOrderBook(instID, 400, instType)
	if err != nil {
		http.Error(w, "failed to get order book", http.StatusInternalServerError)
		return
	}

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

	response := map[string]interface{}{
		"price":      price,
		"inst_type":  instType,
		"inst_id":    instID,
		"is_present": foundOrder != nil,
	}
	if foundOrder != nil {
		response["quantity"] = foundOrder.Quantity
		response["side"] = foundOrder.Side
	} else {
		response["message"] = "no order found at this price"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// GetSignals возвращает список сигналов пользователя
func (h *Handler) GetSignals(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(middleware.UserIDKey).(int64)
	log.Printf("OKXHandler: GetSignals called for user %d", userID)

	signals := h.WebSocketManager.GetUserSignals(userID)

	log.Printf("OKXHandler: Found %d signals for user %d", len(signals), userID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(signals)
}

// DeleteSignal удаляет сигнал по ID
func (h *Handler) DeleteSignal(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(middleware.UserIDKey).(int64)
	signalIDStr := chi.URLParam(r, "id")

	signalID, err := strconv.ParseInt(signalIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid signal ID format", http.StatusBadRequest)
		return
	}

	log.Printf("OKXHandler: DeleteSignal called for user %d, signal ID %d", userID, signalID)

	err = h.WebSocketManager.DeleteUserSignal(userID, signalID)
	if err != nil {
		log.Printf("OKXHandler: ERROR: Failed to delete signal %d: %v", signalID, err)
		http.Error(w, "signal not found", http.StatusNotFound)
		return
	}

	log.Printf("OKXHandler: Signal %d successfully deleted for user %d", signalID, userID)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"message": "signal deleted successfully"})
}

// GetKeysStatus проверяет наличие ключей у пользователя
func (h *Handler) GetKeysStatus(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(middleware.UserIDKey).(int64)

	_, err := h.KeysRepo.GetKeys(userID)
	if err != nil {
		if err == sql.ErrNoRows {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]bool{"hasKeys": false})
			return
		}
		log.Printf("OKXHandler: ERROR: Failed to check keys for user %d: %v", userID, err)
		http.Error(w, "failed to check API keys", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]bool{"hasKeys": true})
}

// isOKXAPIAvailable проверяет доступность API OKX
func (h *Handler) isOKXAPIAvailable() bool {
	cb := h.OKXService.GetFuturesCB()
	if cb == nil {
		return true
	}

	if gbCB, ok := cb.(*gobreaker.CircuitBreaker); ok {
		state := gbCB.State()
		return state == gobreaker.StateClosed || state == gobreaker.StateHalfOpen
	}
	return true
}

// addErrorResponse добавляет ответ с ошибкой
func (h *Handler) addErrorResponse(w http.ResponseWriter, statusCode int, message string) {
	log.Printf("OKXHandler error [%d]: %s", statusCode, message)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]string{
		"error": message,
	})
}
