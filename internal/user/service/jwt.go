package service

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type JWTManager struct {
	SecretKey string
}

func NewJWTManager(secret string) *JWTManager {
	return &JWTManager{
		SecretKey: secret,
	}
}

func (j *JWTManager) Generate(userID int, email string) (string, error) {
	claims := jwt.MapClaims{
		"user_id": userID,
		"email":   email,
		"exp":     time.Now().Add(30 * 24 * time.Hour).Unix(), // 30 дней
		"iat":     time.Now().Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	return token.SignedString([]byte(j.SecretKey))
}
