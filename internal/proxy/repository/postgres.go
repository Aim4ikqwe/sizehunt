package repository

import (
	"context"
	"database/sql"
	"sizehunt/internal/proxy"
)

type ProxyRepository interface {
	SaveProxyConfig(ctx context.Context, userID int64, ssAddr string, ssPort int, ssMethod, ssPassword string) (int, error)
	GetProxyConfig(ctx context.Context, userID int64) (*proxy.ProxyConfig, error)
	UpdateStatus(ctx context.Context, proxyID int64, status string) error
	DeleteProxyConfig(ctx context.Context, userID int64) error
	HasActiveProxy(ctx context.Context, userID int64) (bool, error)
}

type PostgresProxyRepo struct {
	DB *sql.DB
}

func NewPostgresProxyRepo(db *sql.DB) *PostgresProxyRepo {
	return &PostgresProxyRepo{DB: db}
}

// SaveProxyConfig сохраняет или обновляет конфигурацию прокси для пользователя
func (r *PostgresProxyRepo) SaveProxyConfig(ctx context.Context, userID int64, ssAddr string, ssPort int, ssMethod, ssPassword string) (int, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	// Сначала помечаем все существующие конфигурации как "deleted"
	_, err = tx.ExecContext(ctx,
		`UPDATE proxy_configs SET status = 'deleted' WHERE user_id = $1 AND status != 'deleted'`,
		userID)
	if err != nil {
		return 0, err
	}

	// Создаем новую конфигурацию
	var localPort int
	var proxyID int64
	err = tx.QueryRowContext(ctx,
		`INSERT INTO proxy_configs (user_id, ss_addr, ss_port, ss_method, ss_password, local_port, status) 
		 VALUES ($1, $2, $3, $4, $5, 
			(SELECT COALESCE(MAX(local_port), 10000) + 1 FROM proxy_configs),
			'stopped')
		 RETURNING id, local_port`,
		userID, ssAddr, ssPort, ssMethod, ssPassword,
	).Scan(&proxyID, &localPort)
	if err != nil {
		return 0, err
	}

	return localPort, tx.Commit()
}

func (r *PostgresProxyRepo) GetProxyConfig(ctx context.Context, userID int64) (*proxy.ProxyConfig, error) {
	config := &proxy.ProxyConfig{}
	err := r.DB.QueryRowContext(ctx,
		`SELECT id, ss_addr, ss_port, ss_method, ss_password, local_port, status
		 FROM proxy_configs WHERE user_id = $1 AND status != 'deleted' ORDER BY created_at DESC LIMIT 1`,
		userID,
	).Scan(&config.ID, &config.SSAddr, &config.SSPort, &config.SSMethod, &config.SSPassword, &config.LocalPort, &config.Status)
	if err != nil {
		return nil, err
	}
	config.UserID = userID
	return config, nil
}

func (r *PostgresProxyRepo) UpdateStatus(ctx context.Context, proxyID int64, status string) error {
	_, err := r.DB.ExecContext(ctx,
		`UPDATE proxy_configs SET status = $1, updated_at = NOW() WHERE id = $2`,
		status, proxyID,
	)
	return err
}

func (r *PostgresProxyRepo) DeleteProxyConfig(ctx context.Context, userID int64) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Сначала останавливаем прокси в коде (это будет вызвано из сервиса)
	// Затем помечаем конфигурацию как "deleted"
	_, err = tx.ExecContext(ctx,
		`UPDATE proxy_configs SET status = 'deleted' WHERE user_id = $1 AND status != 'deleted'`,
		userID,
	)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// HasActiveProxy проверяет наличие активного прокси для пользователя
func (r *PostgresProxyRepo) HasActiveProxy(ctx context.Context, userID int64) (bool, error) {
	var count int
	err := r.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM proxy_configs WHERE user_id = $1 AND status = 'running'`,
		userID).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}
