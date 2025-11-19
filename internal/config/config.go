package config

import (
	"log"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	DatabaseURL      string
	JWTSecret        string
	EncryptionSecret string
}

func Load() *Config {
	err := godotenv.Load()
	if err != nil {
		log.Println("Warning: .env file not found")
	}

	cfg := &Config{
		DatabaseURL:      os.Getenv("DATABASE_URL"),
		JWTSecret:        os.Getenv("JWT_SECRET"),
		EncryptionSecret: os.Getenv("ENCRYPTION_SECRET"),
	}

	return cfg
}
