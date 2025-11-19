package main

import (
	"fmt"
	"log"
	"sizehunt/internal/binance/repository"
	"sizehunt/internal/binance/service"
	"sizehunt/pkg/db"
)

func main() {
	// Подключение к БД
	database, err := db.Connect("postgres://sizehunt:sizehunt@localhost:5433/sizehunt?sslmode=disable")
	if err != nil {
		log.Fatal(err)
	}

	// Шифруем ключи
	secret := "supersecretkey1234567890123456"
	encryptedKey, _ := service.EncryptAES("my_api_key", secret)
	encryptedSecret, _ := service.EncryptAES("my_secret_key", secret)

	// Сохраняем в БД
	repo := repository.NewPostgresKeysRepo(database)
	repo.SaveKeys(1, encryptedKey, encryptedSecret)

	// Получаем и расшифровываем
	keys, _ := repo.GetKeys(1)
	apiKey, _ := service.DecryptAES(keys.APIKey, secret)
	secretKey, _ := service.DecryptAES(keys.SecretKey, secret)

	fmt.Println("Decrypted APIKey:", apiKey)
	fmt.Println("Decrypted SecretKey:", secretKey)
}
