// internal/binance/service/websocket_manager.go
package service

import (
	"context"
	"fmt"
	"log"
	"sizehunt/internal/binance/repository"
	"sizehunt/internal/config"
	subscriptionservice "sizehunt/internal/subscription/service"
	"sync"

	"github.com/adshao/go-binance/v2/futures"
)

// UserWatcher holds watchers and streams per user
type UserWatcher struct {
	spotWatcher     *MarketDepthWatcher
	futuresWatcher  *MarketDepthWatcher
	userDataStream  *UserDataStream
	positionWatcher *PositionWatcher
	futuresClient   *futures.Client
}

type WebSocketManager struct {
	mu           sync.RWMutex
	userWatchers map[int64]*UserWatcher // userID → watcher set
	subService   *subscriptionservice.Service
	keysRepo     *repository.PostgresKeysRepo
	cfg          *config.Config
	ctx          context.Context
}

func NewWebSocketManager(
	ctx context.Context,
	subService *subscriptionservice.Service,
	keysRepo *repository.PostgresKeysRepo,
	cfg *config.Config,
) *WebSocketManager {
	return &WebSocketManager{
		userWatchers: make(map[int64]*UserWatcher),
		subService:   subService,
		keysRepo:     keysRepo,
		cfg:          cfg,
		ctx:          ctx,
	}
}

// GetOrCreateWatcherForUser возвращает SPOT или Futures watcher для пользователя
func (m *WebSocketManager) GetOrCreateWatcherForUser(userID int64, symbol, market string, autoClose bool) (*MarketDepthWatcher, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	userWatcher, exists := m.userWatchers[userID]
	if !exists {
		userWatcher = &UserWatcher{}
		m.userWatchers[userID] = userWatcher
	}

	switch market {
	case "spot":
		if userWatcher.spotWatcher == nil {
			userWatcher.spotWatcher = NewMarketDepthWatcher(
				m.ctx, "spot", m.subService, m.keysRepo, m.cfg, nil, nil, nil,
			)
		}
		return userWatcher.spotWatcher, nil

	case "futures":
		if userWatcher.futuresWatcher == nil {
			// Нужны ключи для futures клиента и UserDataStream — но только если требуется автозакрытие
			var futuresClient *futures.Client
			var positionWatcher *PositionWatcher
			var userDataStream *UserDataStream

			if autoClose {
				// Получаем и расшифровываем ключи
				ok, apiKey, secretKey := m.getKeysForUser(userID)
				if !ok {
					return nil, fmt.Errorf("futures auto-close requires valid API keys")
				}
				futuresClient = futures.NewClient(apiKey, secretKey)
				positionWatcher = NewPositionWatcher()
				userDataStream = NewUserDataStream(futuresClient, positionWatcher)
				if err := userDataStream.Start(); err != nil {
					log.Printf("WebSocketManager: Failed to start UserDataStream for user %d: %v", userID, err)
					return nil, fmt.Errorf("failed to start user data stream")
				}
			}
			userWatcher.futuresClient = futuresClient
			userWatcher.positionWatcher = positionWatcher
			userWatcher.userDataStream = userDataStream

			userWatcher.futuresWatcher = NewMarketDepthWatcher(
				m.ctx,
				"futures",
				m.subService,
				m.keysRepo,
				m.cfg,
				futuresClient,
				positionWatcher,
				userDataStream,
			)
		}
		return userWatcher.futuresWatcher, nil

	default:
		return nil, fmt.Errorf("unsupported market: %s", market)
	}
}

func (m *WebSocketManager) getKeysForUser(userID int64) (bool, string, string) {
	keys, err := m.keysRepo.GetKeys(userID)
	if err != nil {
		log.Printf("WebSocketManager: Failed to get keys for user %d: %v", userID, err)
		return false, "", ""
	}
	apiKey, err := DecryptAES(keys.APIKey, m.cfg.EncryptionSecret)
	if err != nil {
		log.Printf("WebSocketManager: Failed to decrypt API key for user %d: %v", userID, err)
		return false, "", ""
	}
	secretKey, err := DecryptAES(keys.SecretKey, m.cfg.EncryptionSecret)
	if err != nil {
		log.Printf("WebSocketManager: Failed to decrypt Secret key for user %d: %v", userID, err)
		return false, "", ""
	}
	return true, apiKey, secretKey
}
