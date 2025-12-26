package service

import (
	"context"
	"log"
	bybit_repository "sizehunt/internal/bybit/repository"
	"sizehunt/internal/config"
	subscriptionservice "sizehunt/internal/subscription/service"
	"sync"
	"time"
)

// UserWatcher хранит watcher'ы по символам для одного пользователя Bybit
type UserWatcher struct {
	watcher          *MarketDepthWatcher
	httpClient       *BybitHTTPClient
	signalRepository bybit_repository.SignalRepository
	positionWatcher  *PositionWatcher
	mu               sync.Mutex
}

// SignalResponse структура для ответа API
type SignalResponse struct {
	ID              int64     `json:"id"`
	Symbol          string    `json:"symbol"`
	Category        string    `json:"category"`
	TargetPrice     float64   `json:"target_price"`
	MinQuantity     float64   `json:"min_quantity"`
	TriggerOnCancel bool      `json:"trigger_on_cancel"`
	TriggerOnEat    bool      `json:"trigger_on_eat"`
	EatPercentage   float64   `json:"eat_percentage"`
	OriginalQty     float64   `json:"original_qty"`
	LastQty         float64   `json:"last_qty"`
	AutoClose       bool      `json:"auto_close"`
	WatchCategory   string    `json:"watch_category"`
	CloseCategory   string    `json:"close_category"`
	OriginalSide    string    `json:"original_side"`
	CreatedAt       time.Time `json:"created_at"`
}

// WebSocketManager управляет WebSocket соединениями для всех пользователей Bybit
type WebSocketManager struct {
	mu           sync.RWMutex
	userWatchers map[int64]*UserWatcher
	subService   *subscriptionservice.Service
	keysRepo     *bybit_repository.PostgresKeysRepo
	signalRepo   bybit_repository.SignalRepository
	cfg          *config.Config
	proxyService ProxyProvider
	ctx          context.Context
}

// NewWebSocketManager создает новый WebSocketManager для Bybit
func NewWebSocketManager(
	ctx context.Context,
	subService *subscriptionservice.Service,
	keysRepo *bybit_repository.PostgresKeysRepo,
	signalRepo bybit_repository.SignalRepository,
	cfg *config.Config,
	proxyService ProxyProvider,
) *WebSocketManager {
	return &WebSocketManager{
		userWatchers: make(map[int64]*UserWatcher),
		subService:   subService,
		keysRepo:     keysRepo,
		signalRepo:   signalRepo,
		cfg:          cfg,
		proxyService: proxyService,
		ctx:          ctx,
	}
}

// LoadActiveSignals загружает активные сигналы из БД при старте
func (m *WebSocketManager) LoadActiveSignals() error {
	log.Printf("BybitWebSocketManager: Loading active signals from database...")

	signals, err := m.signalRepo.GetAllActiveSignals(m.ctx)
	if err != nil {
		return err
	}

	log.Printf("BybitWebSocketManager: Found %d active signals", len(signals))

	for _, signalDB := range signals {
		watcher, err := m.GetOrCreateWatcherForUser(signalDB.UserID, signalDB.Symbol, signalDB.Category, signalDB.AutoClose)
		if err != nil {
			log.Printf("BybitWebSocketManager: ERROR: Failed to get watcher for user %d, symbol %s: %v",
				signalDB.UserID, signalDB.Symbol, err)
			continue
		}

		signal := &Signal{
			ID:              signalDB.ID,
			UserID:          signalDB.UserID,
			Symbol:          signalDB.Symbol,
			Category:        signalDB.Category,
			TargetPrice:     signalDB.TargetPrice,
			MinQuantity:     signalDB.MinQuantity,
			TriggerOnCancel: signalDB.TriggerOnCancel,
			TriggerOnEat:    signalDB.TriggerOnEat,
			EatPercentage:   signalDB.EatPercentage,
			OriginalQty:     signalDB.OriginalQty,
			LastQty:         signalDB.LastQty,
			AutoClose:       signalDB.AutoClose,
			WatchCategory:   signalDB.WatchCategory,
			CloseCategory:   signalDB.CloseCategory,
			OriginalSide:    signalDB.OriginalSide,
			CreatedAt:       signalDB.CreatedAt,
		}

		watcher.AddSignal(signal)
		log.Printf("BybitWebSocketManager: Loaded signal %d for user %d, symbol %s", signal.ID, signal.UserID, signal.Symbol)
	}

	return nil
}

