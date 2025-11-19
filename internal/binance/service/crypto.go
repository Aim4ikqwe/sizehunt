package service

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"io"
)

// EncryptAES шифрует текст с помощью ключа
func EncryptAES(plaintext, key string) (string, error) {
	block, err := aes.NewCipher([]byte(key)[:32]) // AES-256
	if err != nil {
		return "", err
	}

	b := []byte(plaintext)
	ciphertext := make([]byte, aes.BlockSize+len(b))
	iv := ciphertext[:aes.BlockSize]
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return "", err
	}

	stream := cipher.NewCFBEncrypter(block, iv)
	stream.XORKeyStream(ciphertext[aes.BlockSize:], b)

	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// DecryptAES расшифровывает текст с помощью ключа
func DecryptAES(ciphertextBase64, key string) (string, error) {
	ciphertext, _ := base64.StdEncoding.DecodeString(ciphertextBase64)

	block, err := aes.NewCipher([]byte(key)[:32])
	if err != nil {
		return "", err
	}

	if len(ciphertext) < aes.BlockSize {
		return "", err
	}
	iv := ciphertext[:aes.BlockSize]
	ciphertext = ciphertext[aes.BlockSize:]

	stream := cipher.NewCFBDecrypter(block, iv)
	stream.XORKeyStream(ciphertext, ciphertext)

	return string(ciphertext), nil
}
