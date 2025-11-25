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
	futuresClient    *futures.Client
	positionWatcher  *PositionWatcher
	userDataStream   *UserDataStream
	signalRepository repository.SignalRepository
	// флаг, что userDataStream запущен
	userDataStreamStarted bool
	mu                    sync.Mutex // локальный мьютекс для UserWatcher
}

type WebSocketManager struct {
	mu           sync.RWMutex
	userWatchers map[int64]*UserWatcher // userID → UserWatcher
	subService   *subscriptionservice.Service
	keysRepo     *repository.PostgresKeysRepo
	signalRepo   repository.SignalRepository
	cfg          *config.Config
	ctx          context.Context
}

func NewWebSocketManager(
	ctx context.Context,
	subService *subscriptionservice.Service,
	keysRepo *repository.PostgresKeysRepo,
	signalRepo repository.SignalRepository,
	cfg *config.Config,
) *WebSocketManager {
	manager := &WebSocketManager{
		userWatchers: make(map[int64]*UserWatcher),
		subService:   subService,
		keysRepo:     keysRepo,
		signalRepo:   signalRepo,
		cfg:          cfg,
		ctx:          ctx,
	}
	log.Println("WebSocketManager: Initialized successfully")
	return manager
}

// Загружаем все активные сигналы из БД при старте
func (m *WebSocketManager) LoadActiveSignals() error {
	// Получаем все уникальные user_id из активных сигналов
	query := `SELECT DISTINCT user_id FROM signals WHERE is_active = true`
	rows, err := m.signalRepo.(*repository.PostgresSignalRepo).DB.Query(query)
	if err != nil {
		return fmt.Errorf("failed to get active users: %w", err)
	}
	defer rows.Close()

	var userIDs []int64
	for rows.Next() {
		var userID int64
		if err := rows.Scan(&userID); err != nil {
			return fmt.Errorf("failed to scan user_id: %w", err)
		}
		userIDs = append(userIDs, userID)
	}

	// Для каждого пользователя загружаем его сигналы
	for _, userID := range userIDs {
		// Загружаем сигналы асинхронно для каждого пользователя
		go func(userID int64) {
			signals, err := m.signalRepo.GetActiveByUserID(context.Background(), userID)
			if err != nil {
				log.Printf("WebSocketManager: WARNING: Failed to load signals for user %d: %v", userID, err)
				return
			}
			log.Printf("WebSocketManager: Loading %d active signals for user %d", len(signals), userID)

			// Создаем UserWatcher для пользователя (без инициализации ресурсов)
			m.mu.Lock()
			uw, exists := m.userWatchers[userID]
			if !exists {
				uw = &UserWatcher{
					spotWatchers:     make(map[string]*MarketDepthWatcher),
					futuresWatchers:  make(map[string]*MarketDepthWatcher),
					signalRepository: m.signalRepo,
				}
				m.userWatchers[userID] = uw
			}
			m.mu.Unlock()

			// Инициализируем watcher'ы и добавляем сигналы
			for _, signalDB := range signals {
				// Копируем данные для использования в горутине
				signalCopy := signalDB

				// Создаем watcher без блокировки основного мьютекса
				watcher, err := m.createWatcherForUser(userID, signalCopy.Symbol, signalCopy.WatchMarket, signalCopy.AutoClose)
				if err != nil {
					log.Printf("WebSocketManager: ERROR: Failed to create watcher for signal %d: %v", signalCopy.ID, err)
					continue
				}

				// Добавляем сигнал асинхронно
				go watcher.AddSignalAsync(&Signal{
					ID:              signalCopy.ID,
					UserID:          signalCopy.UserID,
					Symbol:          signalCopy.Symbol,
					TargetPrice:     signalCopy.TargetPrice,
					MinQuantity:     signalCopy.MinQuantity,
					TriggerOnCancel: signalCopy.TriggerOnCancel,
					TriggerOnEat:    signalCopy.TriggerOnEat,
					EatPercentage:   signalCopy.EatPercentage,
					OriginalQty:     signalCopy.OriginalQty,
					LastQty:         signalCopy.LastQty,
					AutoClose:       signalCopy.AutoClose,
					CloseMarket:     signalCopy.CloseMarket,
					WatchMarket:     signalCopy.WatchMarket,
					OriginalSide:    signalCopy.OriginalSide,
					CreatedAt:       signalCopy.CreatedAt,
				})

				log.Printf("WebSocketManager: Loaded signal %d for user %d, symbol %s", signalCopy.ID, userID, signalCopy.Symbol)
			}
		}(userID)
	}

	return nil
}