// GetOrCreateWatcherForUser возвращает или создает watcher для пользователя
func (m *WebSocketManager) GetOrCreateWatcherForUser(userID int64, symbol, category string, autoClose bool) (*MarketDepthWatcher, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	userWatcher, exists := m.userWatchers[userID]
	if exists && userWatcher.watcher != nil {
		// Если watcher уже существует, проверяем нужно ли запустить PositionWatcher
		if autoClose && (userWatcher.positionWatcher == nil || !userWatcher.positionWatcher.IsRunning()) {
			m.ensurePositionWatcher(userWatcher, userID)
		}
		return userWatcher.watcher, nil
	}

	log.Printf("BybitWebSocketManager: Creating new watcher for user %d", userID)

	// Получаем ключи пользователя
	hasKeys, apiKey, secretKey := m.getKeysForUser(userID)

	var httpClient *BybitHTTPClient
	var positionWatcher *PositionWatcher

	if hasKeys {
		// Получаем прокси
		var proxyAddr string
		if m.proxyService != nil {
			if addr, ok := m.proxyService.GetProxyAddressForUser(userID); ok {
				proxyAddr = addr
			}
		}
		httpClient = NewBybitHTTPClientWithProxy(apiKey, secretKey, proxyAddr)

		// Создаем PositionWatcher ТОЛЬКО если есть auto-close сигналы
		if autoClose {
			positionWatcher = NewPositionWatcher(m.ctx, apiKey, secretKey)
			positionWatcher.SetWebSocketManager(m)
			positionWatcher.SetUserID(userID)

			clientCopy := httpClient // сохраняем для замыкания
			go func() {
				if err := positionWatcher.Start(proxyAddr, clientCopy); err != nil {
					log.Printf("BybitWebSocketManager: ERROR: Failed to start PositionWatcher for user %d: %v", userID, err)
				} else {
					log.Printf("BybitWebSocketManager: PositionWatcher started for user %d", userID)
				}
			}()
		}
	}

	// Создаем watcher
	watcher := NewMarketDepthWatcher(
		m.ctx,
		m.subService,
		m.keysRepo,
		m.cfg,
		httpClient,
		m.signalRepo,
		m,
		userID,
	)

	watcher.positionWatcher = positionWatcher

	// Сохраняем watcher
	m.userWatchers[userID] = &UserWatcher{
		watcher:          watcher,
		httpClient:       httpClient,
		signalRepository: m.signalRepo,
		positionWatcher:  positionWatcher,
	}

	return watcher, nil
}

// ensurePositionWatcher гарантирует что PositionWatcher запущен для auto-close сигналов
func (m *WebSocketManager) ensurePositionWatcher(userWatcher *UserWatcher, userID int64) {
	hasKeys, apiKey, secretKey := m.getKeysForUser(userID)
	if !hasKeys {
		return
	}

	var proxyAddr string
	if m.proxyService != nil {
		if addr, ok := m.proxyService.GetProxyAddressForUser(userID); ok {
			proxyAddr = addr
		}
	}

	positionWatcher := NewPositionWatcher(m.ctx, apiKey, secretKey)
	positionWatcher.SetWebSocketManager(m)
	userWatcher.positionWatcher = positionWatcher
	if userWatcher.watcher != nil {
		userWatcher.watcher.positionWatcher = positionWatcher
	}

	httpClientCopy := userWatcher.httpClient // сохраняем для замыкания
	go func() {
		if err := positionWatcher.Start(proxyAddr, httpClientCopy); err != nil {
			log.Printf("BybitWebSocketManager: ERROR: Failed to start PositionWatcher for user %d: %v", userID, err)
		} else {
			log.Printf("BybitWebSocketManager: PositionWatcher started for user %d (auto-close needed)", userID)
		}
	}()
}

