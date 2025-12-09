package service

import "context"

// ProxyProvider определяет минимальный интерфейс для получения информации о прокси
type ProxyProvider interface {
	GetProxyAddressForUser(userID int64) (string, bool)
	StopProxyForUser(context.Context, int64) error
	CheckAndStopProxy(context.Context, int64) error
}

// OKXClient определяет интерфейс для HTTP клиента OKX
type OKXClient interface {
	GetOrderBook(instID string, depth int, instType string) (*OrderBook, error)
	ValidateAPIKey() error
}