// createWatcherForUser создает watcher для пользователя без блокировки мьютекса на длительные операции
func (m *WebSocketManager) createWatcherForUser(userID int64, symbol, market string, autoClose bool) (*MarketDepthWatcher, error) {
	// 1. Получаем или создаем UserWatcher под мьютексом
	m.mu.Lock()
	uw, exists := m.userWatchers[userID]
	if !exists {
		log.Printf("WebSocketManager: Creating new UserWatcher for user %d", userID)
		uw = &UserWatcher{
			spotWatchers:     make(map[string]*MarketDepthWatcher),
			futuresWatchers:  make(map[string]*MarketDepthWatcher),
			signalRepository: m.signalRepo,
		}
		m.userWatchers[userID] = uw
	} else {
		log.Printf("WebSocketManager: Found existing UserWatcher for user %d", userID)
	}
	m.mu.Unlock()

	// 2. Работа с конкретным рынком - используем локальный мьютекс UserWatcher
	uw.mu.Lock()
	defer uw.mu.Unlock()

	var watcher *MarketDepthWatcher

	switch market {
	case "spot":
		// Проверяем существующий watcher
		if w, exists := uw.spotWatchers[symbol]; exists && w != nil {
			log.Printf("WebSocketManager: Found existing spot watcher for user %d, symbol %s", userID, symbol)
			return w, nil
		}

		// Создаем новый watcher (без инициализации соединения)
		log.Printf("WebSocketManager: Creating new spot watcher for user %d, symbol %s", userID, symbol)
		watcher = NewMarketDepthWatcher(
			m.ctx, "spot", m.subService, m.keysRepo, m.cfg, nil, nil, nil, m.signalRepo,
		)
		uw.spotWatchers[symbol] = watcher

	case "futures":
		// Удаляем старый watcher если он существует (асинхронно)
		if w, exists := uw.futuresWatchers[symbol]; exists && w != nil {
			log.Printf("WebSocketManager: Found existing futures watcher for user %d, symbol %s - will be replaced", userID, symbol)
			go m.cleanupOldWatcherAsync(w, symbol, userID)
		}

		// Если требуется autoClose - проверяем и инициализируем ресурсы
		var futuresClient *futures.Client
		var positionWatcher *PositionWatcher
		var userDataStream *UserDataStream

		if autoClose {
			log.Printf("WebSocketManager: AutoClose enabled - preparing futures resources for user %d", userID)

			// Получаем ключи пользователя
			okKeys, apiKey, secretKey := m.getKeysForUser(userID)
			if !okKeys {
				log.Printf("WebSocketManager: ERROR: Failed to get valid API keys for user %d", userID)
				return nil, fmt.Errorf("futures auto-close requires valid API keys")
			}

			// Создаем новые ресурсы
			log.Printf("WebSocketManager: Creating new futures client for user %d", userID)
			futuresClient = futures.NewClient(apiKey, secretKey)
			positionWatcher = NewPositionWatcher()

			// Инициализируем позиции через REST API в отдельной горутине
			userDataStream = NewUserDataStream(futuresClient, positionWatcher)
			go func() {
				if err := userDataStream.Start(symbol); err != nil {
					log.Printf("WebSocketManager: ERROR: Failed to start UserDataStream for user %d: %v", userID, err)
				} else {
					log.Printf("WebSocketManager: UserDataStream successfully started for user %d", userID)

					// Обновляем флаг в безопасном режиме
					uw.mu.Lock()
					uw.userDataStreamStarted = true
					uw.futuresClient = futuresClient
					uw.positionWatcher = positionWatcher
					uw.userDataStream = userDataStream
					uw.mu.Unlock()
				}
			}()
		}

		// Создаем новый watcher
		log.Printf("WebSocketManager: Creating new futures watcher for user %d, symbol %s", userID, symbol)
		watcher = NewMarketDepthWatcher(
			m.ctx,
			"futures",
			m.subService,
			m.keysRepo,
			m.cfg,
			futuresClient,
			positionWatcher,
			userDataStream,
			m.signalRepo,
		)
		uw.futuresWatchers[symbol] = watcher

	default:
		log.Printf("WebSocketManager: ERROR: Unsupported market type: %s", market)
		return nil, fmt.Errorf("unsupported market: %s", market)
	}

	log.Printf("WebSocketManager: New watcher created for user %d, symbol %s, market %s", userID, symbol, market)

	// 3. Запускаем соединение асинхронно
	go func() {
		if err := watcher.StartConnection(); err != nil {
			log.Printf("WebSocketManager: ERROR: Failed to start connection for user %d, symbol %s: %v", userID, symbol, err)
		}
	}()

	return watcher, nil
}