// getKeysForUser получает и расшифровывает ключи пользователя
func (m *WebSocketManager) getKeysForUser(userID int64) (bool, string, string) {
	keys, err := m.keysRepo.GetKeys(userID)
	if err != nil {
		log.Printf("BybitWebSocketManager: No keys found for user %d: %v", userID, err)
		return false, "", ""
	}

	apiKey, err := DecryptAES(keys.APIKey, m.cfg.EncryptionSecret)
	if err != nil {
		log.Printf("BybitWebSocketManager: Failed to decrypt API key for user %d: %v", userID, err)
		return false, "", ""
	}

	secretKey, err := DecryptAES(keys.SecretKey, m.cfg.EncryptionSecret)
	if err != nil {
		log.Printf("BybitWebSocketManager: Failed to decrypt secret key for user %d: %v", userID, err)
		return false, "", ""
	}

	return true, apiKey, secretKey
}

// GetUserSignals возвращает все сигналы пользователя
func (m *WebSocketManager) GetUserSignals(userID int64) []SignalResponse {
	m.mu.RLock()
	userWatcher, exists := m.userWatchers[userID]
	m.mu.RUnlock()

	var signals []SignalResponse

	if exists && userWatcher.watcher != nil {
		activeSignalIDs := make(map[int64]bool)
		activeSignals, err := m.signalRepo.GetActiveByUserID(context.Background(), userID)
		if err != nil {
			log.Printf("BybitWebSocketManager: ERROR: Failed to get active signals from DB for user %d: %v", userID, err)
			userWatcher.watcher.mu.RLock()
			for _, signalsForSymbol := range userWatcher.watcher.signalsBySymbol {
				for _, signal := range signalsForSymbol {
					signals = append(signals, SignalResponse{
						ID:              signal.ID,
						Symbol:          signal.Symbol,
						Category:        signal.Category,
						TargetPrice:     signal.TargetPrice,
						MinQuantity:     signal.MinQuantity,
						TriggerOnCancel: signal.TriggerOnCancel,
						TriggerOnEat:    signal.TriggerOnEat,
						EatPercentage:   signal.EatPercentage,
						OriginalQty:     signal.OriginalQty,
						LastQty:         signal.LastQty,
						AutoClose:       signal.AutoClose,
						WatchCategory:   signal.WatchCategory,
						CloseCategory:   signal.CloseCategory,
						OriginalSide:    signal.OriginalSide,
						CreatedAt:       signal.CreatedAt,
					})
				}
			}
			userWatcher.watcher.mu.RUnlock()
			return signals
		}

		for _, signal := range activeSignals {
			activeSignalIDs[signal.ID] = true
		}

		userWatcher.watcher.mu.RLock()
		for _, signalsForSymbol := range userWatcher.watcher.signalsBySymbol {
			for _, signal := range signalsForSymbol {
				if signal.UserID == userID && activeSignalIDs[signal.ID] {
					signals = append(signals, SignalResponse{
						ID:              signal.ID,
						Symbol:          signal.Symbol,
						Category:        signal.Category,
						TargetPrice:     signal.TargetPrice,
						MinQuantity:     signal.MinQuantity,
						TriggerOnCancel: signal.TriggerOnCancel,
						TriggerOnEat:    signal.TriggerOnEat,
						EatPercentage:   signal.EatPercentage,
						OriginalQty:     signal.OriginalQty,
						LastQty:         signal.LastQty,
						AutoClose:       signal.AutoClose,
						WatchCategory:   signal.WatchCategory,
						CloseCategory:   signal.CloseCategory,
						OriginalSide:    signal.OriginalSide,
						CreatedAt:       signal.CreatedAt,
					})
				}
			}
		}
		userWatcher.watcher.mu.RUnlock()
	}

	return signals
}

