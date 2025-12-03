// pkg/middleware/validation.go

package middleware

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

// ErrorResponse стандартный формат для ошибок валидации
type ErrorResponse struct {
	Error string      `json:"error"`
	Field string      `json:"field,omitempty"`
	Value interface{} `json:"value,omitempty"`
}

// ValidateRequest проверяет корректность запроса перед передачей его обработчику
func ValidateRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Проверка Content-Type для POST/PUT запросов
		if r.Method == http.MethodPost || r.Method == http.MethodPut {
			contentType := r.Header.Get("Content-Type")
			if contentType != "" && !strings.Contains(contentType, "application/json") {
				errResp := ErrorResponse{Error: "Invalid Content-Type, expected application/json"}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(errResp)
				return
			}

			// Проверка на пустое тело запроса для POST/PUT
			if r.ContentLength == 0 {
				errResp := ErrorResponse{Error: "Request body cannot be empty"}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(errResp)
				return
			}
		}

		// Максимальный размер тела запроса (10MB)
		const maxSize = 10 << 20 // 10 MB
		r.Body = http.MaxBytesReader(w, r.Body, maxSize)

		next.ServeHTTP(w, r)
	})
}

// HandleValidationError обрабатывает ошибки валидации и формирует ответ
func HandleValidationError(w http.ResponseWriter, err error, field, value string) {
	log.Printf("Validation error: %v", err)

	resp := ErrorResponse{
		Error: err.Error(),
		Field: field,
		Value: value,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	json.NewEncoder(w).Encode(resp)
}
