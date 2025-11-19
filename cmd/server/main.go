package main

import (
	"fmt"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"

	"sizehunt/internal/config"
	"sizehunt/internal/user/repository"
	"sizehunt/internal/user/service"
	userhttp "sizehunt/internal/user/transport/http" // алиас!
	"sizehunt/pkg/db"
	"sizehunt/pkg/middleware"
)

func main() {
	fmt.Println("SizeHunt API starting...")

	cfg := config.Load()
	fmt.Println("Config loaded")

	// Подключение к БД
	database, err := db.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Database connection failed: %v", err)
	}
	fmt.Println("Connected to PostgreSQL")

	// --- ИНИЦИАЛИЗАЦИЯ СЛОЁВ ---
	userRepo := repository.NewPostgresUserRepository(database)
	userService := service.NewUserService(userRepo)
	h := userhttp.NewHandler(userService, cfg.JWTSecret)

	// --- РОУТЕР ---
	r := chi.NewRouter()

	// Публичные роуты
	r.Post("/auth/register", h.Register)
	r.Post("/auth/login", h.Login)

	// 🔐 Защищённая группа маршрутов
	r.Group(func(pr chi.Router) {
		pr.Use(middleware.JWTAuth(cfg.JWTSecret))

		pr.Get("/auth/me", func(w http.ResponseWriter, r *http.Request) {
			id := r.Context().Value(middleware.UserIDKey).(int64)
			w.Write([]byte(fmt.Sprintf("Your user ID: %d", id)))
		})
	})

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("OK"))
	})

	log.Println("Server running on :8080")
	if err := http.ListenAndServe(":8080", r); err != nil {
		log.Fatal(err)
	}
}
