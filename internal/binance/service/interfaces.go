package service

import "context"

// ProxyProvider определяет минимальный интерфейс для получения информации о прокси
type ProxyProvider interface {
	GetProxyAddressForUser(userID int64) (string, bool)
	StopProxyForUser(context.Context, int64) error
	CheckAndStopProxy(context.Context, int64) error
}
