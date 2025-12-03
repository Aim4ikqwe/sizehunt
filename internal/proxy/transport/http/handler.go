package http

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sizehunt/internal/api/dto"
	"sizehunt/internal/proxy/repository"
	"sizehunt/internal/proxy/service"
	"sizehunt/pkg/middleware"
	"strings"

	"github.com/go-playground/validator/v10"
)

type ProxyHandler struct {
	ProxyService *service.ProxyService
	ProxyRepo    repository.ProxyRepository
}

func NewProxyHandler(proxyService *service.ProxyService, proxyRepo repository.ProxyRepository) *ProxyHandler {
	return &ProxyHandler{
		ProxyService: proxyService,
		ProxyRepo:    proxyRepo,
	}
}

type ProxyConfigRequest struct {
	SSAddr     string `json:"ss_addr"`
	SSPort     int    `json:"ss_port"`
	SSMethod   string `json:"encryption_method"`
	SSPassword string `json:"ss_password"`
}

func (h *ProxyHandler) SaveProxyConfig(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(middleware.UserIDKey).(int64)
	var req ProxyConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Printf("Invalid JSON in proxy config request: %v", err)
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Используем стандартную валидацию через структурные теги
	if err := dto.Validate.Struct(req); err != nil {
		// Преобразуем ошибки валидации в человекочитаемый формат
		errMessages := make([]string, 0)
		for _, err := range err.(validator.ValidationErrors) {
			field := strings.ToLower(err.Field())
			switch err.Tag() {
			case "required":
				errMessages = append(errMessages, field+" is required")
			case "min", "max":
				errMessages = append(errMessages, field+" must be between "+err.Param()+" characters")
			case "encryption_method":
				errMessages = append(errMessages, "unsupported encryption method")
			default:
				errMessages = append(errMessages, field+" is invalid")
			}
		}
		h.addErrorResponse(w, http.StatusBadRequest, strings.Join(errMessages, "; "))
		return
	}

	// Дополнительные проверки бизнес-логики
	if err := h.validateProxyConfigRequest(&req); err != nil {
		h.addErrorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	log.Printf("Saving proxy config for user %d: %s:%d, method: %s", userID, req.SSAddr, req.SSPort, req.SSMethod)
	err := h.ProxyService.ConfigureProxy(r.Context(), userID, req.SSAddr, req.SSPort, req.SSMethod, req.SSPassword)
	if err != nil {
		log.Printf("Failed to configure proxy for user %d: %v", userID, err)
		http.Error(w, "failed to configure proxy: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// После настройки прокси проверяем, есть ли активные сигналы и пытаемся переподключить WebSocket
	if h.ProxyService.IsProxyRunningForUser(userID) {
		log.Printf("Proxy is running for user %d, checking active signals", userID)
		// Здесь можно добавить логику переподключения WebSocket для активных сигналов
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message": "Proxy configured successfully",
	})
}

func (h *ProxyHandler) DeleteProxyConfig(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(middleware.UserIDKey).(int64)
	log.Printf("Deleting proxy config for user %d", userID)

	// Сначала останавливаем прокси
	if err := h.ProxyService.StopProxyForUser(r.Context(), userID); err != nil {
		log.Printf("Failed to stop proxy for user %d: %v", userID, err)
		// Не возвращаем ошибку, продолжаем удаление конфигурации
	}

	// Удаляем конфиг из БД
	if err := h.ProxyService.DeleteProxyConfig(r.Context(), userID); err != nil {
		log.Printf("Failed to delete proxy config for user %d: %v", userID, err)
		http.Error(w, "failed to delete proxy config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("Proxy config successfully deleted for user %d", userID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message": "Proxy deleted successfully",
	})
}

func (h *ProxyHandler) GetProxyStatus(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(middleware.UserIDKey).(int64)
	log.Printf("Getting proxy status for user %d", userID)

	config, err := h.ProxyService.Repo.GetProxyConfig(r.Context(), userID)
	if err != nil {
		log.Printf("No proxy config found for user %d: %v", userID, err)
		http.Error(w, "proxy not configured", http.StatusNotFound)
		return
	}

	status := "stopped"
	if _, hasProxy := h.ProxyService.GetProxyAddressForUser(userID); hasProxy {
		status = "running"
	}

	response := map[string]interface{}{
		"configured": true,
		"status":     status,
		"ss_addr":    config.SSAddr,
		"ss_port":    config.SSPort,
		"ss_method":  config.SSMethod,
		"local_port": config.LocalPort,
	}

	log.Printf("Proxy status for user %d: %s", userID, status)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// addErrorResponse формирует стандартный ответ об ошибке
func (h *ProxyHandler) addErrorResponse(w http.ResponseWriter, statusCode int, message string) {
	log.Printf("ProxyHandler error [%d]: %s", statusCode, message)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]string{
		"error": message,
	})
}
func (h *ProxyHandler) validateProxyConfigRequest(req *ProxyConfigRequest) error {
	if req == nil {
		return errors.New("empty request body")
	}

	// Дополнительные проверки, выходящие за рамки структурной валидации
	if strings.Contains(req.SSPassword, " ") {
		return errors.New("password cannot contain spaces")
	}

	if req.SSPort < 1 || req.SSPort > 65535 {
		return errors.New("server port must be between 1 and 65535")
	}

	return nil
}
