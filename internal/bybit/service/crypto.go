package service

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
)

// EncryptAES шифрует данные с использованием AES-GCM
func EncryptAES(plainText, secret string) (string, error) {
	key := []byte(secret)
	if len(key) != 32 {
		// Дополняем или обрезаем ключ до 32 байт
		newKey := make([]byte, 32)
		copy(newKey, key)
		key = newKey
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	cipherText := gcm.Seal(nonce, nonce, []byte(plainText), nil)
	return base64.StdEncoding.EncodeToString(cipherText), nil
}

// DecryptAES расшифровывает данные с использованием AES-GCM
func DecryptAES(cipherText, secret string) (string, error) {
	key := []byte(secret)
	if len(key) != 32 {
		newKey := make([]byte, 32)
		copy(newKey, key)
		key = newKey
	}

	data, err := base64.StdEncoding.DecodeString(cipherText)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("ciphertext too short")
	}

	nonce, cipherTextBytes := data[:nonceSize], data[nonceSize:]
	plainText, err := gcm.Open(nil, nonce, cipherTextBytes, nil)
	if err != nil {
		return "", err
	}

	return string(plainText), nil
}
