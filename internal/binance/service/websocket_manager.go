// internal/binance/service/websocket_manager.go
package service

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sizehunt/internal/binance/repository"
	"sizehunt/internal/config"
	subscriptionservice "sizehunt/internal/subscription/service"
	"strconv"
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
	userDataStreamStarted     bool
	hasActiveAutoCloseSignals bool
	mu                        sync.Mutex // локальный мьютекс для UserWatcher
}

type WebSocketManager struct {
	mu                sync.RWMutex
	userWatchers      map[int64]*UserWatcher // userID → UserWatcher
	subService        *subscriptionservice.Service
	keysRepo          *repository.PostgresKeysRepo
	signalRepo        repository.SignalRepository
	cfg               *config.Config
	ctx               context.Context
	networkStatus     string // "up" или "down"
	lastNetworkChange time.Time
	networkMu         sync.Mutex
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
	log.Println("WebSocketManager: Loading active signals from database")

	// Получаем все активные сигналы
	signals, err := m.signalRepo.GetAllActiveSignals(context.Background())
	if err != nil {
		return fmt.Errorf("failed to get all active signals: %w", err)
	}
	inactiveAutoCloseSignals, err := m.signalRepo.GetInactiveAutoCloseSignals(context.Background())
	if err != nil {
		log.Printf("WebSocketManager: WARNING: Failed to get inactive auto-close signals: %v", err)
	}
	_ = append(signals, inactiveAutoCloseSignals...)
	log.Printf("WebSocketManager: Found %d active signals and %d inactive auto-close signals in database", len(signals), len(inactiveAutoCloseSignals))

	log.Printf("WebSocketManager: Found %d active signals in database", len(signals))

	// Группируем сигналы по пользователю
	userSignals := make(map[int64][]*repository.SignalDB)
	for _, signal := range signals {
		userSignals[signal.UserID] = append(userSignals[signal.UserID], signal)
	}

	// Для каждого пользователя загружаем его сигналы
	for userID, signals := range userSignals {
		log.Printf("WebSocketManager: Processing %d signals for user %d", len(signals), userID)

		// Создаем UserWatcher для пользователя
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

		// Обрабатываем каждый сигнал пользователя
		for _, signalDB := range signals {
			// Проверяем права доступа к API ключам пользователя
			hasKeys, apiKey, secretKey := m.getKeysForUser(userID)
			if !hasKeys {
				log.Printf("WebSocketManager: WARNING: No API keys for user %d. Signal %d for %s will not work properly.",
					userID, signalDB.ID, signalDB.Symbol)
			}

			// Проверяем позицию для сигналов с auto-close
			if signalDB.AutoClose && hasKeys {
				// Создаем временный клиент для проверки позиции
				tempFuturesClient := futures.NewClient(apiKey, secretKey)
				resp, err := tempFuturesClient.NewGetPositionRiskService().Symbol(signalDB.Symbol).Do(context.Background())
				if err != nil {
					log.Printf("WebSocketManager: WARNING: Failed to check position for signal %d: %v", signalDB.ID, err)
				} else if len(resp) > 0 {
					positionAmt, _ := strconv.ParseFloat(resp[0].PositionAmt, 64)
					if positionAmt == 0 {
						log.Printf("WebSocketManager: INFO: Position is zero for signal %d symbol %s. Deactivating signal.",
							signalDB.ID, signalDB.Symbol)
						// Деактивируем сигнал, так как позиция нулевая
						if err := m.signalRepo.Deactivate(context.Background(), signalDB.ID); err != nil {
							log.Printf("WebSocketManager: WARNING: Failed to deactivate signal %d: %v", signalDB.ID, err)
						}
						continue
					}
				}
			}

			log.Printf("WebSocketManager: Loading signal %d for user %d, symbol %s",
				signalDB.ID, userID, signalDB.Symbol)

			// Создаем watcher для сигнала
			watcher, err := m.createWatcherForUser(userID, signalDB.Symbol, signalDB.WatchMarket, signalDB.AutoClose)
			if err != nil {
				log.Printf("WebSocketManager: ERROR: Failed to create watcher for signal %d: %v", signalDB.ID, err)
				continue
			}

			// Добавляем сигнал в watcher
			watcher.AddSignal(&Signal{
				ID:              signalDB.ID,
				UserID:          signalDB.UserID,
				Symbol:          signalDB.Symbol,
				TargetPrice:     signalDB.TargetPrice,
				MinQuantity:     signalDB.MinQuantity,
				TriggerOnCancel: signalDB.TriggerOnCancel,
				TriggerOnEat:    signalDB.TriggerOnEat,
				EatPercentage:   signalDB.EatPercentage,
				OriginalQty:     signalDB.OriginalQty,
				LastQty:         signalDB.LastQty,
				AutoClose:       signalDB.AutoClose,
				CloseMarket:     signalDB.CloseMarket,
				WatchMarket:     signalDB.WatchMarket,
				OriginalSide:    signalDB.OriginalSide,
				CreatedAt:       signalDB.CreatedAt,
			})
		}
	}

	log.Println("WebSocketManager: All active signals loaded successfully")
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
			spotWatchers:              make(map[string]*MarketDepthWatcher),
			futuresWatchers:           make(map[string]*MarketDepthWatcher),
			signalRepository:          m.signalRepo,
			hasActiveAutoCloseSignals: false,
		}
		m.userWatchers[userID] = uw
	} else {
		log.Printf("WebSocketManager: Found existing UserWatcher for user %d", userID)
	}
	m.mu.Unlock()

	// 2. Работа с конкретным рынком - используем локальный мьютекс UserWatcher
	uw.mu.Lock()
	defer uw.mu.Unlock()

	// Обновляем флаг наличия активных auto-close сигналов
	if autoClose {
		uw.hasActiveAutoCloseSignals = true
		log.Printf("WebSocketManager: User %d has active auto-close signals", userID)
	}

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
			m.ctx, "spot", m.subService, m.keysRepo, m.cfg, nil, nil, nil, m.signalRepo, m,
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

			// Создаем или используем существующие ресурсы
			if uw.futuresClient == nil {
				log.Printf("WebSocketManager: Creating new futures client for user %d", userID)
				uw.futuresClient = futures.NewClient(apiKey, secretKey)
				uw.positionWatcher = NewPositionWatcher()
				uw.userDataStream = NewUserDataStream(uw.futuresClient, uw.positionWatcher)

				// Инициализируем позиции для ВСЕХ активных сигналов пользователя
				go func() {
					// Получаем все активные сигналы пользователя
					signals, err := m.signalRepo.GetActiveByUserID(context.Background(), userID)
					if err != nil {
						log.Printf("WebSocketManager: ERROR: Failed to get active signals for user %d: %v", userID, err)
					} else {
						// Собираем уникальные символы
						symbols := make(map[string]bool)
						for _, signal := range signals {
							if signal.AutoClose && signal.WatchMarket == "futures" {
								symbols[signal.Symbol] = true
							}
						}

						// Инициализируем позиции для всех символов
						for symbol := range symbols {
							log.Printf("WebSocketManager: Initializing position for symbol %s from REST API", symbol)
							if err := uw.userDataStream.initializePositionForSymbol(symbol); err != nil {
								log.Printf("WebSocketManager: WARNING: Failed to initialize position for %s: %v", symbol, err)
							}
						}
					}

					// Запускаем UserDataStream
					if err := uw.userDataStream.Start(); err != nil {
						log.Printf("WebSocketManager: ERROR: Failed to start UserDataStream for user %d: %v", userID, err)
					} else {
						log.Printf("WebSocketManager: UserDataStream successfully started for user %d", userID)
					}
				}()
			} else {
				log.Printf("WebSocketManager: Using existing futures resources for user %d", userID)
			}

			futuresClient = uw.futuresClient
			positionWatcher = uw.positionWatcher
			userDataStream = uw.userDataStream
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
			m,
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
func (m *WebSocketManager) CheckAndStopUserDataStream(userID int64) {
	log.Printf("WebSocketManager: Checking if UserDataStream needs to be stopped for user %d", userID)

	m.mu.RLock()
	uw, exists := m.userWatchers[userID]
	m.mu.RUnlock()

	if !exists || uw == nil {
		log.Printf("WebSocketManager: UserWatcher not found for user %d", userID)
		return
	}

	uw.mu.Lock()
	defer uw.mu.Unlock()

	// Проверяем, есть ли еще активные сигналы с autoClose
	hasActiveAutoClose := false

	// Проверяем все сигналы во всех watcher'ах
	for _, watcher := range uw.futuresWatchers {
		signals := watcher.GetAllSignals()
		for _, signal := range signals {
			if signal.AutoClose {
				log.Printf("WebSocketManager: Found active auto-close signal %d for user %d", signal.ID, userID)
				hasActiveAutoClose = true
				break
			}
		}
		if hasActiveAutoClose {
			break
		}
	}

	// Если нет активных сигналов с autoClose и есть userDataStream, останавливаем его
	if !hasActiveAutoClose && uw.userDataStream != nil {
		log.Printf("WebSocketManager: No active auto-close signals for user %d, stopping UserDataStream", userID)
		stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		uw.userDataStream.StopWithContext(stopCtx)
		uw.userDataStream = nil
		uw.futuresClient = nil
		uw.positionWatcher = nil
		log.Printf("WebSocketManager: UserDataStream stopped for user %d", userID)
	} else if hasActiveAutoClose {
		log.Printf("WebSocketManager: User %d still has active auto-close signals, keeping UserDataStream active", userID)
	} else {
		log.Printf("WebSocketManager: No UserDataStream to stop for user %d", userID)
	}
}
func (m *WebSocketManager) StartConnectionMonitor() {
	log.Println("WebSocketManager: Starting connection monitor")
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	client := http.Client{
		Timeout: 2 * time.Second,
	}

	// Инициализируем статус сети
	m.networkStatus = "up"
	m.lastNetworkChange = time.Now()

	for {
		select {
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.binance.com/api/v3/ping", nil)
			resp, err := client.Do(req)

			networkUp := (err == nil && resp != nil && resp.StatusCode == http.StatusOK)
			if resp != nil {
				resp.Body.Close()
			}

			m.networkMu.Lock()
			prevStatus := m.networkStatus
			if networkUp {
				m.networkStatus = "up"
				if prevStatus == "down" {
					m.lastNetworkChange = time.Now()
					log.Printf("WebSocketManager: Network restored at %v", m.lastNetworkChange)
					// Запускаем восстановление auto-close в отдельной горутине
					go m.restoreAutoCloseFeatures()
				}
			} else {
				m.networkStatus = "down"
				if prevStatus == "up" {
					m.lastNetworkChange = time.Now()
					log.Printf("WebSocketManager: Network lost at %v", m.lastNetworkChange)
					m.handleNetworkDown()
				}
			}
			m.networkMu.Unlock()

		case <-m.ctx.Done():
			log.Println("WebSocketManager: Connection monitor stopped")
			return
		}
	}
}

