package http

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"sizehunt/internal/api/dto"
	"sizehunt/internal/binance/repository"
	"sizehunt/internal/binance/service"
	"sizehunt/internal/config"
	"sizehunt/pkg/middleware"
)

type Handler struct {
	BinanceService   *service.Watcher
	KeysRepo         *repository.PostgresKeysRepo
	Config           *config.Config
	WebSocketManager *service.WebSocketManager
}

func NewBinanceHandler(watcher *service.Watcher, keysRepo *repository.PostgresKeysRepo, cfg *config.Config, wsManager *service.WebSocketManager) *Handler {
	return &Handler{
		BinanceService:   watcher,
		KeysRepo:         keysRepo,
		Config:           cfg,
		WebSocketManager: wsManager,
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

	client := service.NewBinanceHTTPClient(apiKey)
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

	// Получаем WebSocketWatcher
	watcher, err := h.WebSocketManager.GetOrCreateWatcher(req.Symbol, req.Market)
	if err != nil {
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
		AutoClose:       req.AutoClose, // добавь
	}

	watcher.AddSignal(signal)

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"message":"signal created"}`))
}

// generateID — генератор ID (можно использовать UUID или просто счётчик)
func generateID() int64 {
	// Реализуй как хочешь: UUID, auto-increment, etc.
	// Пока просто временный вариант
	return time.Now().UnixNano() % 1000000
}
