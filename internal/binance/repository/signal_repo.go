package repository

import (
	"context"
	"time"

	"github.com/jmoiron/sqlx"
)

type SignalRepository interface {
	Save(ctx context.Context, signal *SignalDB) error
	Update(ctx context.Context, signal *SignalDB) error
	GetActiveByUserID(ctx context.Context, userID int64) ([]*SignalDB, error)
	GetByID(ctx context.Context, id int64) (*SignalDB, error)
	Delete(ctx context.Context, id int64) error
}

type PostgresSignalRepo struct {
	DB *sqlx.DB
}

func NewPostgresSignalRepo(db *sqlx.DB) *PostgresSignalRepo {
	return &PostgresSignalRepo{DB: db}
}

func (r *PostgresSignalRepo) Save(ctx context.Context, signal *SignalDB) error {
	signal.CreatedAt = time.Now()
	signal.UpdatedAt = time.Now()
	signal.IsActive = true

	query := `
	INSERT INTO signals (
		user_id, symbol, target_price, min_quantity, trigger_on_cancel, 
		trigger_on_eat, eat_percentage, original_qty, last_qty, auto_close,
		close_market, watch_market, original_side, created_at, updated_at, is_active
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
	RETURNING id`

	return r.DB.QueryRowContext(
		ctx,
		query,
		signal.UserID,
		signal.Symbol,
		signal.TargetPrice,
		signal.MinQuantity,
		signal.TriggerOnCancel,
		signal.TriggerOnEat,
		signal.EatPercentage,
		signal.OriginalQty,
		signal.LastQty,
		signal.AutoClose,
		signal.CloseMarket,
		signal.WatchMarket,
		signal.OriginalSide,
		signal.CreatedAt,
		signal.UpdatedAt,
		signal.IsActive,
	).Scan(&signal.ID)
}

func (r *PostgresSignalRepo) Update(ctx context.Context, signal *SignalDB) error {
	signal.UpdatedAt = time.Now()

	query := `
	UPDATE signals 
	SET last_qty = $2, updated_at = $3, is_active = $4
	WHERE id = $1`

	_, err := r.DB.ExecContext(
		ctx,
		query,
		signal.ID,
		signal.LastQty,
		time.Now(),
		signal.IsActive,
	)
	return err
}

func (r *PostgresSignalRepo) GetActiveByUserID(ctx context.Context, userID int64) ([]*SignalDB, error) {
	query := `
	SELECT id, user_id, symbol, target_price, min_quantity, trigger_on_cancel,
		trigger_on_eat, eat_percentage, original_qty, last_qty, auto_close,
		close_market, watch_market, original_side, created_at, updated_at
	FROM signals
	WHERE user_id = $1 AND is_active = true`

	rows, err := r.DB.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var signals []*SignalDB
	for rows.Next() {
		signal := &SignalDB{}
		if err := rows.Scan(
			&signal.ID,
			&signal.UserID,
			&signal.Symbol,
			&signal.TargetPrice,
			&signal.MinQuantity,
			&signal.TriggerOnCancel,
			&signal.TriggerOnEat,
			&signal.EatPercentage,
			&signal.OriginalQty,
			&signal.LastQty,
			&signal.AutoClose,
			&signal.CloseMarket,
			&signal.WatchMarket,
			&signal.OriginalSide,
			&signal.CreatedAt,
			&signal.UpdatedAt,
		); err != nil {
			return nil, err
		}
		signal.IsActive = true
		signals = append(signals, signal)
	}

	return signals, rows.Err()
}

func (r *PostgresSignalRepo) GetByID(ctx context.Context, id int64) (*SignalDB, error) {
	query := `
	SELECT id, user_id, symbol, target_price, min_quantity, trigger_on_cancel,
		trigger_on_eat, eat_percentage, original_qty, last_qty, auto_close,
		close_market, watch_market, original_side, created_at, updated_at, is_active
	FROM signals WHERE id = $1`

	signal := &SignalDB{}
	err := r.DB.QueryRowContext(ctx, query, id).Scan(
		&signal.ID,
		&signal.UserID,
		&signal.Symbol,
		&signal.TargetPrice,
		&signal.MinQuantity,
		&signal.TriggerOnCancel,
		&signal.TriggerOnEat,
		&signal.EatPercentage,
		&signal.OriginalQty,
		&signal.LastQty,
		&signal.AutoClose,
		&signal.CloseMarket,
		&signal.WatchMarket,
		&signal.OriginalSide,
		&signal.CreatedAt,
		&signal.UpdatedAt,
		&signal.IsActive,
	)
	return signal, err
}

func (r *PostgresSignalRepo) Delete(ctx context.Context, id int64) error {
	query := `UPDATE signals SET is_active = false, updated_at = $2 WHERE id = $1`
	_, err := r.DB.ExecContext(ctx, query, id, time.Now())
	return err
}
