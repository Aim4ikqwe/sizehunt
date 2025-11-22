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

type WebSocketManager struct {
	spotWatcher     *MarketDepthWatcher
	futuresWatcher  *MarketDepthWatcher
	mu              sync.RWMutex
	subService      *subscriptionservice.Service
	keysRepo        *repository.PostgresKeysRepo
	cfg             *config.Config
	positionWatcher *PositionWatcher
	userDataStream  *UserDataStream
}

func NewWebSocketManager(
	ctx context.Context,
	subService *subscriptionservice.Service,
	keysRepo *repository.PostgresKeysRepo,
	cfg *config.Config,
) *WebSocketManager {
	// --- ИНИЦИАЛИЗАЦИЯ НОВЫХ КОМПОНЕНТОВ ---
	// Получаем API-ключи пользователя 1 (или нужно для всех пользователей?)
	// ВАЖНО: В текущей архитектуре это будет работать ТОЛЬКО для ОДНОГО пользователя (владельца ключей).
	// Для мультиюзерности нужна отдельная логика (см. "Важное замечание" ниже).
	userID := int64(8)                                            // <-- ВРЕМЕННО! Нужно брать из запроса или делать мультиюзерским
	_, apiKey, secretKey := getKeysForUser(userID, keysRepo, cfg) // Реализуй эту функцию

	futuresClient := futures.NewClient(apiKey, secretKey)
	positionWatcher := NewPositionWatcher()
	userDataStream := NewUserDataStream(futuresClient, positionWatcher)

	// Запускаем User Data Stream
	if err := userDataStream.Start(); err != nil {
		log.Fatalf("Failed to start User Data Stream: %v", err)
	}
	// --- КОНЕЦ ИНИЦИАЛИЗАЦИИ ---

	manager := &WebSocketManager{
		subService:      subService,
		keysRepo:        keysRepo,
		cfg:             cfg,
		positionWatcher: positionWatcher,
		userDataStream:  userDataStream,
	}
	// Создаём watchers, передавая новые зависимости
	manager.spotWatcher = NewMarketDepthWatcher(ctx, "spot", subService, keysRepo, cfg, nil, nil, nil)
	manager.futuresWatcher = NewMarketDepthWatcher(ctx, "futures", subService, keysRepo, cfg, futuresClient, positionWatcher, userDataStream)
	return manager
}
func getKeysForUser(userID int64, keysRepo *repository.PostgresKeysRepo, cfg *config.Config) (bool, string, string) {
	keys, err := keysRepo.GetKeys(userID)
	if err != nil {
		log.Printf("WebSocketManager: Failed to get keys for user %d: %v", userID, err)
		return false, "", ""
	}
	secret := cfg.EncryptionSecret
	apiKey, err := DecryptAES(keys.APIKey, secret)
	if err != nil {
		log.Printf("WebSocketManager: Failed to decrypt API key for user %d: %v", userID, err)
		return false, "", ""
	}
	secretKey, err := DecryptAES(keys.SecretKey, secret)
	if err != nil {
		log.Printf("WebSocketManager: Failed to decrypt Secret key for user %d: %v", userID, err)
		return false, "", ""
	}
	return true, apiKey, secretKey
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
