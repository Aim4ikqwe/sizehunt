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
	return &WebSocketManager{
		userWatchers: make(map[int64]*UserWatcher),
		subService:   subService,
		keysRepo:     keysRepo,
		cfg:          cfg,
		ctx:          ctx,
	}
}

// GetOrCreateWatcherForUser возвращает watcher для конкретного пользователя и символа.
// Реализовано так, чтобы сетевые операции (Start userDataStream) выполнялись ВНЕ мьютекса.
func (m *WebSocketManager) GetOrCreateWatcherForUser(userID int64, symbol, market string, autoClose bool) (*MarketDepthWatcher, error) {
	// Флаги/объекты для старта вне мьютекса
	var toStartUserData bool
	var udStreamToStart *UserDataStream

	// 1) Быстрая фаза под мьютексом: получить или создать UserWatcher и вернуть существующий watcher если есть.
	m.mu.Lock()
	uw, ok := m.userWatchers[userID]
	if !ok {
		uw = &UserWatcher{
			spotWatchers:    make(map[string]*MarketDepthWatcher),
			futuresWatchers: make(map[string]*MarketDepthWatcher),
		}
		m.userWatchers[userID] = uw
	}

	switch market {
	case "spot":
		// Если уже есть watcher для символа — вернуть
		if w, exists := uw.spotWatchers[symbol]; exists && w != nil {
			m.mu.Unlock()
			return w, nil
		}
		// Создаём новый MarketDepthWatcher (он ещё не подключится до AddSignal)
		w := NewMarketDepthWatcher(
			m.ctx, "spot", m.subService, m.keysRepo, m.cfg, nil, nil, nil,
		)
		uw.spotWatchers[symbol] = w
		m.mu.Unlock()
		return w, nil

	case "futures":
		// Если уже есть watcher для символа — вернуть
		if w, exists := uw.futuresWatchers[symbol]; exists && w != nil {
			m.mu.Unlock()
			return w, nil
		}

		// Если требуется autoClose — убедимся, что у пользователя есть userDataStream и futuresClient.
		if autoClose {
			// Если у нас ещё нет созданных общих объектов для этого пользователя — создаём их (но не стартуем stream)
			if uw.userDataStream == nil {
				// получаем ключи
				okKeys, apiKey, secretKey := m.getKeysForUser(userID)
				if !okKeys {
					m.mu.Unlock()
					return nil, fmt.Errorf("futures auto-close requires valid API keys")
				}
				uw.futuresClient = futures.NewClient(apiKey, secretKey)
				uw.positionWatcher = NewPositionWatcher()
				uw.userDataStream = NewUserDataStream(uw.futuresClient, uw.positionWatcher)
				uw.userDataStreamStarted = false
				// пометим, что нужно стартовать UserDataStream после выхода из мьютекса
				toStartUserData = true
				udStreamToStart = uw.userDataStream
			}
			// если userDataStream уже есть, но не стартован — пометим для старта
			if uw.userDataStream != nil && !uw.userDataStreamStarted {
				toStartUserData = true
				udStreamToStart = uw.userDataStream
			}
		} else {
			// Если autoClose == false — можем оставить futuresClient nil (MarketDepthWatcher может работать без него)
		}

		// Создаём futures watcher для данного символа, передаём общие структуры (возможно nil)
		w := NewMarketDepthWatcher(
			m.ctx,
			"futures",
			m.subService,
			m.keysRepo,
			m.cfg,
			uw.futuresClient,
			uw.positionWatcher,
			uw.userDataStream,
		)
		uw.futuresWatchers[symbol] = w

		// Сохраняем локальную ссылку и выходим из мьютекса
		m.mu.Unlock()

		// 2) Стартап UserDataStream (если требуется) — ОБЯЗАТЕЛЬНО ВНЕ мьютекса
		if toStartUserData && udStreamToStart != nil {
			// даём разумный таймаут старта, чтобы не повесить обработчик
			startCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			// note: текущая сигнатура Start() не принимает ctx, но мы оставляем шаблон/таймаут на случай расширения
			_ = startCtx
			defer cancel()

			if err := udStreamToStart.Start(); err != nil {
				// если старт не удался — логируем и корректно откорректируем состояние под мьютексом
				log.Printf("WebSocketManager: Failed to start UserDataStream for user %d: %v", userID, err)
				// откатим создание watcher'а — безопасно под мьютексом
				m.mu.Lock()
				// удаляем только этот watcher; остальные могут остаться
				if uw2, ok2 := m.userWatchers[userID]; ok2 {
					delete(uw2.futuresWatchers, symbol)
				}
				// если userDataStream был создан нами и он не стартовал — сбросим его, чтобы попытать снова позже
				uw.futuresClient = nil
				uw.positionWatcher = nil
				uw.userDataStream = nil
				uw.userDataStreamStarted = false
				m.mu.Unlock()

				return nil, fmt.Errorf("failed to start user data stream: %w", err)
			}

			// пометить, что stream запущен
			m.mu.Lock()
			if uw3, ok3 := m.userWatchers[userID]; ok3 {
				uw3.userDataStreamStarted = true
			}
			m.mu.Unlock()
		}

		// возвращаем созданный watcher
		return w, nil

	default:
		m.mu.Unlock()
		return nil, fmt.Errorf("unsupported market: %s", market)
	}
}

// getKeysForUser — оставил как в оригинале
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
