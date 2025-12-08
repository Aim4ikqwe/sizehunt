package service

import (
	"context"
	"log"
	"sizehunt/internal/config"
	okx_repository "sizehunt/internal/okx/repository"
	subscriptionservice "sizehunt/internal/subscription/service"
	"sync"
	"time"
)

// UserWatcher хранит watcher'ы по инструментам для одного пользователя OKX
type UserWatcher struct {
	watcher          *MarketDepthWatcher
	httpClient       *OKXHTTPClient
	signalRepository okx_repository.SignalRepository
	positionWatcher  *PositionWatcher  // WebSocket для отслеживания позиций
	tradingWS        *TradingWebSocket // WebSocket для размещения ордеров
	mu               sync.Mutex
}

// SignalResponse структура для ответа API
type SignalResponse struct {
	ID              int64     `json:"id"`
	InstID          string    `json:"inst_id"`
	InstType        string    `json:"inst_type"`
	TargetPrice     float64   `json:"target_price"`
	MinQuantity     float64   `json:"min_quantity"`
	TriggerOnCancel bool      `json:"trigger_on_cancel"`
	TriggerOnEat    bool      `json:"trigger_on_eat"`
	EatPercentage   float64   `json:"eat_percentage"`
	OriginalQty     float64   `json:"original_qty"`
	LastQty         float64   `json:"last_qty"`
	AutoClose       bool      `json:"auto_close"`
	OriginalSide    string    `json:"original_side"`
	CreatedAt       time.Time `json:"created_at"`
}

// WebSocketManager управляет WebSocket соединениями для всех пользователей OKX
type WebSocketManager struct {
	mu           sync.RWMutex
	userWatchers map[int64]*UserWatcher
	subService   *subscriptionservice.Service
	keysRepo     *okx_repository.PostgresKeysRepo
	signalRepo   okx_repository.SignalRepository
	cfg          *config.Config
	proxyService ProxyProvider
	ctx          context.Context
}

// NewWebSocketManager создает новый WebSocketManager для OKX
func NewWebSocketManager(
	ctx context.Context,
	subService *subscriptionservice.Service,
	keysRepo *okx_repository.PostgresKeysRepo,
	signalRepo okx_repository.SignalRepository,
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
	log.Printf("OKXWebSocketManager: Loading active signals from database...")

	signals, err := m.signalRepo.GetAllActiveSignals(m.ctx)
	if err != nil {
		return err
	}

	log.Printf("OKXWebSocketManager: Found %d active signals", len(signals))

	for _, signalDB := range signals {
		// Получаем watcher для пользователя
		watcher, err := m.GetOrCreateWatcherForUser(signalDB.UserID, signalDB.InstID, signalDB.InstType, signalDB.AutoClose)
		if err != nil {
			log.Printf("OKXWebSocketManager: ERROR: Failed to get watcher for user %d, instID %s: %v",
				signalDB.UserID, signalDB.InstID, err)
			continue
		}

		// Создаем сигнал для мониторинга
		signal := &Signal{
			ID:              signalDB.ID,
			UserID:          signalDB.UserID,
			InstID:          signalDB.InstID,
			InstType:        signalDB.InstType,
			TargetPrice:     signalDB.TargetPrice,
			MinQuantity:     signalDB.MinQuantity,
			TriggerOnCancel: signalDB.TriggerOnCancel,
			TriggerOnEat:    signalDB.TriggerOnEat,
			EatPercentage:   signalDB.EatPercentage,
			OriginalQty:     signalDB.OriginalQty,
			LastQty:         signalDB.LastQty,
			AutoClose:       signalDB.AutoClose,
			OriginalSide:    signalDB.OriginalSide,
			CreatedAt:       signalDB.CreatedAt,
		}

		watcher.AddSignal(signal)
		log.Printf("OKXWebSocketManager: Loaded signal %d for user %d, instID %s", signal.ID, signal.UserID, signal.InstID)
	}

	return nil
}

