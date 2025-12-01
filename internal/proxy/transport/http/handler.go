package http

import (
	"encoding/json"
	"net/http"
	"sizehunt/internal/proxy/service"
	"sizehunt/pkg/middleware"
)

type ProxyHandler struct {
	ProxyService *service.ProxyService
}

func NewProxyHandler(proxyService *service.ProxyService) *ProxyHandler {
	return &ProxyHandler{ProxyService: proxyService}
}

type ProxyConfigRequest struct {
	SSAddr     string `json:"ss_addr"`
	SSPort     int    `json:"ss_port"` // ДОБАВЛЕНО ПОЛЕ ПОРТА
	SSMethod   string `json:"encryption_method" validate:"required,oneof=aes-256-gcm chacha20-ietf-poly1305 aes-128-gcm"`
	SSPassword string `json:"ss_password"`
}

func (h *ProxyHandler) SaveProxyConfig(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(middleware.UserIDKey).(int64)
	var req ProxyConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.SSAddr == "" || req.SSMethod == "" || req.SSPassword == "" {
		http.Error(w, "all fields are required", http.StatusBadRequest)
		return
	}
	// ДОБАВЛЕНА ПЕРЕДАЧА ПОРТА В ConfigureProxy
	err := h.ProxyService.ConfigureProxy(r.Context(), userID, req.SSAddr, req.SSPort, req.SSMethod, req.SSPassword)
	if err != nil {
		http.Error(w, "failed to configure proxy: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message": "Proxy configured successfully",
	})
}
func (h *ProxyHandler) DeleteProxyConfig(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(middleware.UserIDKey).(int64)
	// Останавливаем прокси
	if err := h.ProxyService.StopProxyForUser(r.Context(), userID); err != nil {
		http.Error(w, "failed to stop proxy: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Удаляем конфиг из БД
	if err := h.ProxyService.DeleteProxyConfig(r.Context(), userID); err != nil {
		http.Error(w, "failed to delete proxy config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message": "Proxy deleted successfully",
	})
}

func (h *ProxyHandler) GetProxyStatus(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(middleware.UserIDKey).(int64)
	config, err := h.ProxyService.Repo.GetProxyConfig(r.Context(), userID)
	if err != nil {
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
		"ss_method":  config.SSMethod,
		"local_port": config.LocalPort,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
