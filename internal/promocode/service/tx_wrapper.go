package service

import (
	"context"
	"database/sql"
	"sizehunt/internal/promocode"
	"sizehunt/internal/subscription"
	"time"
)

// TxPromoCodeRepository - репозиторий для работы с промокодами в транзакции
type TxPromoCodeRepository struct {
	tx *sql.Tx
}

func NewTxPromoCodeRepository(tx *sql.Tx) *TxPromoCodeRepository {
	return &TxPromoCodeRepository{tx: tx}
}

func (r *TxPromoCodeRepository) GetByCode(ctx context.Context, code string) (*promocode.PromoCode, error) {
	pc := &promocode.PromoCode{}
	query := `SELECT id, code, days_duration, max_uses, used_count, is_active, created_by, created_at, expires_at 
              FROM promo_codes WHERE code = $1 AND is_active = true`

	err := r.tx.QueryRowContext(ctx, query, code).Scan(
		&pc.ID,
		&pc.Code,
		&pc.DaysDuration,
		&pc.MaxUses,
		&pc.UsedCount,
		&pc.IsActive,
		&pc.CreatedBy,
		&pc.CreatedAt,
		&pc.ExpiresAt,
	)
	if err != nil {
		return nil, err
	}

	return pc, nil
}

func (r *TxPromoCodeRepository) IncrementUsage(ctx context.Context, promoCodeID int64) error {
	query := `UPDATE promo_codes SET used_count = used_count + 1 WHERE id = $1`
	_, err := r.tx.ExecContext(ctx, query, promoCodeID)
	return err
}

func (r *TxPromoCodeRepository) RecordUsage(ctx context.Context, userID, promoCodeID int64) error {
	query := `INSERT INTO promo_code_usages (user_id, promo_code_id, used_at) VALUES ($1, $2, NOW())`
	_, err := r.tx.ExecContext(ctx, query, userID, promoCodeID)
	return err
}

func (r *TxPromoCodeRepository) HasUserUsed(ctx context.Context, userID, promoCodeID int64) (bool, error) {
	var exists bool
	query := `SELECT EXISTS(SELECT 1 FROM promo_code_usages WHERE user_id = $1 AND promo_code_id = $2)`
	err := r.tx.QueryRowContext(ctx, query, userID, promoCodeID).Scan(&exists)
	return exists, err
}

// TxSubscriptionRepository - репозиторий для работы с подписками в транзакции
type TxSubscriptionRepository struct {
	tx *sql.Tx
}

func NewTxSubscriptionRepository(tx *sql.Tx) *TxSubscriptionRepository {
	return &TxSubscriptionRepository{tx: tx}
}

func (r *TxSubscriptionRepository) GetActiveByUserID(ctx context.Context, userID int64) (*subscription.Subscription, error) {
	sub := &subscription.Subscription{}
	err := r.tx.QueryRowContext(ctx,
		`SELECT id, user_id, status, expires_at, crypto_tx_id, created_at FROM subscriptions 
		 WHERE user_id = $1 AND status = 'active' AND expires_at > NOW()`,
		userID).Scan(&sub.ID, &sub.UserID, &sub.Status, &sub.ExpiresAt, &sub.CryptoTxID, &sub.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return sub, nil
}

func (r *TxSubscriptionRepository) UpdateExpiresAt(ctx context.Context, userID int64, expiresAt time.Time) error {
	_, err := r.tx.ExecContext(ctx,
		`UPDATE subscriptions SET expires_at = $1 WHERE user_id = $2 AND status = 'active'`,
		expiresAt, userID)
	return err
}

func (r *TxSubscriptionRepository) CreateActive(ctx context.Context, userID int64, expiresAt time.Time) error {
	_, err := r.tx.ExecContext(ctx,
		`INSERT INTO subscriptions (user_id, status, expires_at) VALUES ($1, 'active', $2)`,
		userID, expiresAt)
	return err
}
