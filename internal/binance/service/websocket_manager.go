// internal/binance/service/websocket_manager.go
package service

import (
	"context"
	"fmt"
	"sizehunt/internal/binance/repository"
	"sizehunt/internal/config"
	subscriptionservice "sizehunt/internal/subscription/service"
	"sync"
)

type WebSocketManager struct {
	spotWatcher    *MarketDepthWatcher
	futuresWatcher *MarketDepthWatcher
	mu             sync.RWMutex
	subService     *subscriptionservice.Service
	keysRepo       *repository.PostgresKeysRepo
	cfg            *config.Config
}

func NewWebSocketManager(
	ctx context.Context,
	subService *subscriptionservice.Service,
	keysRepo *repository.PostgresKeysRepo,
	cfg *config.Config,
) *WebSocketManager {
	manager := &WebSocketManager{
		subService: subService,
		keysRepo:   keysRepo,
		cfg:        cfg,
	}

	// Создаём watchers для каждого рынка, передавая контекст
	manager.spotWatcher = NewMarketDepthWatcher(ctx, "spot", subService, keysRepo, cfg)
	manager.futuresWatcher = NewMarketDepthWatcher(ctx, "futures", subService, keysRepo, cfg)

	// Запуск больше не происходит здесь автоматически при создании.
	// Он инициируется при добавлении первого сигнала в watcher через AddSignal.

	return manager
}

func (m *WebSocketManager) GetOrCreateWatcher(symbol string, market string) (*MarketDepthWatcher, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	switch market {
	case "spot":
		return m.spotWatcher, nil
	case "futures":
		return m.futuresWatcher, nil
	default:
		return nil, fmt.Errorf("unsupported market: %s", market)
	}
}
