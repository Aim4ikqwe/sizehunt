package service

import (
	"context"
	"sizehunt/internal/subscription"
	"time" // <--- добавь
)

type SubscriptionRepository interface {
	GetActiveByUserID(ctx context.Context, userID int64) (*subscription.Subscription, error)
	CreatePending(ctx context.Context, userID int64, txID string) (*subscription.Subscription, error)
	Activate(ctx context.Context, txID string) error
	UpdateExpiresAt(ctx context.Context, userID int64, expiresAt time.Time) error
	CreateActive(ctx context.Context, userID int64, expiresAt time.Time) error
}

type Service struct {
	repo SubscriptionRepository
}

func NewService(repo SubscriptionRepository) *Service {
	return &Service{repo: repo}
}

func (s *Service) IsUserSubscribed(ctx context.Context, userID int64) (bool, error) {
	sub, err := s.repo.GetActiveByUserID(ctx, userID)
	if err != nil {
		return false, err
	}
	return sub != nil, nil
}

func (s *Service) ExtendSubscription(ctx context.Context, userID int64, days int) error {
	sub, err := s.repo.GetActiveByUserID(ctx, userID)
	if err != nil {
		return err
	}

	if sub == nil {
		return s.CreateSubscription(ctx, userID, days)
	}

	newExpiresAt := sub.ExpiresAt.AddDate(0, 0, days)
	return s.repo.UpdateExpiresAt(ctx, userID, newExpiresAt)
}

func (s *Service) CreateSubscription(ctx context.Context, userID int64, days int) error {
	expiresAt := time.Now().AddDate(0, 0, days)
	return s.repo.CreateActive(ctx, userID, expiresAt)
}

func (s *Service) CreatePayment(ctx context.Context, userID int64) (string, error) {
	// Генерируем уникальный ID транзакции
	txID := "tx_" + time.Now().Format("20060102150405") + "_" + string([]byte{byte(userID % 256)})

	sub, err := s.repo.CreatePending(ctx, userID, txID)
	if err != nil {
		return "", err
	}

	return sub.CryptoTxID, nil
}

func (s *Service) HandlePaymentWebhook(ctx context.Context, txID string) error {
	return s.repo.Activate(ctx, txID)
}

func (s *Service) GetSubscription(ctx context.Context, userID int64) (*subscription.Subscription, error) {
	return s.repo.GetActiveByUserID(ctx, userID)
}
