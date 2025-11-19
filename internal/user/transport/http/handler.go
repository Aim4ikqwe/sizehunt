package http

import (
	"encoding/json"
	"net/http"

	"sizehunt/internal/api/dto"
	"sizehunt/internal/user/service"
)

type Handler struct {
	UserService *service.UserService
	JWT         *service.JWTManager
}

func NewHandler(us *service.UserService, jwtSecret string) *Handler {
	return &Handler{
		UserService: us,
		JWT:         service.NewJWTManager(jwtSecret),
	}
}

func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	var req dto.RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if err := dto.Validate.Struct(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	u, err := h.UserService.Register(r.Context(), req.Email, req.Password)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	resp := map[string]interface{}{
		"id":    u.ID,
		"email": u.Email,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req dto.LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if err := dto.Validate.Struct(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	u, err := h.UserService.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}

	token, err := h.JWT.Generate(u.ID, u.Email)
	if err != nil {
		http.Error(w, "token error", http.StatusInternalServerError)
		return
	}

	resp := map[string]interface{}{
		"id":    u.ID,
		"email": u.Email,
		"token": token,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
