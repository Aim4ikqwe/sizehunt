package repository

import (
	"context"
	"database/sql"
)

// OKXAPIKeys — структура для хранения ключей пользователя OKX
type OKXAPIKeys struct {
	UserID     int64
	APIKey     string
	SecretKey  string
	Passphrase string
}

// KeysRepository — интерфейс для работы с ключами OKX
type KeysRepository interface {
	SaveKeys(userID int64, apiKey, secretKey, passphrase string) error
	GetKeys(userID int64) (*OKXAPIKeys, error)
	DeleteByUserID(ctx context.Context, userID int64) error
}

// PostgresKeysRepo — реализация KeysRepository для PostgreSQL
type PostgresKeysRepo struct {
	DB *sql.DB
}

// NewPostgresKeysRepo создает новый репозиторий для ключей OKX
func NewPostgresKeysRepo(db *sql.DB) *PostgresKeysRepo {
	return &PostgresKeysRepo{DB: db}
}

// SaveKeys сохраняет или обновляет ключи пользователя OKX
func (r *PostgresKeysRepo) SaveKeys(userID int64, apiKey, secretKey, passphrase string) error {
	_, err := r.DB.ExecContext(context.Background(),
		`INSERT INTO okx_keys(user_id, api_key, secret_key, passphrase) 
		 VALUES($1,$2,$3,$4)
		 ON CONFLICT (user_id) DO UPDATE 
		 SET api_key = $2, secret_key = $3, passphrase = $4`,
		userID, apiKey, secretKey, passphrase)
	return err
}

// GetKeys получает ключи пользователя OKX
func (r *PostgresKeysRepo) GetKeys(userID int64) (*OKXAPIKeys, error) {
	row := r.DB.QueryRowContext(context.Background(),
		`SELECT api_key, secret_key, passphrase FROM okx_keys WHERE user_id=$1`, userID)
	var apiKey, secretKey, passphrase string
	if err := row.Scan(&apiKey, &secretKey, &passphrase); err != nil {
		return nil, err
	}
	return &OKXAPIKeys{
		UserID:     userID,
		APIKey:     apiKey,
		SecretKey:  secretKey,
		Passphrase: passphrase,
	}, nil
}

// DeleteByUserID удаляет ключи пользователя OKX
func (r *PostgresKeysRepo) DeleteByUserID(ctx context.Context, userID int64) error {
	_, err := r.DB.ExecContext(ctx,
		`DELETE FROM okx_keys WHERE user_id = $1`,
		userID)
	return err
}
