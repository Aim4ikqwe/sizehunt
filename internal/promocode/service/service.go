package service

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"sizehunt/internal/promocode"
	subService "sizehunt/internal/subscription/service"
	userService "sizehunt/internal/user/service"
)

var (
	ErrPromoCodeNotFound = errors.New("промокод не найден или неактивен")
	ErrPromoCodeExpired  = errors.New("срок действия промокода истек")
	ErrPromoCodeMaxUses  = errors.New("превышено максимальное количество использований")
	ErrUserAlreadyUsed   = errors.New("пользователь уже использовал этот промокод")
	ErrNotAdmin          = errors.New("требуются права администратора")
)

type PromoCodeRepository interface {
	Create(ctx context.Context, pc *promocode.PromoCode) error
	GetByCode(ctx context.Context, code string) (*promocode.PromoCode, error)
	GetByID(ctx context.Context, id int64) (*promocode.PromoCode, error)
	IncrementUsage(ctx context.Context, promoCodeID int64) error
	RecordUsage(ctx context.Context, userID, promoCodeID int64) error
	HasUserUsed(ctx context.Context, userID, promoCodeID int64) (bool, error)
	GetAll(ctx context.Context) ([]*promocode.PromoCode, error)
	Delete(ctx context.Context, id int64) error
	Update(ctx context.Context, pc *promocode.PromoCode) error
}

type UserService interface {
	IsAdmin(ctx context.Context, userID int64) (bool, error)
}

type SubscriptionService interface {
	IsUserSubscribed(ctx context.Context, userID int64) (bool, error)
	ExtendSubscription(ctx context.Context, userID int64, days int) error
	CreateSubscription(ctx context.Context, userID int64, days int) error
}

type Service struct {
	Repo                PromoCodeRepository
	UserService         UserService
	SubscriptionService SubscriptionService
	DB                  *sql.DB
}

func NewService(repo PromoCodeRepository, userService *userService.UserService, subService *subService.Service, db *sql.DB) *Service {
	return &Service{
		Repo:                repo,
		UserService:         userService,
		SubscriptionService: subService,
		DB:                  db,
	}
}

func (s *Service) CreatePromoCode(ctx context.Context, adminID int64, code string, daysDuration, maxUses int, expiresAt *time.Time) (*promocode.PromoCode, error) {
	isAdmin, err := s.UserService.IsAdmin(ctx, adminID)
	if err != nil || !isAdmin {
		return nil, ErrNotAdmin
	}

	pc := &promocode.PromoCode{
		Code:         code,
		DaysDuration: daysDuration,
		MaxUses:      maxUses,
		UsedCount:    0,
		IsActive:     true,
		CreatedBy:    adminID,
		CreatedAt:    time.Now(),
		ExpiresAt:    expiresAt,
	}

	return pc, s.Repo.Create(ctx, pc)
}

func (s *Service) ApplyPromoCode(ctx context.Context, userID int64, code string) error {
	// Начинаем транзакцию
	tx, err := s.beginTransaction(ctx)
	if err != nil {
		return err
	}

	// Создаем транзакционные репозитории
	txPromoCodeRepo := NewTxPromoCodeRepository(tx)
	txSubRepo := NewTxSubscriptionRepository(tx)

	// Получаем промокод в транзакции
	pc, err := txPromoCodeRepo.GetByCode(ctx, code)
	if err != nil {
		s.rollback(tx)
		if err == sql.ErrNoRows {
			return ErrPromoCodeNotFound
		}
		return err
	}

	// Проверяем, не истек ли срок действия
	if pc.ExpiresAt != nil && time.Now().After(*pc.ExpiresAt) {
		s.rollback(tx)
		return ErrPromoCodeExpired
	}

	// Проверяем, достигнут ли лимит использований
	if pc.UsedCount >= pc.MaxUses {
		s.rollback(tx)
		return ErrPromoCodeMaxUses
	}

	// Проверяем, использовал ли пользователь этот промокод ранее
	used, err := txPromoCodeRepo.HasUserUsed(ctx, userID, pc.ID)
	if err != nil {
		s.rollback(tx)
		return err
	}
	if used {
		s.rollback(tx)
		return ErrUserAlreadyUsed
	}

	// Обновляем счетчик использований
	if err := txPromoCodeRepo.IncrementUsage(ctx, pc.ID); err != nil {
		s.rollback(tx)
		return err
	}

	// Записываем факт использования
	if err := txPromoCodeRepo.RecordUsage(ctx, userID, pc.ID); err != nil {
		s.rollback(tx)
		return err
	}

	// Проверяем статус подписки в транзакции
	sub, err := txSubRepo.GetActiveByUserID(ctx, userID)
	if err != nil {
		s.rollback(tx)
		return err
	}

	if sub != nil {
		// Продлеваем существующую подписку
		newExpiresAt := sub.ExpiresAt.AddDate(0, 0, pc.DaysDuration)
		err = txSubRepo.UpdateExpiresAt(ctx, userID, newExpiresAt)
	} else {
		// Создаем новую подписку
		expiresAt := time.Now().AddDate(0, 0, pc.DaysDuration)
		err = txSubRepo.CreateActive(ctx, userID, expiresAt)
	}

	if err != nil {
		s.rollback(tx)
		return err
	}

	return s.commit(tx)
}

func (s *Service) GetAllPromoCodes(ctx context.Context, userID int64) ([]*promocode.PromoCode, error) {
	isAdmin, err := s.UserService.IsAdmin(ctx, userID)
	if err != nil || !isAdmin {
		return nil, ErrNotAdmin
	}

	return s.Repo.GetAll(ctx)
}

func (s *Service) DeletePromoCode(ctx context.Context, userID, promoCodeID int64) error {
	isAdmin, err := s.UserService.IsAdmin(ctx, userID)
	if err != nil || !isAdmin {
		return ErrNotAdmin
	}

	return s.Repo.Delete(ctx, promoCodeID)
}

func (s *Service) GetPromoCode(ctx context.Context, userID, promoCodeID int64) (*promocode.PromoCode, error) {
	isAdmin, err := s.UserService.IsAdmin(ctx, userID)
	if err != nil || !isAdmin {
		return nil, ErrNotAdmin
	}

	return s.Repo.GetByID(ctx, promoCodeID)
}

// Вспомогательные функции для транзакций
func (s *Service) beginTransaction(ctx context.Context) (*sql.Tx, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return tx, nil
}

func (s *Service) rollback(tx *sql.Tx) {
	if tx != nil {
		tx.Rollback()
	}
}

func (s *Service) commit(tx *sql.Tx) error {
	if tx != nil {
		return tx.Commit()
	}
	return nil
}
