// cmd/server/main.go
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	repository2 "sizehunt/internal/proxy/repository"
	"sizehunt/internal/proxy/service"
	http2 "sizehunt/internal/proxy/transport/http"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/cors"
	"github.com/jmoiron/sqlx"

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

func initProxyContainers(proxyService *service.ProxyService, database *sql.DB) {
	ctx := context.Background()
	log.Println("Initializing proxy containers for users with active signals...")

	// Получаем всех пользователей с активными сигналами
	query := `
		SELECT DISTINCT user_id 
		FROM signals 
		WHERE is_active = true`
	rows, err := database.QueryContext(ctx, query)
	if err != nil {
		log.Printf("Failed to get users with active signals: %v", err)
		return
	}
	defer rows.Close()

	var userIds []int64
	for rows.Next() {
		var userID int64
		if err := rows.Scan(&userID); err != nil {
			log.Printf("Error scanning user ID: %v", err)
			continue
		}
		userIds = append(userIds, userID)
	}

	log.Printf("Found %d users with active signals", len(userIds))

	// Запускаем прокси для каждого пользователя
	for _, userID := range userIds {
		go func(uid int64) {
			// Проверяем, есть ли у пользователя настройки прокси
			_, err := proxyService.Repo.GetProxyConfig(ctx, uid)
			if err != nil {
				log.Printf("User %d has no proxy config, skipping", uid)
				return
			}

			// Запускаем прокси контейнер
			if err := proxyService.StartProxyForUser(ctx, uid); err != nil {
				log.Printf("Failed to start proxy for user %d: %v", uid, err)
			} else {
				log.Printf("Proxy container started successfully for user %d", uid)
			}
		}(userID)
	}

	// Даем время на запуск контейнеров
	time.Sleep(2 * time.Second)
	log.Println("Proxy container initialization completed")
}

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
	// Proxy
	proxyRepo := repository2.NewPostgresProxyRepo(database)
	proxyService := service.NewProxyService(proxyRepo)
	proxyHandler := http2.NewProxyHandler(proxyService)
	initProxyContainers(proxyService, database)
	// Binance
	keysRepo := repository.NewPostgresKeysRepo(database)
	dbx := sqlx.NewDb(database, "postgres") // Конвертируем *sql.DB в *sqlx.DB
	signalRepo := repository.NewPostgresSignalRepo(dbx)
	binanceClient := binance_service.NewBinanceHTTPClient("", "") // Инициализируем без ключей
	binanceWatcher := binance_service.NewWatcher(binanceClient)
	subRepo := subscriptionrepository.NewSubscriptionRepository(database)
	subService := subscriptionservice.NewService(subRepo)

	wsManager := binance_service.NewWebSocketManager(
		context.Background(),
		subService,
		keysRepo,
		signalRepo,
		cfg,
		proxyService,
	)
	go wsManager.StartConnectionMonitor()
	log.Println("Loading active signals from database...")
	if err := wsManager.LoadActiveSignals(); err != nil {
		log.Printf("Failed to load active signals: %v", err)
	} else {
		log.Println("Active signals loaded successfully")
	}

	// --- Создаем сервер ДО инициализации обработчиков ---
	server := &http.Server{
		Addr: ":8080",
	}

	// Передаем сервер в обработчик Binance
	binanceHandler := binancehttp.NewBinanceHandler(binanceWatcher, keysRepo, cfg, wsManager, subService, server, signalRepo, proxyService)
	subHandler := subscriptionhttp.NewSubscriptionHandler(subService)
	log.Println("Loading active signals from database...")
	if err := wsManager.LoadActiveSignals(); err != nil {
		log.Printf("Failed to load active signals: %v", err)
	} else {
		log.Println("Active signals loaded successfully")
	}
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
	// Добавить новый эндпоинт для проверки состояния сети
	r.Get("/health/network", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		client := http.Client{}
		req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.binance.com/api/v3/ping", nil)
		resp, err := client.Do(req)

		status := make(map[string]interface{})
		status["timestamp"] = time.Now()

		if err != nil {
			status["status"] = "down"
			status["error"] = err.Error()
			w.WriteHeader(http.StatusServiceUnavailable)
		} else if resp.StatusCode != http.StatusOK {
			status["status"] = "down"
			status["http_status"] = resp.StatusCode
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			status["status"] = "up"
			status["latency_ms"] = time.Since(time.Now()).Milliseconds()
			w.WriteHeader(http.StatusOK)
		}

		if resp != nil {
			resp.Body.Close()
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(status)
	})

	// 🔐 Защищённая группа маршрутов
	r.Group(func(pr chi.Router) {
		pr.Use(middleware.JWTAuth(cfg.JWTSecret))
		pr.Get("/auth/me", func(w http.ResponseWriter, r *http.Request) {
			id := r.Context().Value(middleware.UserIDKey).(int64)
			w.Write([]byte(fmt.Sprintf("Your user ID: %d", id)))
		})

		// Proxy routes - добавляем эти маршруты
		pr.Post("/api/proxy", proxyHandler.SaveProxyConfig)
		pr.Delete("/api/proxy", proxyHandler.DeleteProxyConfig)
		pr.Get("/api/proxy/status", proxyHandler.GetProxyStatus)

		// Binance routes
		pr.Get("/api/binance/book", binanceHandler.GetOrderBook)
		pr.Get("/api/binance/order-at-price", binanceHandler.GetOrderAtPrice)
		pr.Post("/api/binance/keys", binanceHandler.SaveKeys)
		pr.Delete("/api/binance/keys", binanceHandler.DeleteKeys)
		pr.Post("/api/binance/signal", binanceHandler.CreateSignal)
		pr.Get("/api/binance/signals", binanceHandler.GetSignals)
		pr.Delete("/api/binance/signals/{id}", binanceHandler.DeleteSignal)
		pr.Post("/api/binance/graceful-shutdown", binanceHandler.GracefulShutdown)

		// Payment routes
		pr.Post("/api/payment/create", subHandler.CreatePayment)
		pr.Post("/api/payment/webhook", subHandler.Webhook)
	})

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("OK"))
	})

	// Устанавливаем обработчик для сервера
	server.Handler = r

	log.Println("Server running on :8080")

	// Graceful shutdown на сигналы ОС
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig

		log.Println("Shutdown signal received, starting graceful shutdown")
		shutdownServer(server)
	}()

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func shutdownServer(server *http.Server) {
	log.Println("Starting server shutdown process")

	// Создаем контекст с таймаутом
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Printf("Server shutdown failed: %v", err)
	}

	log.Println("Server stopped")
}
