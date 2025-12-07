// pkg/middleware/auth.go
package middleware

import (
	"encoding/base64"
	"net/http"
	"strings"
)

// BasicAuth возвращает middleware для базовой аутентификации
func BasicAuth(username, password string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("WWW-Authenticate", `Basic realm="metrics"`)

			// Получаем учетные данные из заголовка
			auth := r.Header.Get("Authorization")
			if auth == "" {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			// Парсим учетные данные
			parts := strings.SplitN(auth, " ", 2)
			if len(parts) != 2 || parts[0] != "Basic" {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			// Декодируем
			payload, err := base64.StdEncoding.DecodeString(parts[1])
			if err != nil {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			pair := strings.SplitN(string(payload), ":", 2)
			if len(pair) != 2 {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			// Проверяем учетные данные с константным временем сравнения
			if !constantTimeCompare(pair[0], username) || !constantTimeCompare(pair[1], password) {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			// Если аутентификация успешна, передаем управление следующему обработчику
			next.ServeHTTP(w, r)
		})
	}
}

// constantTimeCompare сравнивает две строки за константное время для предотвращения атак по времени
func constantTimeCompare(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	result := 0
	for i := range a {
		result |= int(a[i] ^ b[i])
	}
	return result == 0
}
