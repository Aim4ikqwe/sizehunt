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
	"time"

	"github.com/adshao/go-binance/v2/futures"
)

// UserWatcher хранит watcher'ы по символам для одного пользователя
type UserWatcher struct {
	spotWatchers    map[string]*MarketDepthWatcher
	futuresWatchers map[string]*MarketDepthWatcher
	// Общие для пользователя ресурсы (futures)
	futuresClient   *futures.Client
	positionWatcher *PositionWatcher
	userDataStream  *UserDataStream
	// флаг, что userDataStream запущен
	userDataStreamStarted bool
	mu                    sync.Mutex // локальный мьютекс для UserWatcher
}

type WebSocketManager struct {
	mu           sync.RWMutex
	userWatchers map[int64]*UserWatcher // userID → UserWatcher
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
	manager := &WebSocketManager{
		userWatchers: make(map[int64]*UserWatcher),
		subService:   subService,
		keysRepo:     keysRepo,
		cfg:          cfg,
		ctx:          ctx,
	}

	log.Println("WebSocketManager: Initialized successfully")
	return manager
}

// cleanupOldWatcher асинхронно закрывает старый watcher и освобождает ресурсы
func (m *WebSocketManager) cleanupOldWatcher(oldWatcher *MarketDepthWatcher, symbol string, userID int64) {
	if oldWatcher == nil {
		return
	}

	go func() {
		startTime := time.Now()
		log.Printf("WebSocketManager: Starting async cleanup for symbol %s, user %d", symbol, userID)

		// Закрываем WebSocket соединение с таймаутом
		if oldWatcher.client != nil {
			log.Printf("WebSocketManager: Closing WebSocket client for symbol %s, user %d", symbol, userID)
			oldWatcher.client.Close()
			log.Printf("WebSocketManager: WebSocket client closed for symbol %s, user %d (took %v)", symbol, userID, time.Since(startTime))
		}
		if oldWatcher.client == nil {
			log.Printf("WebSocketManager: Attempt to clean up watcher with nil client for symbol %s, user %d", symbol, userID)
			return
		}

		// Удаляем все сигналы для символа
		log.Printf("WebSocketManager: Removing all signals for symbol %s, user %d", symbol, userID)
		oldWatcher.RemoveAllSignalsForSymbol(symbol)
		log.Printf("WebSocketManager: All signals removed for symbol %s, user %d", symbol, userID)

		log.Printf("WebSocketManager: Async cleanup completed for symbol %s, user %d (total time: %v)", symbol, userID, time.Since(startTime))
	}()
}

