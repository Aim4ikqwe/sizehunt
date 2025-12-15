package http

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"sizehunt/internal/promocode/service"
	"sizehunt/pkg/middleware"
)

type Handler struct {
	Service *service.Service
}

func NewHandler(service *service.Service) *Handler {
	return &Handler{Service: service}
}

// Создание промокода (доступно только администраторам)
func (h *Handler) CreatePromoCode(w http.ResponseWriter, r *http.Request) {
	// Проверяем, является ли пользователь администратором
	userID := r.Context().Value(middleware.UserIDKey).(int64)
	isAdmin, err := h.Service.UserService.IsAdmin(r.Context(), userID)
	if err != nil || !isAdmin {
		http.Error(w, "доступ запрещен", http.StatusForbidden)
		return
	}

	var req struct {
		Code         string  `json:"code"`
		DaysDuration int     `json:"days_duration"`
		MaxUses      int     `json:"max_uses"`
		ExpiresAt    *string `json:"expires_at"` // ISO формат даты
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "некорректный JSON", http.StatusBadRequest)
		return
	}

	var expiresAt *time.Time
	if req.ExpiresAt != nil {
		parsedTime, err := time.Parse(time.RFC3339, *req.ExpiresAt)
		if err != nil {
			http.Error(w, "некорректный формат даты", http.StatusBadRequest)
			return
		}
		expiresAt = &parsedTime
	}

	promoCode, err := h.Service.CreatePromoCode(r.Context(), userID, req.Code, req.DaysDuration, req.MaxUses, expiresAt)
	if err != nil {
		status := http.StatusInternalServerError
		if err == service.ErrNotAdmin {
			status = http.StatusForbidden
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(promoCode)
}

// Применение промокода (доступно всем пользователям)
func (h *Handler) ApplyPromoCode(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(middleware.UserIDKey).(int64)

	var req struct {
		Code string `json:"code"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "некорректный JSON", http.StatusBadRequest)
		return
	}

	err := h.Service.ApplyPromoCode(r.Context(), userID, req.Code)
	if err != nil {
		status := http.StatusBadRequest
		if err == service.ErrPromoCodeNotFound ||
			err == service.ErrPromoCodeExpired ||
			err == service.ErrPromoCodeMaxUses ||
			err == service.ErrUserAlreadyUsed {
			status = http.StatusForbidden
		} else if err == service.ErrNotAdmin {
			status = http.StatusForbidden
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"message": "промокод успешно применен"})
}

// Получение списка всех промокодов (доступно только администраторам)
func (h *Handler) GetAllPromoCodes(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(middleware.UserIDKey).(int64)
	isAdmin, err := h.Service.UserService.IsAdmin(r.Context(), userID)
	if err != nil || !isAdmin {
		http.Error(w, "доступ запрещен", http.StatusForbidden)
		return
	}

	promoCodes, err := h.Service.GetAllPromoCodes(r.Context(), userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(promoCodes)
}

// Получение конкретного промокода (доступно только администраторам)
func (h *Handler) GetPromoCode(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(middleware.UserIDKey).(int64)
	isAdmin, err := h.Service.UserService.IsAdmin(r.Context(), userID)
	if err != nil || !isAdmin {
		http.Error(w, "доступ запрещен", http.StatusForbidden)
		return
	}

	promoCodeIDStr := r.PathValue("id") // Используем PathValue для получения параметра из URL
	if promoCodeIDStr == "" {
		http.Error(w, "не указан ID промокода", http.StatusBadRequest)
		return
	}

	promoCodeID, err := strconv.ParseInt(promoCodeIDStr, 10, 64)
	if err != nil {
		http.Error(w, "некорректный ID промокода", http.StatusBadRequest)
		return
	}

	promoCode, err := h.Service.GetPromoCode(r.Context(), userID, promoCodeID)
	if err != nil {
		status := http.StatusInternalServerError
		if err == service.ErrNotAdmin {
			status = http.StatusForbidden
		} else if err == service.ErrPromoCodeNotFound {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(promoCode)
}

// Удаление промокода (доступно только администраторам)
func (h *Handler) DeletePromoCode(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(middleware.UserIDKey).(int64)
	isAdmin, err := h.Service.UserService.IsAdmin(r.Context(), userID)
	if err != nil || !isAdmin {
		http.Error(w, "доступ запрещен", http.StatusForbidden)
		return
	}

	promoCodeIDStr := r.PathValue("id") // Используем PathValue для получения параметра из URL
	if promoCodeIDStr == "" {
		http.Error(w, "не указан ID промокода", http.StatusBadRequest)
		return
	}

	promoCodeID, err := strconv.ParseInt(promoCodeIDStr, 10, 64)
	if err != nil {
		http.Error(w, "некорректный ID промокода", http.StatusBadRequest)
		return
	}

	err = h.Service.DeletePromoCode(r.Context(), userID, promoCodeID)
	if err != nil {
		status := http.StatusInternalServerError
		if err == service.ErrNotAdmin {
			status = http.StatusForbidden
		} else if err == service.ErrPromoCodeNotFound {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"message": "промокод успешно удален"})
}