// GetOrCreateWatcherForUser возвращает watcher для конкретного пользователя и символа
func (m *WebSocketManager) GetOrCreateWatcherForUser(userID int64, symbol, market string, autoClose bool) (*MarketDepthWatcher, error) {
	startTime := time.Now()
	log.Printf("WebSocketManager: GetOrCreateWatcherForUser called for user %d, symbol %s, market %s, autoClose %v",
		userID, symbol, market, autoClose)
	defer func() {
		log.Printf("WebSocketManager: GetOrCreateWatcherForUser completed for user %d, symbol %s (total time: %v)",
			userID, symbol, time.Since(startTime))
	}()

	return m.createWatcherForUser(userID, symbol, market, autoClose)
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

	apiKey, err := DecryptAES(keys.APIKey, m.cfg.EncryptionSecret)
	if err != nil {
		log.Printf("WebSocketManager: ERROR: Failed to decrypt API key for user %d: %v", userID, err)
		return false, "", ""
	}

	secretKey, err := DecryptAES(keys.SecretKey, m.cfg.EncryptionSecret)
	if err != nil {
		log.Printf("WebSocketManager: ERROR: Failed to decrypt Secret key for user %d: %v", userID, err)
		return false, "", ""
	}

	log.Printf("WebSocketManager: Successfully retrieved keys for user %d", userID)
	return true, apiKey, secretKey
}

// cleanupOldWatcherAsync асинхронно закрывает старый watcher и освобождает ресурсы
func (m *WebSocketManager) cleanupOldWatcherAsync(watcher *MarketDepthWatcher, symbol string, userID int64) {
	startTime := time.Now()
	log.Printf("WebSocketManager: Starting async cleanup for symbol %s, user %d", symbol, userID)

	defer func() {
		log.Printf("WebSocketManager: Async cleanup completed for symbol %s, user %d (total time: %v)", symbol, userID, time.Since(startTime))
	}()

	if watcher == nil {
		return
	}

	// Асинхронно закрываем соединение
	go func() {
		if watcher.client != nil {
			log.Printf("WebSocketManager: Closing WebSocket client for symbol %s, user %d", symbol, userID)
			watcher.client.Close()
			log.Printf("WebSocketManager: WebSocket client closed for symbol %s, user %d", symbol, userID)
		}

		// Удаляем сигналы для символа
		log.Printf("WebSocketManager: Removing all signals for symbol %s, user %d", symbol, userID)
		watcher.RemoveAllSignalsForSymbol(symbol)
		log.Printf("WebSocketManager: All signals removed for symbol %s, user %d", symbol, userID)
	}()
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

	// Очищаем спотовые watcher'ы асинхронно
	for symbol, watcher := range uw.spotWatchers {
		go m.cleanupOldWatcherAsync(watcher, symbol, userID)
	}

	// Очищаем фьючерсные watcher'ы асинхронно
	for symbol, watcher := range uw.futuresWatchers {
		go m.cleanupOldWatcherAsync(watcher, symbol, userID)
	}

	// Останавливаем userDataStream асинхронно
	if uw.userDataStream != nil {
		go func() {
			log.Printf("WebSocketManager: Stopping userDataStream for user %d", userID)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			uw.userDataStream.StopWithContext(ctx)
			log.Printf("WebSocketManager: UserDataStream stopped for user %d", userID)
		}()
	}

	log.Printf("WebSocketManager: Cleanup initiated for user %d resources", userID)
}