// GetOrCreateWatcherForUser возвращает watcher для конкретного пользователя и символа.
// Переписан для избежания блокировок при закрытии соединений
func (m *WebSocketManager) GetOrCreateWatcherForUser(userID int64, symbol, market string, autoClose bool) (*MarketDepthWatcher, error) {
	startTime := time.Now()
	log.Printf("WebSocketManager: GetOrCreateWatcherForUser called for user %d, symbol %s, market %s, autoClose %v",
		userID, symbol, market, autoClose)
	defer func() {
		log.Printf("WebSocketManager: GetOrCreateWatcherForUser completed for user %d, symbol %s (total time: %v)",
			userID, symbol, time.Since(startTime))
	}()

	// 1. Быстрая фаза под мьютексом: получить или создать UserWatcher
	m.mu.Lock()
	uw, exists := m.userWatchers[userID]
	if !exists {
		log.Printf("WebSocketManager: Creating new UserWatcher for user %d", userID)
		uw = &UserWatcher{
			spotWatchers:    make(map[string]*MarketDepthWatcher),
			futuresWatchers: make(map[string]*MarketDepthWatcher),
		}
		m.userWatchers[userID] = uw
	} else {
		log.Printf("WebSocketManager: Found existing UserWatcher for user %d", userID)
	}
	m.mu.Unlock()

	// 2. Работа с конкретным рынком
	switch market {
	case "spot":
		uw.mu.Lock()
		defer uw.mu.Unlock()

		// Проверяем существующий watcher
		if w, exists := uw.spotWatchers[symbol]; exists && w != nil {
			log.Printf("WebSocketManager: Found existing spot watcher for user %d, symbol %s", userID, symbol)
			return w, nil
		}

		// Создаем новый watcher
		log.Printf("WebSocketManager: Creating new spot watcher for user %d, symbol %s", userID, symbol)
		newWatcher := NewMarketDepthWatcher(
			m.ctx, "spot", m.subService, m.keysRepo, m.cfg, nil, nil, nil,
		)

		uw.spotWatchers[symbol] = newWatcher

		log.Printf("WebSocketManager: New spot watcher created for user %d, symbol %s", userID, symbol)
		return newWatcher, nil

	case "futures":
		// Используем локальный мьютекс для UserWatcher
		uw.mu.Lock()
		defer uw.mu.Unlock()

		// Удаляем старый watcher если он существует
		if w, exists := uw.futuresWatchers[symbol]; exists && w != nil {
			log.Printf("WebSocketManager: Found existing futures watcher for user %d, symbol %s - will be replaced", userID, symbol)
			// Асинхронно очищаем старый watcher
			m.cleanupOldWatcher(w, symbol, userID)
		}

		// Если требуется autoClose - проверяем и инициализируем ресурсы
		if autoClose {
			log.Printf("WebSocketManager: AutoClose enabled - checking/initializing futures resources for user %d", userID)

			// Проверяем/пересоздаем userDataStream
			if uw.userDataStream != nil {
				log.Printf("WebSocketManager: Stopping existing userDataStream for user %d", userID)

				// Создаем контекст с таймаутом для остановки
				stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()

				done := make(chan struct{})
				go func() {
					uw.userDataStream.StopWithContext(stopCtx)
					close(done)
				}()

				select {
				case <-done:
					log.Printf("WebSocketManager: UserDataStream for user %d stopped cleanly", userID)
				case <-time.After(3 * time.Second):
					log.Printf("WebSocketManager: WARNING: Forced stop of UserDataStream for user %d after timeout", userID)
				}

				uw.userDataStream = nil
				uw.futuresClient = nil
				uw.positionWatcher = nil
				uw.userDataStreamStarted = false
			}

			// Получаем ключи пользователя
			okKeys, apiKey, secretKey := m.getKeysForUser(userID)
			if !okKeys {
				log.Printf("WebSocketManager: ERROR: Failed to get valid API keys for user %d", userID)
				return nil, fmt.Errorf("futures auto-close requires valid API keys")
			}

			// Создаем новые ресурсы
			log.Printf("WebSocketManager: Creating new futures client for user %d", userID)
			uw.futuresClient = futures.NewClient(apiKey, secretKey)
			uw.positionWatcher = NewPositionWatcher()
			uw.userDataStream = NewUserDataStream(uw.futuresClient, uw.positionWatcher)
			uw.userDataStreamStarted = false
			log.Printf("WebSocketManager: New futures resources created for user %d", userID)

			// 🔥 КРИТИЧЕСКИ ВАЖНО: Запускаем UserDataStream для получения позиций
			log.Printf("WebSocketManager: Starting UserDataStream for user %d", userID)
			if err := uw.userDataStream.Start(symbol); err != nil {
				log.Printf("WebSocketManager: ERROR: Failed to start UserDataStream for user %d: %v", userID, err)
				// Откатываем созданные ресурсы
				uw.futuresClient = nil
				uw.positionWatcher = nil
				uw.userDataStream = nil
				return nil, fmt.Errorf("failed to start user data stream: %w", err)
			}
			uw.userDataStreamStarted = true
			log.Printf("WebSocketManager: UserDataStream successfully started for user %d", userID)
		}

		// Создаем новый watcher
		log.Printf("WebSocketManager: Creating new futures watcher for user %d, symbol %s", userID, symbol)
		newWatcher := NewMarketDepthWatcher(
			m.ctx,
			"futures",
			m.subService,
			m.keysRepo,
			m.cfg,
			uw.futuresClient,
			uw.positionWatcher,
			uw.userDataStream,
		)

		uw.futuresWatchers[symbol] = newWatcher

		log.Printf("WebSocketManager: New futures watcher created for user %d, symbol %s", userID, symbol)
		return newWatcher, nil

	default:
		log.Printf("WebSocketManager: ERROR: Unsupported market type: %s", market)
		return nil, fmt.Errorf("unsupported market: %s", market)
	}
}

