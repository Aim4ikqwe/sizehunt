package repository

import (
	"time"
)

type SignalDB struct {
	ID              int64     `db:"id"`
	UserID          int64     `db:"user_id"`
	Symbol          string    `db:"symbol"`
	TargetPrice     float64   `db:"target_price"`
	MinQuantity     float64   `db:"min_quantity"`
	TriggerOnCancel bool      `db:"trigger_on_cancel"`
	TriggerOnEat    bool      `db:"trigger_on_eat"`
	EatPercentage   float64   `db:"eat_percentage"`
	OriginalQty     float64   `db:"original_qty"`
	LastQty         float64   `db:"last_qty"`
	AutoClose       bool      `db:"auto_close"`
	CloseMarket     string    `db:"close_market"`
	WatchMarket     string    `db:"watch_market"`
	OriginalSide    string    `db:"original_side"`
	CreatedAt       time.Time `db:"created_at"`
	UpdatedAt       time.Time `db:"updated_at"`
	IsActive        bool      `db:"is_active"`
}
