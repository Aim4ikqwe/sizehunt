package service

import "context"

// ProxyProvider интерфейс для работы с прокси-сервисом
type ProxyProvider interface {
	GetProxyAddressForUser(userID int64) (string, bool)
	CheckAndStopProxy(ctx context.Context, userID int64) error
}