// getKeysForUser — получает и расшифровывает ключи пользователя
func (m *WebSocketManager) getKeysForUser(userID int64) (bool, string, string) {
	startTime := time.Now()
	defer func() {
		log.Printf("WebSocketManager: getKeysForUser for user %d took %v", userID, time.Since(startTime))
	}()

	log.Printf("WebSocketManager: Getting API keys for user %d", userID)
	keys, err := m.keysRepo.GetKeys(userID)
	if err != nil {
		log.Printf("WebSocketManager: ERROR: Failed to get keys for user %d: %v", userID, err)
		return false, "", ""
	}

	log.Printf("WebSocketManager: Decrypting API key for user %d", userID)
	apiKey, err := DecryptAES(keys.APIKey, m.cfg.EncryptionSecret)
	if err != nil {
		log.Printf("WebSocketManager: ERROR: Failed to decrypt API key for user %d: %v", userID, err)
		return false, "", ""
	}

	log.Printf("WebSocketManager: Decrypting Secret key for user %d", userID)
	secretKey, err := DecryptAES(keys.SecretKey, m.cfg.EncryptionSecret)
	if err != nil {
		log.Printf("WebSocketManager: ERROR: Failed to decrypt Secret key for user %d: %v", userID, err)
		return false, "", ""
	}

	log.Printf("WebSocketManager: Successfully retrieved keys for user %d", userID)
	return true, apiKey, secretKey
}

// CleanupUserResources очищает все ресурсы пользователя
func (m *WebSocketManager) CleanupUserResources(userID int64) {
	log.Printf("WebSocketManager: Starting cleanup for user %d", userID)
	startTime := time.Now()
	defer func() {
		log.Printf("WebSocketManager: Cleanup completed for user %d (total time: %v)", userID, time.Since(startTime))
	}()

	m.mu.Lock()
	uw, exists := m.userWatchers[userID]
	if !exists {
		m.mu.Unlock()
		log.Printf("WebSocketManager: No resources found for user %d", userID)
		return
	}
	delete(m.userWatchers, userID)
	m.mu.Unlock()

	if uw == nil {
		return
	}

	// Очищаем спотовые watcher'ы
	for symbol, watcher := range uw.spotWatchers {
		log.Printf("WebSocketManager: Cleaning up spot watcher for user %d, symbol %s", userID, symbol)
		m.cleanupOldWatcher(watcher, symbol, userID)
	}

	// Очищаем фьючерсные watcher'ы
	for symbol, watcher := range uw.futuresWatchers {
		log.Printf("WebSocketManager: Cleaning up futures watcher for user %d, symbol %s", userID, symbol)
		m.cleanupOldWatcher(watcher, symbol, userID)
	}

	// Останавливаем userDataStream
	if uw.userDataStream != nil {
		log.Printf("WebSocketManager: Stopping userDataStream for user %d", userID)
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		done := make(chan struct{})
		go func() {
			uw.userDataStream.StopWithContext(stopCtx)
			close(done)
		}()

		select {
		case <-done:
			log.Printf("WebSocketManager: UserDataStream stopped cleanly for user %d", userID)
		case <-time.After(5 * time.Second):
			log.Printf("WebSocketManager: WARNING: Forced stop of UserDataStream for user %d after timeout", userID)
		}
	}

	log.Printf("WebSocketManager: All resources cleaned up for user %d", userID)
}

// internal/binance/service/websocket_manager.go
// ... существующий код ...