// GetUserSignals возвращает все сигналы пользователя
func (m *WebSocketManager) GetUserSignals(userID int64) []SignalResponse {
	startTime := time.Now()
	defer func() {
		log.Printf("WebSocketManager: GetUserSignals for user %d completed (total time: %v)", userID, time.Since(startTime))
	}()

	m.mu.RLock()
	uw, exists := m.userWatchers[userID]
	m.mu.RUnlock()

	if !exists {
		return []SignalResponse{}
	}

	// Собираем все сигналы без блокировки
	var allSignals []*Signal

	// Собираем сигналы из spot watchers
	uw.mu.Lock()
	for _, watcher := range uw.spotWatchers {
		signals := watcher.GetAllSignals()
		allSignals = append(allSignals, signals...)
	}

	// Собираем сигналы из futures watchers
	for _, watcher := range uw.futuresWatchers {
		signals := watcher.GetAllSignals()
		allSignals = append(allSignals, signals...)
	}
	uw.mu.Unlock()

	// Конвертируем в ответ без блокировки
	var signals []SignalResponse
	for _, s := range allSignals {
		if s.UserID == userID {
			signals = append(signals, convertSignalToResponse(s))
		}
	}

	return signals
}

// DeleteUserSignal удаляет сигнал пользователя по ID
func (m *WebSocketManager) DeleteUserSignal(userID int64, signalID int64) error {
	startTime := time.Time{}
	defer func() {
		log.Printf("WebSocketManager: DeleteUserSignal completed (total time: %v)", time.Since(startTime))
	}()

	startTime = time.Now()

	m.mu.RLock()
	uw, exists := m.userWatchers[userID]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("user not found")
	}

	// Ищем сигнал и удаляем его
	var found bool
	var marketType string
	var symbol string

	uw.mu.Lock()
	// Ищем в спотовых watcher'ах
	for sym, watcher := range uw.spotWatchers {
		if watcher.HasSignal(signalID) {
			found = true
			marketType = "spot"
			symbol = sym
			break
		}
	}

	// Если не нашли, ищем в фьючерсных
	if !found {
		for sym, watcher := range uw.futuresWatchers {
			if watcher.HasSignal(signalID) {
				found = true
				marketType = "futures"
				symbol = sym
				break
			}
		}
	}
	uw.mu.Unlock()

	if !found {
		return fmt.Errorf("signal not found")
	}

	// Удаляем сигнал из watcher'а асинхронно
	go func() {
		m.mu.RLock()
		uw, exists := m.userWatchers[userID]
		m.mu.RUnlock()

		if !exists {
			return
		}

		uw.mu.Lock()
		var watcher *MarketDepthWatcher
		if marketType == "spot" {
			watcher = uw.spotWatchers[symbol]
		} else {
			watcher = uw.futuresWatchers[symbol]
		}
		uw.mu.Unlock()

		if watcher != nil {
			log.Printf("WebSocketManager: Removing signal %d from watcher", signalID)
			watcher.RemoveSignal(signalID)
		}
	}()

	// Удаляем сигнал из базы данных асинхронно
	go func() {
		if err := m.signalRepo.Delete(context.Background(), signalID); err != nil {
			log.Printf("WebSocketManager: WARNING: Failed to delete signal %d from database: %v", signalID, err)
		} else {
			log.Printf("WebSocketManager: Signal %d deleted from database", signalID)
		}
	}()

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
