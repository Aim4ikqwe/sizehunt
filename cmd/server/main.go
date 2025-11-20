// cmd/server/main.go
package main

import (
	"context"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/cors"
	"log"
	"net/http"
	"sizehunt/internal/binance/repository"
	binance_service "sizehunt/internal/binance/service"
	binancehttp "sizehunt/internal/binance/transport/http"
	"sizehunt/internal/config"
	subscriptionrepository "sizehunt/internal/subscription/repository"
	subscriptionservice "sizehunt/internal/subscription/service"
	subscriptionhttp "sizehunt/internal/subscription/transport/http"
	tokenrepository "sizehunt/internal/token/repository"
	userrepository "sizehunt/internal/user/repository"
	userservice "sizehunt/internal/user/service"
	userhttp "sizehunt/internal/user/transport/http"
	"sizehunt/pkg/db"
	"sizehunt/pkg/middleware"
)

func main() {
	fmt.Println("SizeHunt API starting...")
	cfg := config.Load()
	fmt.Println("Config loaded")

	database, err := db.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Database connection failed: %v", err)
	}
	log.Println("Database connected")
	fmt.Println("Connected to PostgreSQL")

	// --- ИНИЦИАЛИЗАЦИЯ СЛОЁВ ---
	userRepo := userrepository.NewPostgresUserRepository(database)
	userService := userservice.NewUserService(userRepo)
	refreshTokenRepo := tokenrepository.NewRefreshTokenRepository(database)
	h := userhttp.NewHandler(userService, cfg.JWTSecret, refreshTokenRepo)

	// Binance
	keysRepo := repository.NewPostgresKeysRepo(database)
	binanceClient := binance_service.NewBinanceHTTPClient("", "") // Инициализируем без ключей
	binanceWatcher := binance_service.NewWatcher(binanceClient)

	subRepo := subscriptionrepository.NewSubscriptionRepository(database)
	subService := subscriptionservice.NewService(subRepo)

	wsManager := binance_service.NewWebSocketManager(
		context.Background(),
		subService,
		keysRepo,
		cfg,
	)

	binanceHandler := binancehttp.NewBinanceHandler(binanceWatcher, keysRepo, cfg, wsManager, subService)
	subHandler := subscriptionhttp.NewSubscriptionHandler(subService)

	// --- РОУТЕР ---
	r := chi.NewRouter()

	// CORS
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"https://localhost:3000", "http://localhost:3000", "http://localhost:5173"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	// Публичные роуты
	r.Post("/auth/register", h.Register)
	r.Post("/auth/login", h.Login)
	r.Post("/auth/refresh", h.Refresh)

	// 🔐 Защищённая группа маршрутов
	r.Group(func(pr chi.Router) {
		pr.Use(middleware.JWTAuth(cfg.JWTSecret))
		pr.Get("/auth/me", func(w http.ResponseWriter, r *http.Request) {
			id := r.Context().Value(middleware.UserIDKey).(int64)
			w.Write([]byte(fmt.Sprintf("Your user ID: %d", id)))
		})

		// Binance routes
		pr.Get("/api/binance/book", binanceHandler.GetOrderBook)
		pr.Get("/api/binance/order-at-price", binanceHandler.GetOrderAtPrice) // добавь
		pr.Post("/api/binance/keys", binanceHandler.SaveKeys)
		pr.Delete("/api/binance/keys", binanceHandler.DeleteKeys)
		pr.Post("/api/binance/signal", binanceHandler.CreateSignal)

		// Payment routes
		pr.Post("/api/payment/create", subHandler.CreatePayment)
		pr.Post("/api/payment/webhook", subHandler.Webhook)
	})

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("OK"))
	})

	log.Println("Server running on :8080")
	if err := http.ListenAndServe(":8080", r); err != nil {
		log.Fatal(err)
	}
}