// GetUserSignals возвращает все сигналы пользователя
func (m *WebSocketManager) GetUserSignals(userID int64) []SignalResponse {
	m.mu.RLock()
	defer m.mu.RUnlock()

	uw, exists := m.userWatchers[userID]
	if !exists {
		return []SignalResponse{}
	}

	var signals []SignalResponse

	// Собираем сигналы из spot watchers
	for symbol, watcher := range uw.spotWatchers {
		watcher.mu.RLock()
		signalList := watcher.GetAllSignalsForSymbol(symbol)
		watcher.mu.RUnlock()

		for _, s := range signalList {
			if s.UserID == userID {
				signals = append(signals, convertSignalToResponse(s))
			}
		}
	}

	// Собираем сигналы из futures watchers
	for symbol, watcher := range uw.futuresWatchers {
		watcher.mu.RLock()
		signalList := watcher.GetAllSignalsForSymbol(symbol)
		watcher.mu.RUnlock()

		for _, s := range signalList {
			if s.UserID == userID {
				signals = append(signals, convertSignalToResponse(s))
			}
		}
	}

	return signals
}

// DeleteUserSignal удаляет сигнал пользователя по ID
func (m *WebSocketManager) DeleteUserSignal(userID int64, signalID int64) error {
	m.mu.RLock()
	uw, exists := m.userWatchers[userID]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("user not found")
	}

	// Блокируем UserWatcher для операции удаления
	uw.mu.Lock()
	defer uw.mu.Unlock()

	// Ищем и удаляем в spot watchers
	found := false
	for symbol, watcher := range uw.spotWatchers {
		watcher.mu.Lock()
		// Проверяем наличие сигнала перед удалением
		if signals, ok := watcher.signalsBySymbol[symbol]; ok {
			for _, s := range signals {
				if s.ID == signalID {
					watcher.removeSignalByIDLocked(signalID)
					found = true
					break
				}
			}
		}
		watcher.mu.Unlock()
		if found {
			break
		}
	}

	// Если не нашли в spot, ищем в futures
	if !found {
		for symbol, watcher := range uw.futuresWatchers {
			watcher.mu.Lock()
			// Проверяем наличие сигнала перед удалением
			if signals, ok := watcher.signalsBySymbol[symbol]; ok {
				for _, s := range signals {
					if s.ID == signalID {
						watcher.removeSignalByIDLocked(signalID)
						found = true
						break
					}
				}
			}
			watcher.mu.Unlock()
			if found {
				break
			}
		}
	}

	if !found {
		return fmt.Errorf("signal not found")
	}

	return nil
}

func convertSignalToResponse(s *Signal) SignalResponse {
	return SignalResponse{
		ID:              s.ID,
		Symbol:          s.Symbol,
		TargetPrice:     s.TargetPrice,
		MinQuantity:     s.MinQuantity,
		TriggerOnCancel: s.TriggerOnCancel,
		TriggerOnEat:    s.TriggerOnEat,
		EatPercentage:   s.EatPercentage,
		OriginalQty:     s.OriginalQty,
		LastQty:         s.LastQty,
		AutoClose:       s.AutoClose,
		CloseMarket:     s.CloseMarket,
		WatchMarket:     s.WatchMarket,
		OriginalSide:    s.OriginalSide,
		CreatedAt:       s.CreatedAt,
	}
}

// SignalResponse структура для ответа API
type SignalResponse struct {
	ID              int64     `json:"id"`
	Symbol          string    `json:"symbol"`
	TargetPrice     float64   `json:"target_price"`
	MinQuantity     float64   `json:"min_quantity"`
	TriggerOnCancel bool      `json:"trigger_on_cancel"`
	TriggerOnEat    bool      `json:"trigger_on_eat"`
	EatPercentage   float64   `json:"eat_percentage"`
	OriginalQty     float64   `json:"original_qty"`
	LastQty         float64   `json:"last_qty"`
	AutoClose       bool      `json:"auto_close"`
	CloseMarket     string    `json:"close_market"`
	WatchMarket     string    `json:"watch_market"`
	OriginalSide    string    `json:"original_side"`
	CreatedAt       time.Time `json:"created_at"`
}

func (m *WebSocketManager) GetAllUserIDs() []int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	userIDs := make([]int64, 0, len(m.userWatchers))
	for userID := range m.userWatchers {
		userIDs = append(userIDs, userID)
	}

	return userIDs
}
