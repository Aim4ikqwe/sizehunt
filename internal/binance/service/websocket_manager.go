package service

import (
	"context"
	"log"
	"sizehunt/internal/binance/repository"
	"sizehunt/internal/config"
	subscriptionservice "sizehunt/internal/subscription/service"
	"sync"
)

type WebSocketManager struct {
	watchers            map[string]*WebSocketWatcher // symbol_market -> watcher
	mu                  sync.RWMutex
	ctx                 context.Context
	subscriptionService *subscriptionservice.Service
	keysRepo            *repository.PostgresKeysRepo
	config              *config.Config
}

func NewWebSocketManager(
	ctx context.Context,
	subService *subscriptionservice.Service,
	keysRepo *repository.PostgresKeysRepo,
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
		log.Printf("WebSocketManager: Reusing existing watcher for %s", key)
		return w, nil
	}

	log.Printf("WebSocketManager: Creating new watcher for %s", key)
	w := NewWebSocketWatcher(m.subscriptionService, m.keysRepo, m.config)
	if err := w.Start(m.ctx, symbol, market); err != nil {
		log.Printf("WebSocketManager: Failed to start watcher for %s: %v", key, err)
		return nil, err
	}

	m.watchers[key] = w
	log.Printf("WebSocketManager: Started watcher for %s", key)
	return w, nil
}
