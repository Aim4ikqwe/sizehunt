package http

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"sizehunt/internal/api/dto"
	"sizehunt/internal/token"
	tokenrepository "sizehunt/internal/token/repository"
	"sizehunt/internal/user/service"
	"sizehunt/pkg/middleware"
)

type Handler struct {
	UserService      *service.UserService
	JWT              *service.JWTManager
	RefreshTokenRepo *tokenrepository.RefreshTokenRepository
}

func NewHandler(us *service.UserService, jwtSecret string, rtRepo *tokenrepository.RefreshTokenRepository) *Handler {
	return &Handler{
		UserService:      us,
		JWT:              service.NewJWTManager(jwtSecret),
		RefreshTokenRepo: rtRepo,
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

func (h *Handler) GetAdminStatus(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(middleware.UserIDKey).(int64)

	isAdmin, err := h.UserService.IsAdmin(r.Context(), userID)
	if err != nil {
		http.Error(w, "Failed to check admin status", http.StatusInternalServerError)
		return
	}

	resp := map[string]bool{
		"is_admin": isAdmin,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
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

	// Генерируем пару токенов
	accessToken, refreshToken, err := h.JWT.GeneratePair(u.ID, u.Email)
	if err != nil {
		http.Error(w, "token generation failed", http.StatusInternalServerError)
		return
	}

	// Создаём refresh-токен в БД
	rt, err := token.NewRefreshToken(u.ID)
	if err != nil {
		http.Error(w, "failed to generate refresh token", http.StatusInternalServerError)
		return
	}
	rt.Token = refreshToken

	ctx := r.Context()
	if err := h.RefreshTokenRepo.Save(ctx, rt); err != nil {
		http.Error(w, "failed to save refresh token", http.StatusInternalServerError)
		return
	}

	resp := map[string]string{
		"access_token":  accessToken,
		"refresh_token": refreshToken,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	refreshToken := r.Header.Get("Authorization")
	if refreshToken == "" || !strings.HasPrefix(refreshToken, "Bearer ") {
		http.Error(w, "refresh token required", http.StatusUnauthorized)
		return
	}

	refreshToken = strings.TrimPrefix(refreshToken, "Bearer ")

	ctx := r.Context()

	dbToken, err := h.RefreshTokenRepo.GetByToken(ctx, refreshToken)
	if err != nil || dbToken.ExpiresAt.Before(time.Now()) {
		http.Error(w, "invalid or expired refresh token", http.StatusUnauthorized)
		return
	}

	// Генерируем новую пару
	newAccess, newRefresh, err := h.JWT.GeneratePair(dbToken.UserID, "temp_email_placeholder")
	if err != nil {
		http.Error(w, "token generation failed", http.StatusInternalServerError)
		return
	}

	// Удаляем старый refresh-токен
	if err := h.RefreshTokenRepo.DeleteByToken(ctx, refreshToken); err != nil {
		http.Error(w, "failed to invalidate old token", http.StatusInternalServerError)
		return
	}

	// Сохраняем новый refresh-токен
	newRefreshTokenObj, err := token.NewRefreshToken(dbToken.UserID)
	if err != nil {
		http.Error(w, "failed to generate new refresh token", http.StatusInternalServerError)
		return
	}
	newRefreshTokenObj.Token = newRefresh

	if err := h.RefreshTokenRepo.Save(ctx, newRefreshTokenObj); err != nil {
		http.Error(w, "failed to save new refresh token", http.StatusInternalServerError)
		return
	}

	resp := map[string]string{
		"access_token":  newAccess,
		"refresh_token": newRefresh,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