// DeleteUserSignal удаляет сигнал пользователя по ID
func (m *WebSocketManager) DeleteUserSignal(userID int64, signalID int64) error {
	m.mu.RLock()
	userWatcher, exists := m.userWatchers[userID]
	m.mu.RUnlock()

	if !exists || userWatcher.watcher == nil {
		return m.signalRepo.Delete(m.ctx, signalID)
	}

	userWatcher.watcher.RemoveSignal(signalID)

	if err := m.signalRepo.Deactivate(m.ctx, signalID); err != nil {
		log.Printf("BybitWebSocketManager: WARNING: Failed to deactivate signal %d in database: %v", signalID, err)
	}

	go m.GracefulStopProxyForUser(userID)

	return nil
}

// CleanupUserResources очищает все ресурсы пользователя
func (m *WebSocketManager) CleanupUserResources(userID int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	userWatcher, exists := m.userWatchers[userID]
	if !exists {
		return
	}

	if userWatcher.watcher != nil {
		userWatcher.watcher.Close()
	}

	if userWatcher.positionWatcher != nil {
		userWatcher.positionWatcher.Stop()
	}

	delete(m.userWatchers, userID)
	log.Printf("BybitWebSocketManager: Cleaned up resources for user %d", userID)
}

// GetAllUserIDs возвращает список всех userID с активными watcher'ами
func (m *WebSocketManager) GetAllUserIDs() []int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	userIDs := make([]int64, 0, len(m.userWatchers))
	for userID := range m.userWatchers {
		userIDs = append(userIDs, userID)
	}
	return userIDs
}

// GetProxyAddressForUser возвращает адрес прокси для пользователя
func (m *WebSocketManager) GetProxyAddressForUser(userID int64) (string, bool) {
	if m.proxyService != nil {
		return m.proxyService.GetProxyAddressForUser(userID)
	}
	return "", false
}

// RemoveSignalFromMemory удаляет сигнал из памяти MarketDepthWatcher
func (m *WebSocketManager) RemoveSignalFromMemory(userID int64, signalID int64) {
	log.Printf("BybitWebSocketManager: Removing signal %d from memory for user %d", signalID, userID)

	m.mu.RLock()
	userWatcher, exists := m.userWatchers[userID]
	m.mu.RUnlock()

	if !exists || userWatcher == nil || userWatcher.watcher == nil {
		log.Printf("BybitWebSocketManager: No UserWatcher or MarketDepthWatcher found for user %d", userID)
		return
	}

	userWatcher.watcher.RemoveSignal(signalID)
}

// GracefulStopProxyForUser обеспечивает плавную остановку прокси и WebSocket соединений
func (m *WebSocketManager) GracefulStopProxyForUser(userID int64) {
	signals := m.GetUserSignals(userID)
	if len(signals) > 0 {
		log.Printf("BybitWebSocketManager: User %d still has %d active signals, skipping cleanup", userID, len(signals))
		return
	}

	log.Printf("BybitWebSocketManager: No active signals for user %d, stopping resources", userID)

	m.mu.Lock()
	userWatcher, exists := m.userWatchers[userID]
	m.mu.Unlock()

	if exists {
		if userWatcher.positionWatcher != nil && userWatcher.positionWatcher.IsRunning() {
			log.Printf("BybitWebSocketManager: Stopping PositionWatcher for user %d", userID)
			userWatcher.positionWatcher.Stop()
			userWatcher.positionWatcher = nil
		}
	}

	if m.proxyService == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := m.proxyService.CheckAndStopProxy(ctx, userID); err != nil {
		log.Printf("BybitWebSocketManager: ERROR: Failed to stop proxy for user %d: %v", userID, err)
	} else {
		log.Printf("BybitWebSocketManager: Checked and stopped proxy for user %d if needed", userID)
	}
}

// StartConnectionMonitor запускает мониторинг соединений
func (m *WebSocketManager) StartConnectionMonitor() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m.mu.RLock()
			for userID, userWatcher := range m.userWatchers {
				if userWatcher.watcher != nil {
					log.Printf("BybitWebSocketManager: User %d watcher status - started: %v",
						userID, userWatcher.watcher.started)
				}
			}
			m.mu.RUnlock()
		case <-m.ctx.Done():
			return
		}
	}
}
