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
}

type PostgresSignalRepo struct {
	DB *sqlx.DB
}

func NewPostgresSignalRepo(db *sqlx.DB) *PostgresSignalRepo {
	return &PostgresSignalRepo{DB: db}
}

// GetActiveByUserID gets all active signals for a user
func (r *PostgresSignalRepo) GetActiveByUserID(ctx context.Context, userID int64) ([]*SignalDB, error) {
	query := `
		SELECT id, user_id, symbol, target_price, min_quantity, trigger_on_cancel, 
		       trigger_on_eat, eat_percentage, original_qty, last_qty, auto_close, 
		       close_market, watch_market, original_side, created_at, updated_at, is_active
		FROM signals
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
			&signal.ID, &signal.UserID, &signal.Symbol, &signal.TargetPrice, &signal.MinQuantity,
			&signal.TriggerOnCancel, &signal.TriggerOnEat, &signal.EatPercentage, &signal.OriginalQty,
			&signal.LastQty, &signal.AutoClose, &signal.CloseMarket, &signal.WatchMarket, &signal.OriginalSide,
			&signal.CreatedAt, &signal.UpdatedAt, &signal.IsActive,
		); err != nil {
			return nil, err
		}
		signals = append(signals, signal)
	}
	return signals, nil
}

// GetActiveByUserAndSymbol gets all active signals for a user and symbol
func (r *PostgresSignalRepo) GetActiveByUserAndSymbol(ctx context.Context, userID int64, symbol string) ([]*SignalDB, error) {
	query := `
		SELECT id, user_id, symbol, target_price, min_quantity, trigger_on_cancel, 
		       trigger_on_eat, eat_percentage, original_qty, last_qty, auto_close, 
		       close_market, watch_market, original_side, created_at, updated_at, is_active
		FROM signals
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
			&signal.ID, &signal.UserID, &signal.Symbol, &signal.TargetPrice, &signal.MinQuantity,
			&signal.TriggerOnCancel, &signal.TriggerOnEat, &signal.EatPercentage, &signal.OriginalQty,
			&signal.LastQty, &signal.AutoClose, &signal.CloseMarket, &signal.WatchMarket, &signal.OriginalSide,
			&signal.CreatedAt, &signal.UpdatedAt, &signal.IsActive,
		); err != nil {
			return nil, err
		}
		signals = append(signals, signal)
	}
	return signals, nil
}

// GetAllActiveSignals gets all active signals in the database
func (r *PostgresSignalRepo) GetAllActiveSignals(ctx context.Context) ([]*SignalDB, error) {
	query := `
		SELECT id, user_id, symbol, target_price, min_quantity, trigger_on_cancel, 
		       trigger_on_eat, eat_percentage, original_qty, last_qty, auto_close, 
		       close_market, watch_market, original_side, created_at, updated_at, is_active
		FROM signals
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
			&signal.ID, &signal.UserID, &signal.Symbol, &signal.TargetPrice, &signal.MinQuantity,
			&signal.TriggerOnCancel, &signal.TriggerOnEat, &signal.EatPercentage, &signal.OriginalQty,
			&signal.LastQty, &signal.AutoClose, &signal.CloseMarket, &signal.WatchMarket, &signal.OriginalSide,
			&signal.CreatedAt, &signal.UpdatedAt, &signal.IsActive,
		); err != nil {
			return nil, err
		}
		signals = append(signals, signal)
	}
	return signals, nil
}

// Save creates a new signal in the database
func (r *PostgresSignalRepo) Save(ctx context.Context, signal *SignalDB) error {
	query := `
		INSERT INTO signals (
			user_id, symbol, target_price, min_quantity, trigger_on_cancel, 
			trigger_on_eat, eat_percentage, original_qty, last_qty, auto_close, 
			close_market, watch_market, original_side, created_at, updated_at, is_active
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, NOW(), NOW(), $14)
		RETURNING id
	`
	return r.DB.QueryRowContext(ctx, query,
		signal.UserID, signal.Symbol, signal.TargetPrice, signal.MinQuantity,
		signal.TriggerOnCancel, signal.TriggerOnEat, signal.EatPercentage, signal.OriginalQty,
		signal.LastQty, signal.AutoClose, signal.CloseMarket, signal.WatchMarket,
		signal.OriginalSide, signal.IsActive,
	).Scan(&signal.ID)
}

// Update updates an existing signal
func (r *PostgresSignalRepo) Update(ctx context.Context, signal *SignalDB) error {
	query := `
		UPDATE signals
		SET last_qty = $1, is_active = $2, updated_at = NOW()
		WHERE id = $3
	`
	_, err := r.DB.ExecContext(ctx, query, signal.LastQty, signal.IsActive, signal.ID)
	return err
}

// Delete removes a signal from the database
func (r *PostgresSignalRepo) Delete(ctx context.Context, signalID int64) error {
	_, err := r.DB.ExecContext(ctx, "DELETE FROM signals WHERE id = $1", signalID)
	return err
}

// Deactivate marks a signal as inactive
func (r *PostgresSignalRepo) Deactivate(ctx context.Context, signalID int64) error {
	_, err := r.DB.ExecContext(ctx,
		`UPDATE signals SET is_active = false, updated_at = NOW() WHERE id = $1`,
		signalID)
	return err
}
