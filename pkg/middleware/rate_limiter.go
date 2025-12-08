package middleware

import (
	"log"
	"net/http"
	"sync"
	"time"
)

type RateLimiter struct {
	mu        sync.Mutex
	limit     int
	window    time.Duration
	requests  map[string]int
	lastReset time.Time
}

func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	limiter := &RateLimiter{
		limit:     limit,
		window:    window,
		requests:  make(map[string]int),
		lastReset: time.Now(),
	}

	// Запускаем горутину для очистки старых записей
	go limiter.cleanup()

	return limiter
}

func (r *RateLimiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		r.mu.Lock()
		currentTime := time.Now()

		// Удаляем записи старше 1 часа
		for ip, count := range r.requests {
			if count == 0 && currentTime.Sub(r.lastReset) > time.Hour {
				delete(r.requests, ip)
			}
		}

		r.mu.Unlock()
	}
}

func (r *RateLimiter) Allow(ip string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Сбрасываем счетчик при истечении окна
	if time.Since(r.lastReset) > r.window {
		r.requests = make(map[string]int)
		r.lastReset = time.Now()
	}

	// Увеличиваем счетчик для IP
	count := r.requests[ip]
	if count >= r.limit {
		return false
	}

	r.requests[ip] = count + 1
	return true
}

func (r *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ip := req.RemoteAddr
		if !r.Allow(ip) {
			http.Error(w, "Too many requests", http.StatusTooManyRequests)
			log.Printf("Rate limit exceeded for IP: %s", ip)
			return
		}

		next.ServeHTTP(w, req)
	})
}

// Базовый рейт-лимитер для всего сервера
var GlobalRateLimiter = NewRateLimiter(100, 1*time.Minute) // 100 запросов в минуту
