package token

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"
)

var (
	ErrInvalidToken = errors.New("invalid token")
	ErrExpiredToken = errors.New("token expired")
)

func GenerateToken(length int) (string, error) {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func NewRefreshToken(userID int64) (*Token, error) {
	tokenStr, err := GenerateToken(32)
	if err != nil {
		return nil, err
	}

	return &Token{
		UserID:    userID,
		Token:     tokenStr,
		Type:      "refresh",
		ExpiresAt: time.Now().Add(7 * 24 * time.Hour), // 7 дней
	}, nil
}
