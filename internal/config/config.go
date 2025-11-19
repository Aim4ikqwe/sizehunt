package config

import (
	"log"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	DatabaseURL string
	JWTSecret   string
}

func Load() *Config {
	err := godotenv.Load()
	if err != nil {
		log.Println("Warning: .env file not found")
	}

	cfg := &Config{
		DatabaseURL: os.Getenv("DATABASE_URL"),
		JWTSecret:   os.Getenv("JWT_SECRET"),
	}

	return cfg
}