// GetOrCreateWatcherForUser возвращает или создает watcher для пользователя
func (m *WebSocketManager) GetOrCreateWatcherForUser(userID int64, instID, instType string, autoClose bool) (*MarketDepthWatcher, error) {
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

	log.Printf("OKXWebSocketManager: Creating new watcher for user %d", userID)

	// Получаем ключи пользователя
	hasKeys, apiKey, secretKey, passphrase := m.getKeysForUser(userID)

	var httpClient *OKXHTTPClient
	var positionWatcher *PositionWatcher
	var tradingWS *TradingWebSocket

	if hasKeys {
		// Получаем прокси
		var proxyAddr string
		if m.proxyService != nil {
			if addr, ok := m.proxyService.GetProxyAddressForUser(userID); ok {
				proxyAddr = addr
			}
		}
		httpClient = NewOKXHTTPClientWithProxy(apiKey, secretKey, passphrase, proxyAddr)

		// Создаем TradingWebSocket для быстрого размещения ордеров
		tradingWS = NewTradingWebSocket(m.ctx, apiKey, secretKey, passphrase, proxyAddr)
		go func() {
			if err := tradingWS.Connect(); err != nil {
				log.Printf("OKXWebSocketManager: ERROR: Failed to connect TradingWebSocket for user %d: %v", userID, err)
			} else {
				log.Printf("OKXWebSocketManager: TradingWebSocket connected for user %d", userID)
			}
		}()

		// Создаем PositionWatcher ТОЛЬКО если есть auto-close сигналы
		if autoClose {
			positionWatcher = NewPositionWatcher(m.ctx, apiKey, secretKey, passphrase)

			// Запускаем PositionWatcher асинхронно
			go func() {
				if err := positionWatcher.Start(proxyAddr); err != nil {
					log.Printf("OKXWebSocketManager: ERROR: Failed to start PositionWatcher for user %d: %v", userID, err)
				} else {
					log.Printf("OKXWebSocketManager: PositionWatcher started for user %d", userID)
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

	// Передаем PositionWatcher и TradingWS в watcher
	watcher.positionWatcher = positionWatcher
	watcher.tradingWS = tradingWS

	// Сохраняем watcher
	m.userWatchers[userID] = &UserWatcher{
		watcher:          watcher,
		httpClient:       httpClient,
		signalRepository: m.signalRepo,
		positionWatcher:  positionWatcher,
		tradingWS:        tradingWS,
	}

	return watcher, nil
}

// ensurePositionWatcher гарантирует что PositionWatcher запущен для auto-close сигналов
func (m *WebSocketManager) ensurePositionWatcher(userWatcher *UserWatcher, userID int64) {
	// Получаем ключи
	hasKeys, apiKey, secretKey, passphrase := m.getKeysForUser(userID)
	if !hasKeys {
		return
	}

	// Получаем прокси
	var proxyAddr string
	if m.proxyService != nil {
		if addr, ok := m.proxyService.GetProxyAddressForUser(userID); ok {
			proxyAddr = addr
		}
	}

	// Создаем и запускаем PositionWatcher
	positionWatcher := NewPositionWatcher(m.ctx, apiKey, secretKey, passphrase)
	userWatcher.positionWatcher = positionWatcher
	if userWatcher.watcher != nil {
		userWatcher.watcher.positionWatcher = positionWatcher
	}

	go func() {
		if err := positionWatcher.Start(proxyAddr); err != nil {
			log.Printf("OKXWebSocketManager: ERROR: Failed to start PositionWatcher for user %d: %v", userID, err)
		} else {
			log.Printf("OKXWebSocketManager: PositionWatcher started for user %d (auto-close needed)", userID)
		}
	}()
}

// getKeysForUser получает и расшифровывает ключи пользователя
func (m *WebSocketManager) getKeysForUser(userID int64) (bool, string, string, string) {
	keys, err := m.keysRepo.GetKeys(userID)
	if err != nil {
		log.Printf("OKXWebSocketManager: No keys found for user %d: %v", userID, err)
		return false, "", "", ""
	}

	apiKey, err := DecryptAES(keys.APIKey, m.cfg.EncryptionSecret)
	if err != nil {
		log.Printf("OKXWebSocketManager: Failed to decrypt API key for user %d: %v", userID, err)
		return false, "", "", ""
	}

	secretKey, err := DecryptAES(keys.SecretKey, m.cfg.EncryptionSecret)
	if err != nil {
		log.Printf("OKXWebSocketManager: Failed to decrypt secret key for user %d: %v", userID, err)
		return false, "", "", ""
	}

	passphrase, err := DecryptAES(keys.Passphrase, m.cfg.EncryptionSecret)
	if err != nil {
		log.Printf("OKXWebSocketManager: Failed to decrypt passphrase for user %d: %v", userID, err)
		return false, "", "", ""
	}

	return true, apiKey, secretKey, passphrase
}

// GetUserSignals возвращает все сигналы пользователя
func (m *WebSocketManager) GetUserSignals(userID int64) []SignalResponse {
	m.mu.RLock()
	userWatcher, exists := m.userWatchers[userID]
	m.mu.RUnlock()

	var signals []SignalResponse

	if exists && userWatcher.watcher != nil {
		userWatcher.watcher.mu.RLock()
		for _, signalsForInst := range userWatcher.watcher.signalsByInstID {
			for _, signal := range signalsForInst {
				signals = append(signals, SignalResponse{
					ID:              signal.ID,
					InstID:          signal.InstID,
					InstType:        signal.InstType,
					TargetPrice:     signal.TargetPrice,
					MinQuantity:     signal.MinQuantity,
					TriggerOnCancel: signal.TriggerOnCancel,
					TriggerOnEat:    signal.TriggerOnEat,
					EatPercentage:   signal.EatPercentage,
					OriginalQty:     signal.OriginalQty,
					LastQty:         signal.LastQty,
					AutoClose:       signal.AutoClose,
					OriginalSide:    signal.OriginalSide,
					CreatedAt:       signal.CreatedAt,
				})
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
		// Попробуем удалить из БД напрямую
		return m.signalRepo.Delete(m.ctx, signalID)
	}

	// Удаляем из watcher'а
	userWatcher.watcher.RemoveSignal(signalID)

	// Удаляем из БД
	if err := m.signalRepo.Deactivate(m.ctx, signalID); err != nil {
		log.Printf("OKXWebSocketManager: WARNING: Failed to deactivate signal %d in database: %v", signalID, err)
	}

	// Проверяем и очищаем ресурсы если сигналов не осталось
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

	delete(m.userWatchers, userID)
	log.Printf("OKXWebSocketManager: Cleaned up resources for user %d", userID)
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

// GracefulStopProxyForUser обеспечивает плавную остановку прокси и WebSocket соединений для пользователя
func (m *WebSocketManager) GracefulStopProxyForUser(userID int64) {
	// Проверяем, остались ли у пользователя активные сигналы
	signals := m.GetUserSignals(userID)
	if len(signals) > 0 {
		log.Printf("OKXWebSocketManager: User %d still has %d active signals, skipping cleanup", userID, len(signals))
		return
	}

	log.Printf("OKXWebSocketManager: No active signals for user %d, stopping resources", userID)

	m.mu.Lock()
	userWatcher, exists := m.userWatchers[userID]
	m.mu.Unlock()

	if exists {
		// Останавливаем TradingWebSocket
		if userWatcher.tradingWS != nil && userWatcher.tradingWS.IsConnected() {
			log.Printf("OKXWebSocketManager: Stopping TradingWebSocket for user %d", userID)
			userWatcher.tradingWS.Close()
			userWatcher.tradingWS = nil
		}

		// Останавливаем PositionWatcher
		if userWatcher.positionWatcher != nil && userWatcher.positionWatcher.IsRunning() {
			log.Printf("OKXWebSocketManager: Stopping PositionWatcher for user %d", userID)
			userWatcher.positionWatcher.Stop()
			userWatcher.positionWatcher = nil
		}
	}

	if m.proxyService == nil {
		return
	}

	// Останавливаем прокси
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := m.proxyService.StopProxyForUser(ctx, userID); err != nil {
		log.Printf("OKXWebSocketManager: ERROR: Failed to stop proxy for user %d: %v", userID, err)
	} else {
		log.Printf("OKXWebSocketManager: Proxy stopped for user %d", userID)
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
					log.Printf("OKXWebSocketManager: User %d watcher status - started: %v",
						userID, userWatcher.watcher.started)
				}
			}
			m.mu.RUnlock()
		case <-m.ctx.Done():
			return
		}
	}
}
