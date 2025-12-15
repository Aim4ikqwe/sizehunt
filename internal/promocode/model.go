package promocode

import (
	"time"
)

type PromoCode struct {
	ID           int64      `json:"id"`
	Code         string     `json:"code"`
	DaysDuration int        `json:"days_duration"` // количество дней продления подписки
	MaxUses      int        `json:"max_uses"`      // максимальное количество использований
	UsedCount    int        `json:"used_count"`    // количество уже использованных
	IsActive     bool       `json:"is_active"`     // активен ли промокод
	CreatedBy    int64      `json:"created_by"`    // ID администратора, который создал
	CreatedAt    time.Time  `json:"created_at"`
	ExpiresAt    *time.Time `json:"expires_at"` // дата истечения действия (опционально)
}

type PromoCodeUsage struct {
	ID          int64     `json:"id"`
	UserID      int64     `json:"user_id"`
	PromoCodeID int64     `json:"promo_code_id"`
	UsedAt      time.Time `json:"used_at"`
}
