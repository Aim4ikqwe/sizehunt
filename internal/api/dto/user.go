package dto

import (
	"regexp"
	"strings"

	"github.com/go-playground/validator/v10"
)

// Кастомные валидаторы
var (
	Validate       = validator.New()
	symbolRegex    = regexp.MustCompile(`^[A-Z0-9]{3,10}$`)
	priceRegex     = regexp.MustCompile(`^\d+(\.\d{1,8})?$`)
	quantityRegex  = regexp.MustCompile(`^\d+(\.\d{1,8})?$`)
	secretKeyRegex = regexp.MustCompile(`^[A-Za-z0-9+/=]{32,64}$`)
	apiKeyRegex    = regexp.MustCompile(`^[A-Za-z0-9]{64}$`)
)

func init() {
	// Регистрация кастомных валидаторов
	_ = Validate.RegisterValidation("symbol", validateSymbol)
	_ = Validate.RegisterValidation("binance_price", validateBinancePrice)
	_ = Validate.RegisterValidation("binance_quantity", validateBinanceQuantity)
	_ = Validate.RegisterValidation("api_key", validateAPIKey)
	_ = Validate.RegisterValidation("secret_key", validateSecretKey)
	_ = Validate.RegisterValidation("proxy_address", validateProxyAddress)
	_ = Validate.RegisterValidation("encryption_method", validateEncryptionMethod)
}

func validateSymbol(fl validator.FieldLevel) bool {
	value := fl.Field().String()
	return symbolRegex.MatchString(value) && len(value) >= 3 && len(value) <= 10
}

func validateBinancePrice(fl validator.FieldLevel) bool {
	value := fl.Field().String()
	return priceRegex.MatchString(value)
}

func validateBinanceQuantity(fl validator.FieldLevel) bool {
	value := fl.Field().String()
	return quantityRegex.MatchString(value) && strings.Count(value, ".") <= 1
}

func validateAPIKey(fl validator.FieldLevel) bool {
	value := fl.Field().String()
	return apiKeyRegex.MatchString(value) && len(value) == 64
}

func validateSecretKey(fl validator.FieldLevel) bool {
	value := fl.Field().String()
	return secretKeyRegex.MatchString(value) && len(value) >= 32
}

func validateProxyAddress(fl validator.FieldLevel) bool {
	value := fl.Field().String()
	// Разрешаем localhost, IP-адреса и домены
	return value != "" && !strings.Contains(value, " ")
}

func validateEncryptionMethod(fl validator.FieldLevel) bool {
	value := fl.Field().String()
	validMethods := map[string]bool{
		"aes-256-gcm": true, "chacha20-ietf-poly1305": true, "aes-128-gcm": true,
		"aes-256-cfb": true, "aes-192-cfb": true, "aes-128-cfb": true,
	}
	return validMethods[value]
}

type RegisterRequest struct {
	Email    string `json:"email" validate:"required,email,max=100"`
	Password string `json:"password" validate:"required,min=6,max=72,containsany=!@#$%^&*"`
}

type LoginRequest struct {
	Email    string `json:"email" validate:"required,email,max=100"`
	Password string `json:"password" validate:"required,min=6,max=72"`
}

type SaveKeysRequest struct {
	APIKey    string `json:"api_key" validate:"required,api_key"`
	SecretKey string `json:"secret_key" validate:"required,secret_key"`
}

type CreateSignalRequest struct {
	Symbol          string  `json:"symbol" validate:"required,symbol"`
	TargetPrice     float64 `json:"target_price" validate:"required,gt=0"`
	MinQuantity     float64 `json:"min_quantity" validate:"required,gt=0"`
	TriggerOnCancel bool    `json:"trigger_on_cancel"`
	TriggerOnEat    bool    `json:"trigger_on_eat"`
	EatPercentage   float64 `json:"eat_percentage" validate:"omitempty,gte=0.01,lte=1"`
	Market          string  `json:"market" validate:"required,oneof=spot futures"`
	AutoClose       bool    `json:"auto_close"`
}

type ProxyConfigRequest struct {
	ServerAddr       string `json:"server_addr" validate:"required,proxy_address"`
	ServerPort       int    `json:"server_port" validate:"required,min=1,max=65535"`
	Password         string `json:"password" validate:"required,min=8,max=64"`
	EncryptionMethod string `json:"encryption_method" validate:"required,encryption_method"`
}
