package repository

import (
	"context"
	"database/sql"
	"sizehunt/internal/subscription"
	"time" // <--- добавь
)

type SubscriptionRepository struct {
	db *sql.DB
}

func NewSubscriptionRepository(db *sql.DB) *SubscriptionRepository {
	return &SubscriptionRepository{db: db}
}

func (r *SubscriptionRepository) GetActiveByUserID(ctx context.Context, userID int64) (*subscription.Subscription, error) {
	sub := &subscription.Subscription{}
	err := r.db.QueryRowContext(ctx,
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

func (r *SubscriptionRepository) CreatePending(ctx context.Context, userID int64, txID string) (*subscription.Subscription, error) {
	sub := &subscription.Subscription{
		UserID:     userID,
		Status:     "pending",
		CryptoTxID: txID,
		ExpiresAt:  time.Now().Add(30 * 24 * time.Hour), // 30 дней
	}

	err := r.db.QueryRowContext(ctx,
		`INSERT INTO subscriptions (user_id, status, expires_at, crypto_tx_id) 
		 VALUES ($1, $2, $3, $4) RETURNING id, created_at`,
		sub.UserID, sub.Status, sub.ExpiresAt, sub.CryptoTxID).
		Scan(&sub.ID, &sub.CreatedAt)
	if err != nil {
		return nil, err
	}
	return sub, nil
}

func (r *SubscriptionRepository) Activate(ctx context.Context, txID string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE subscriptions SET status = 'active' WHERE crypto_tx_id = $1`,
		txID)
	return err
}
