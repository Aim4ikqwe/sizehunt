package http

import (
	"encoding/json"
	"log"
	"net/http"
	"sizehunt/internal/proxy/repository"
	"sizehunt/internal/proxy/service"
	"sizehunt/pkg/middleware"
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

	if req.SSAddr == "" || req.SSMethod == "" || req.SSPassword == "" || req.SSPort == 0 {
		http.Error(w, "all fields are required", http.StatusBadRequest)
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

	// Останавливаем прокси
	if err := h.ProxyService.StopProxyForUser(r.Context(), userID); err != nil {
		log.Printf("Failed to stop proxy for user %d: %v", userID, err)
		http.Error(w, "failed to stop proxy: "+err.Error(), http.StatusInternalServerError)
		return
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
