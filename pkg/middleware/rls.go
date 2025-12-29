package middleware

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
)

// DBRlsMiddleware устанавливает ID пользователя в сессии Postgres для работы RLS.
// ВАЖНО: Для надежной работы RLS запросы в обработчиках должны выполняться
// в рамках одной транзакции (BEGIN; SET LOCAL ...; SELECT ...; COMMIT;).
func DBRlsMiddleware(db *sql.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userID, ok := r.Context().Value(UserIDKey).(int64)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}

			// Устанавливаем ID пользователя для текущей сессии
			_, err := db.ExecContext(r.Context(), fmt.Sprintf("SET app.user_id = '%d'", userID))
			if err != nil {
				// В продакшене лучше логировать и возвращать 500
				http.Error(w, "Failed to set security context", http.StatusInternalServerError)
				return
			}

			next.ServeHTTP(w, r)

			// Сбрасываем ID после завершения запроса (опционально, для безопасности пула)
			_, _ = db.ExecContext(context.Background(), "RESET app.user_id")
		})
	}
}
