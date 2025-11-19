package dto

import "github.com/go-playground/validator/v10"

type RegisterRequest struct {
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required,min=6,max=72"`
}

type LoginRequest struct {
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required,min=6,max=72"`
}

type SaveKeysRequest struct {
	APIKey    string `json:"api_key" validate:"required"`
	SecretKey string `json:"secret_key" validate:"required"`
}

type CreateSignalRequest struct {
	Symbol          string  `json:"symbol" validate:"required"`
	TargetPrice     float64 `json:"target_price" validate:"required"`
	MinQuantity     float64 `json:"min_quantity" validate:"required"`
	TriggerOnCancel bool    `json:"trigger_on_cancel"`
	TriggerOnEat    bool    `json:"trigger_on_eat"`
	EatPercentage   float64 `json:"eat_percentage"` // 0.5 = 50%
	Market          string  `json:"market"`         // "spot" или "futures"
	AutoClose       bool    `json:"auto_close"`     // закрыть позицию при срабатывании
}

var Validate = validator.New()
