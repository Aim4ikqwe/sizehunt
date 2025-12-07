package middleware

import (
	"net/http"
	"strconv"
	"time"

	"sizehunt/internal/metrics"

	"github.com/go-chi/chi/v5"
)

// MetricsMiddleware собирает метрики для HTTP запросов
func MetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Инкрементируем счетчик активных запросов
		metrics.HTTPRequestsInFlight.Inc()
		defer metrics.HTTPRequestsInFlight.Dec()

		start := time.Now()
		ww := newWrapResponseWriter(w, r.ProtoMajor)

		defer func() {
			duration := time.Since(start).Seconds()
			method := r.Method
			path := chi.RouteContext(r.Context()).RoutePattern()
			if path == "" {
				path = r.URL.Path
			}
			status := strconv.Itoa(ww.Status())

			// Обновляем метрики
			metrics.HTTPRequestsTotal.WithLabelValues(method, path, status).Inc()
			metrics.HTTPRequestDuration.WithLabelValues(method, path).Observe(duration)
		}()

		next.ServeHTTP(ww, r)
	})
}

// wrapResponseWriter для отслеживания статус кода
type wrapResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func newWrapResponseWriter(w http.ResponseWriter, protoMajor int) *wrapResponseWriter {
	return &wrapResponseWriter{ResponseWriter: w, status: http.StatusOK}
}

func (rw *wrapResponseWriter) Status() int {
	return rw.status
}

func (rw *wrapResponseWriter) WriteHeader(code int) {
	if rw.wroteHeader {
		return
	}
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
	rw.wroteHeader = true
}

func (rw *wrapResponseWriter) Write(b []byte) (int, error) {
	if !rw.wroteHeader {
		rw.WriteHeader(http.StatusOK)
	}
	return rw.ResponseWriter.Write(b)
}
