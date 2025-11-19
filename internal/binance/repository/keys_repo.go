package repository

import (
	"context"
	"database/sql"
)

// APIKeys — структура для хранения ключей пользователя
type APIKeys struct {
	UserID    int64
	APIKey    string
	SecretKey string
}

// KeysRepository — интерфейс для работы с ключами
type KeysRepository interface {
	SaveKeys(userID int64, apiKey, secretKey string) error
	GetKeys(userID int64) (*APIKeys, error)
	DeleteByUserID(ctx context.Context, userID int64) error
}

// PostgresKeysRepo — реализация KeysRepository для PostgreSQL
type PostgresKeysRepo struct {
	DB *sql.DB
}

// Конструктор
func NewPostgresKeysRepo(db *sql.DB) *PostgresKeysRepo {
	return &PostgresKeysRepo{DB: db}
}

// SaveKeys сохраняет или обновляет ключи пользователя
func (r *PostgresKeysRepo) SaveKeys(userID int64, apiKey, secretKey string) error {
	_, err := r.DB.ExecContext(context.Background(),
		`INSERT INTO binance_keys(user_id, api_key, secret_key) 
		 VALUES($1,$2,$3)
		 ON CONFLICT (user_id) DO UPDATE 
		 SET api_key = $2, secret_key = $3`,
		userID, apiKey, secretKey)
	return err
}

// GetKeys получает ключи пользователя
func (r *PostgresKeysRepo) GetKeys(userID int64) (*APIKeys, error) {
	row := r.DB.QueryRowContext(context.Background(),
		`SELECT api_key, secret_key FROM binance_keys WHERE user_id=$1`, userID)
	var apiKey, secretKey string
	if err := row.Scan(&apiKey, &secretKey); err != nil {
		return nil, err
	}
	return &APIKeys{
		UserID:    userID,
		APIKey:    apiKey,
		SecretKey: secretKey,
	}, nil
}

// Добавь метод в PostgresKeysRepo:
func (r *PostgresKeysRepo) DeleteByUserID(ctx context.Context, userID int64) error {
	_, err := r.DB.ExecContext(ctx,
		`DELETE FROM binance_keys WHERE user_id = $1`,
		userID)
	return err
}
