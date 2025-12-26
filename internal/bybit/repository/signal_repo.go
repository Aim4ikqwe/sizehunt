package repository

import (
	"context"

	"github.com/jmoiron/sqlx"
)

// SignalRepository interface that PostgresSignalRepo must implement
type SignalRepository interface {
	GetActiveByUserID(ctx context.Context, userID int64) ([]*SignalDB, error)
	GetActiveByUserAndSymbol(ctx context.Context, userID int64, symbol string) ([]*SignalDB, error)
	GetAllActiveSignals(ctx context.Context) ([]*SignalDB, error)
	Save(ctx context.Context, signal *SignalDB) error
	Update(ctx context.Context, signal *SignalDB) error
	Delete(ctx context.Context, signalID int64) error
	Deactivate(ctx context.Context, signalID int64) error
	GetByID(ctx context.Context, signalID int64) (*SignalDB, error)
	GetInactiveAutoCloseSignals(ctx context.Context) ([]*SignalDB, error)
}

// PostgresSignalRepo реализация SignalRepository для PostgreSQL
type PostgresSignalRepo struct {
	DB *sqlx.DB
}

// NewPostgresSignalRepo создает новый репозиторий сигналов Bybit
func NewPostgresSignalRepo(db *sqlx.DB) *PostgresSignalRepo {
	return &PostgresSignalRepo{DB: db}
}

// GetActiveByUserID получает все активные сигналы пользователя
func (r *PostgresSignalRepo) GetActiveByUserID(ctx context.Context, userID int64) ([]*SignalDB, error) {
	query := `
		SELECT id, user_id, symbol, category, target_price, min_quantity, trigger_on_cancel, 
		       trigger_on_eat, eat_percentage, original_qty, last_qty, auto_close, 
		       watch_category, close_category, original_side, created_at, updated_at, is_active
		FROM bybit_signals
		WHERE user_id = $1 AND is_active = true
	`
	rows, err := r.DB.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var signals []*SignalDB
	for rows.Next() {
		signal := &SignalDB{}
		if err := rows.Scan(
			&signal.ID, &signal.UserID, &signal.Symbol, &signal.Category, &signal.TargetPrice, &signal.MinQuantity,
			&signal.TriggerOnCancel, &signal.TriggerOnEat, &signal.EatPercentage, &signal.OriginalQty,
			&signal.LastQty, &signal.AutoClose, &signal.WatchCategory, &signal.CloseCategory,
			&signal.OriginalSide, &signal.CreatedAt, &signal.UpdatedAt, &signal.IsActive,
		); err != nil {
			return nil, err
		}
		signals = append(signals, signal)
	}
	return signals, nil
}

// GetActiveByUserAndSymbol получает все активные сигналы для пользователя и символа
func (r *PostgresSignalRepo) GetActiveByUserAndSymbol(ctx context.Context, userID int64, symbol string) ([]*SignalDB, error) {
	query := `
		SELECT id, user_id, symbol, category, target_price, min_quantity, trigger_on_cancel, 
		       trigger_on_eat, eat_percentage, original_qty, last_qty, auto_close, 
		       watch_category, close_category, original_side, created_at, updated_at, is_active
		FROM bybit_signals
		WHERE user_id = $1 AND symbol = $2 AND is_active = true
		ORDER BY created_at DESC
	`
	rows, err := r.DB.QueryContext(ctx, query, userID, symbol)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var signals []*SignalDB
	for rows.Next() {
		signal := &SignalDB{}
		if err := rows.Scan(
			&signal.ID, &signal.UserID, &signal.Symbol, &signal.Category, &signal.TargetPrice, &signal.MinQuantity,
			&signal.TriggerOnCancel, &signal.TriggerOnEat, &signal.EatPercentage, &signal.OriginalQty,
			&signal.LastQty, &signal.AutoClose, &signal.WatchCategory, &signal.CloseCategory,
			&signal.OriginalSide, &signal.CreatedAt, &signal.UpdatedAt, &signal.IsActive,
		); err != nil {
			return nil, err
		}
		signals = append(signals, signal)
	}
	return signals, nil
}

// GetAllActiveSignals получает все активные сигналы в базе данных
func (r *PostgresSignalRepo) GetAllActiveSignals(ctx context.Context) ([]*SignalDB, error) {
	query := `
		SELECT id, user_id, symbol, category, target_price, min_quantity, trigger_on_cancel, 
		       trigger_on_eat, eat_percentage, original_qty, last_qty, auto_close, 
		       watch_category, close_category, original_side, created_at, updated_at, is_active
		FROM bybit_signals
		WHERE is_active = true
	`
	rows, err := r.DB.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var signals []*SignalDB
	for rows.Next() {
		signal := &SignalDB{}
		if err := rows.Scan(
			&signal.ID, &signal.UserID, &signal.Symbol, &signal.Category, &signal.TargetPrice, &signal.MinQuantity,
			&signal.TriggerOnCancel, &signal.TriggerOnEat, &signal.EatPercentage, &signal.OriginalQty,
			&signal.LastQty, &signal.AutoClose, &signal.WatchCategory, &signal.CloseCategory,
			&signal.OriginalSide, &signal.CreatedAt, &signal.UpdatedAt, &signal.IsActive,
		); err != nil {
			return nil, err
		}
		signals = append(signals, signal)
	}
	return signals, nil
}

