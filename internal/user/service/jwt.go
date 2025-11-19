package service

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
	"sizehunt/internal/token"
)

type JWTManager struct {
	SecretKey string
}

func NewJWTManager(secret string) *JWTManager {
	return &JWTManager{
		SecretKey: secret,
	}
}

func (j *JWTManager) Generate(userID int64, email string) (string, error) {
	claims := jwt.MapClaims{
		"user_id": userID,
		"email":   email,
		"exp":     time.Now().Add(15 * time.Minute).Unix(), // 15 минут
		"iat":     time.Now().Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	return token.SignedString([]byte(j.SecretKey))
}

// GeneratePair возвращает access и refresh токены
func (j *JWTManager) GeneratePair(userID int64, email string) (accessToken, refreshToken string, err error) {
	accessToken, err = j.Generate(userID, email)
	if err != nil {
		return "", "", err
	}

	refreshToken, err = token.GenerateToken(32)
	if err != nil {
		return "", "", err
	}

	return accessToken, refreshToken, nil
}