func (m *WebSocketManager) checkCircuitBreakers() {
	// Здесь можно добавить логику для проверки состояния circuit breakers
	// Например, отправка метрик в мониторинг
}

func (m *WebSocketManager) handleNetworkDown() {
	log.Printf("WebSocketManager: Network down, temporarily disabling auto-close features")

	userIDs := m.GetAllUserIDs()

	for _, userID := range userIDs {
		signals := m.GetUserSignals(userID)

		for _, signal := range signals {
			if signal.AutoClose {
				log.Printf("WebSocketManager: Temporarily deactivating auto-close for signal %d (user %d, symbol %s) due to network issues",
					signal.ID, userID, signal.Symbol)

				// Обновляем статус в БД, но сохраняем флаг auto_close = true для последующего восстановления
				signalDB := &repository.SignalDB{
					ID:        signal.ID,
					IsActive:  false, // Временно деактивируем
					LastQty:   signal.LastQty,
					AutoClose: true, // Сохраняем флаг для восстановления
				}

				if err := m.signalRepo.Update(context.Background(), signalDB); err != nil {
					log.Printf("WebSocketManager: ERROR: Failed to update signal %d during network down: %v", signal.ID, err)
				}
			}
		}
	}

	// Не удаляем полностью сигналы, а только деактивируем их для восстановления позже
	log.Printf("WebSocketManager: All auto-close features temporarily disabled due to network issues")
}
func (m *WebSocketManager) disableAutoCloseForUser(userID int64) {
	log.Printf("WebSocketManager: Disabling auto-close for user %d due to network issues", userID)

	m.mu.RLock()
	uw, exists := m.userWatchers[userID]
	m.mu.RUnlock()

	if !exists || uw == nil {
		return
	}

	uw.mu.Lock()
	defer uw.mu.Unlock()

	var affectedSignals []int64

	// Отключаем auto-close для всех фьючерсных сигналов
	for symbol, watcher := range uw.futuresWatchers {
		signals := watcher.GetAllSignals()
		for _, signal := range signals {
			if signal.AutoClose {
				signal.AutoClose = false
				affectedSignals = append(affectedSignals, signal.ID)
				log.Printf("WebSocketManager: Auto-close disabled for signal %d (user %d, symbol %s) due to network issues",
					signal.ID, userID, symbol)
			}
		}
	}

	if len(affectedSignals) > 0 {
		// Обновляем сигналы в БД
		go func() {
			for _, signalID := range affectedSignals {
				if err := m.signalRepo.Update(context.Background(), &repository.SignalDB{
					ID:        signalID,
					AutoClose: false,
				}); err != nil {
					log.Printf("WebSocketManager: ERROR: Failed to update signal %d in database: %v", signalID, err)
				}
			}
			// Отправляем уведомление пользователю
			go m.notifyUserAboutNetworkIssue(userID)
		}()
	}
}
func (m *WebSocketManager) notifyUserAboutNetworkIssue(userID int64) {
	// Здесь можно реализовать отправку уведомления пользователю
	// Например, через email, push-уведомление или сохранение в БД
	log.Printf("NOTIFICATION: User %d - Auto-close features disabled due to network connection issues. Please manage your positions manually.", userID)

	// Пример сохранения в БД для отображения в интерфейсе:
	// notification := &Notification{
	//     UserID:  userID,
	//     Message: "Auto-close features disabled due to network connection issues. Please manage your positions manually.",
	//     Type:    "network_issue",
	// }
	// m.notificationRepo.Save(notification)
}
func (m *WebSocketManager) restoreAutoCloseFeatures() {
	log.Println("WebSocketManager: Restoring auto-close features for all users")

	// Получаем всех пользователей с активными сигналами
	userIDs := m.GetAllUserIDs()

	for _, userID := range userIDs {
		log.Printf("WebSocketManager: Checking signals for user %d", userID)
		signals := m.GetUserSignals(userID)

		for _, signal := range signals {
			// Проверяем, был ли сигнал с auto-close отключен из-за потери сети
			if signal.AutoClose {
				log.Printf("WebSocketManager: Restoring auto-close for signal %d (user %d, symbol %s)",
					signal.ID, userID, signal.Symbol)

				// Загружаем актуальные данные из БД
				signalDB, err := m.signalRepo.GetByID(context.Background(), signal.ID)
				if err != nil {
					log.Printf("WebSocketManager: ERROR: Failed to get signal %d from DB: %v", signal.ID, err)
					continue
				}

				// Если сигнал был деактивирован из-за сети, проверяем позицию и восстанавливаем
				if !signalDB.IsActive {
					// Проверяем текущую позицию на Binance
					posAmt, err := m.checkPositionForSignal(signalDB)
					if err != nil {
						log.Printf("WebSocketManager: WARNING: Failed to check position for signal %d: %v", signalDB.ID, err)
						continue
					}

					// Если позиция не нулевая, восстанавливаем сигнал
					if posAmt != 0 {
						log.Printf("WebSocketManager: Position for %s is %.6f, reactivating auto-close signal", signalDB.Symbol, posAmt)
						signalDB.IsActive = true
						signalDB.LastQty = posAmt // Обновляем последний объем

						// Обновляем в БД
						if err := m.signalRepo.Update(context.Background(), signalDB); err != nil {
							log.Printf("WebSocketManager: ERROR: Failed to update signal %d: %v", signalDB.ID, err)
							continue
						}

						// Восстанавливаем watcher для этого сигнала
						go func() {
							watcher, err := m.createWatcherForUser(userID, signalDB.Symbol, signalDB.WatchMarket, true)
							if err != nil {
								log.Printf("WebSocketManager: ERROR: Failed to recreate watcher for signal %d: %v", signalDB.ID, err)
								return
							}

							// Создаем новый сигнал с актуальными данными
							restoredSignal := &Signal{
								ID:              signalDB.ID,
								UserID:          signalDB.UserID,
								Symbol:          signalDB.Symbol,
								TargetPrice:     signalDB.TargetPrice,
								MinQuantity:     signalDB.MinQuantity,
								TriggerOnCancel: signalDB.TriggerOnCancel,
								TriggerOnEat:    signalDB.TriggerOnEat,
								EatPercentage:   signalDB.EatPercentage,
								OriginalQty:     posAmt, // Используем текущий объем как начальный
								LastQty:         posAmt,
								AutoClose:       true,
								CloseMarket:     signalDB.CloseMarket,
								WatchMarket:     signalDB.WatchMarket,
								OriginalSide:    signalDB.OriginalSide,
								CreatedAt:       signalDB.CreatedAt,
							}

							watcher.AddSignal(restoredSignal)
							log.Printf("WebSocketManager: Successfully restored auto-close for signal %d", signalDB.ID)

							// Отправляем уведомление пользователю
							m.notifyUserAboutRestoredFeature(userID, signalDB.Symbol)
						}()
					} else {
						log.Printf("WebSocketManager: Position for %s is zero, not restoring signal %d", signalDB.Symbol, signalDB.ID)
					}
				}
			}
		}
	}
	log.Println("WebSocketManager: Auto-close restoration process completed")
}
func (m *WebSocketManager) checkPositionForSignal(signalDB *repository.SignalDB) (float64, error) {
	// Получаем API ключи пользователя
	okKeys, apiKey, secretKey := m.getKeysForUser(signalDB.UserID)
	if !okKeys {
		return 0, fmt.Errorf("no valid API keys for user %d", signalDB.UserID)
	}

	// Создаем временный клиент для проверки позиции
	client := futures.NewClient(apiKey, secretKey)
	resp, err := client.NewGetPositionRiskService().Symbol(signalDB.Symbol).Do(context.Background())
	if err != nil {
		return 0, err
	}

	if len(resp) == 0 {
		return 0, nil
	}

	positionAmt, err := strconv.ParseFloat(resp[0].PositionAmt, 64)
	if err != nil {
		return 0, err
	}

	return positionAmt, nil
}

func (m *WebSocketManager) notifyUserAboutRestoredFeature(userID int64, symbol string) {
	log.Printf("NOTIFICATION: User %d - Auto-close features restored for %s. Position monitoring is active again.", userID, symbol)
	// Здесь можно добавить реальную отправку уведомления (email, push и т.д.)
}
