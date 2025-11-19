package subscription

import "time"

type Subscription struct {
	ID         int64
	UserID     int64
	Status     string // "pending", "active", "expired"
	ExpiresAt  time.Time
	CryptoTxID string // ID транзакции
	CreatedAt  time.Time
}
