package http

import (
	"encoding/json"
	"net/http"

	"sizehunt/internal/subscription/service"
	"sizehunt/pkg/middleware"
)

type Handler struct {
	SubscriptionService *service.Service
}

func NewSubscriptionHandler(ss *service.Service) *Handler {
	return &Handler{SubscriptionService: ss}
}

func (h *Handler) CreatePayment(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(middleware.UserIDKey).(int64)

	txID, err := h.SubscriptionService.CreatePayment(r.Context(), userID)
	if err != nil {
		http.Error(w, "failed to create payment", http.StatusInternalServerError)
		return
	}

	resp := map[string]string{
		"payment_id": txID,
		"message":    "Payment created. Wait for confirmation.",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *Handler) Webhook(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TransactionID string `json:"transaction_id"`
		Status        string `json:"status"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if body.Status == "confirmed" {
		if err := h.SubscriptionService.HandlePaymentWebhook(r.Context(), body.TransactionID); err != nil {
			http.Error(w, "failed to activate", http.StatusInternalServerError)
			return
		}
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}