// Save создает новый сигнал в базе данных
func (r *PostgresSignalRepo) Save(ctx context.Context, signal *SignalDB) error {
	query := `
		INSERT INTO bybit_signals (
			user_id, symbol, category, target_price, min_quantity, trigger_on_cancel, 
			trigger_on_eat, eat_percentage, original_qty, last_qty, auto_close, 
			watch_category, close_category, original_side, created_at, updated_at, is_active
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, NOW(), NOW(), $15)
		RETURNING id
	`
	return r.DB.QueryRowContext(ctx, query,
		signal.UserID, signal.Symbol, signal.Category, signal.TargetPrice, signal.MinQuantity,
		signal.TriggerOnCancel, signal.TriggerOnEat, signal.EatPercentage, signal.OriginalQty,
		signal.LastQty, signal.AutoClose, signal.WatchCategory, signal.CloseCategory,
		signal.OriginalSide, signal.IsActive,
	).Scan(&signal.ID)
}

// Update обновляет существующий сигнал
func (r *PostgresSignalRepo) Update(ctx context.Context, signal *SignalDB) error {
	query := `
		UPDATE bybit_signals
		SET last_qty = $1, is_active = $2, updated_at = NOW()
		WHERE id = $3
	`
	_, err := r.DB.ExecContext(ctx, query, signal.LastQty, signal.IsActive, signal.ID)
	return err
}

// Delete удаляет сигнал из базы данных
func (r *PostgresSignalRepo) Delete(ctx context.Context, signalID int64) error {
	_, err := r.DB.ExecContext(ctx, "DELETE FROM bybit_signals WHERE id = $1", signalID)
	return err
}

// Deactivate помечает сигнал как неактивный
func (r *PostgresSignalRepo) Deactivate(ctx context.Context, signalID int64) error {
	_, err := r.DB.ExecContext(ctx,
		`UPDATE bybit_signals SET is_active = false, updated_at = NOW() WHERE id = $1`,
		signalID)
	return err
}

// GetByID получает сигнал по ID
func (r *PostgresSignalRepo) GetByID(ctx context.Context, signalID int64) (*SignalDB, error) {
	query := `
        SELECT id, user_id, symbol, category, target_price, min_quantity, trigger_on_cancel, 
               trigger_on_eat, eat_percentage, original_qty, last_qty, auto_close, 
               watch_category, close_category, original_side, created_at, updated_at, is_active
        FROM bybit_signals
        WHERE id = $1
    `
	signal := &SignalDB{}
	err := r.DB.QueryRowContext(ctx, query, signalID).Scan(
		&signal.ID, &signal.UserID, &signal.Symbol, &signal.Category, &signal.TargetPrice, &signal.MinQuantity,
		&signal.TriggerOnCancel, &signal.TriggerOnEat, &signal.EatPercentage, &signal.OriginalQty,
		&signal.LastQty, &signal.AutoClose, &signal.WatchCategory, &signal.CloseCategory,
		&signal.OriginalSide, &signal.CreatedAt, &signal.UpdatedAt, &signal.IsActive,
	)
	if err != nil {
		return nil, err
	}
	return signal, nil
}

// GetInactiveAutoCloseSignals получает неактивные сигналы с auto_close
func (r *PostgresSignalRepo) GetInactiveAutoCloseSignals(ctx context.Context) ([]*SignalDB, error) {
	query := `
        SELECT id, user_id, symbol, category, target_price, min_quantity, trigger_on_cancel, 
               trigger_on_eat, eat_percentage, original_qty, last_qty, auto_close, 
               watch_category, close_category, original_side, created_at, updated_at, is_active
        FROM bybit_signals
        WHERE is_active = false AND auto_close = true
        ORDER BY created_at DESC
    `
	rows, err := r.DB.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var signals []*SignalDB
	for rows.Next() {
		signal := &SignalDB{}
		if err := rows.Scan(
			&signal.ID, &signal.UserID, &signal.Symbol, &signal.Category, &signal.TargetPrice, &signal.MinQuantity,
			&signal.TriggerOnCancel, &signal.TriggerOnEat, &signal.EatPercentage, &signal.OriginalQty,
			&signal.LastQty, &signal.AutoClose, &signal.WatchCategory, &signal.CloseCategory,
			&signal.OriginalSide, &signal.CreatedAt, &signal.UpdatedAt, &signal.IsActive,
		); err != nil {
			return nil, err
		}
		signals = append(signals, signal)
	}
	return signals, nil
}
