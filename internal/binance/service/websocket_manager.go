package service

import (
	"context"
	"sizehunt/internal/config"
	"sync"
)

type WebSocketManager struct {
	watchers            map[string]*WebSocketWatcher // symbol_market -> watcher
	mu                  sync.RWMutex
	ctx                 context.Context
	subscriptionService interface{} // subscription.Service
	keysRepo            interface{} // repository.PostgresKeysRepo
	config              *config.Config
}

func NewWebSocketManager(
	ctx context.Context,
	subService interface{}, // subscription.Service
	keysRepo interface{}, // repository.PostgresKeysRepo
	cfg *config.Config,
) *WebSocketManager {
	return &WebSocketManager{
		watchers:            make(map[string]*WebSocketWatcher),
		ctx:                 ctx,
		subscriptionService: subService,
		keysRepo:            keysRepo,
		config:              cfg,
	}
}

func (m *WebSocketManager) GetOrCreateWatcher(symbol string, market string) (*WebSocketWatcher, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := symbol + "_" + market

	if w, ok := m.watchers[key]; ok {
		return w, nil
	}

	w := NewWebSocketWatcher(m.subscriptionService, m.keysRepo, m.config)
	if err := w.Start(m.ctx, symbol, market); err != nil {
		return nil, err
	}

	m.watchers[key] = w
	return w, nil
}
