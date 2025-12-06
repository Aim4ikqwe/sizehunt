// internal/binance/service/user_data_stream.go
package service

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/adshao/go-binance/v2/futures"
)

type UserDataStream struct {
	futuresClient *futures.Client
	watcher       *PositionWatcher
	listenKey     string
	stopChan      chan struct{}
	doneChan      chan struct{}
	mu            sync.Mutex
	stopWsChan    chan struct{}
	lastAlive     time.Time
	proxyProvider ProxyProvider // Изменено с proxyService на интерфейс
	userID        int64
	isStopped     bool
}

func NewUserDataStream(client *futures.Client, watcher *PositionWatcher, proxyProvider ProxyProvider, userID int64) *UserDataStream {
	return &UserDataStream{
		futuresClient: client,
		watcher:       watcher,
		stopChan:      make(chan struct{}),
		doneChan:      make(chan struct{}),
		lastAlive:     time.Now(),
		proxyProvider: proxyProvider, // Используем интерфейс
		userID:        userID,
	}
}

func (u *UserDataStream) Start() error {
	// Проверяем, не остановлен ли поток
	u.mu.Lock()
	if u.isStopped {
		u.mu.Unlock()
		return fmt.Errorf("user data stream already stopped")
	}
	u.mu.Unlock()

	// Создаем новый поток
	keyStartTime := time.Now()
	key, err := u.futuresClient.NewStartUserStreamService().Do(context.Background())
	if err != nil {
		log.Printf("UserDataStream: ERROR: Failed to create listen key: %v", err)
		return fmt.Errorf("failed to create listen key: %w", err)
	}
	log.Printf("UserDataStream: Listen key created successfully: %s (took %v)", key, time.Since(keyStartTime))
	u.listenKey = key
	u.lastAlive = time.Now()

	// Запускаем keep-alive в отдельной горутине
	log.Println("UserDataStream: Starting keep-alive loop")
	go u.keepAliveLoop()

	// Запускаем WebSocket соединение
	log.Println("UserDataStream: Starting WebSocket connection")
	if err := u.runWs(); err != nil {
		log.Printf("UserDataStream: ERROR: Failed to start WebSocket: %v", err)
		return fmt.Errorf("failed to start WebSocket: %w", err)
	}

	log.Println("UserDataStream: Successfully started")
	return nil
}

func (u *UserDataStream) initializePositionsFromREST(symbol string) {
	if u.watcher == nil {
		log.Println("UserDataStream: WARNING: PositionWatcher is nil, skipping REST init")
		return
	}

	var resp []*futures.PositionRisk
	var err error

	// Если указан конкретный символ, запрашиваем только его позицию
	if symbol != "" {
		resp, err = u.futuresClient.NewGetPositionRiskService().Symbol(symbol).Do(context.Background())
		if err != nil {
			log.Printf("UserDataStream: ERROR: Failed to fetch position for %s from REST: %v", symbol, err)
			return
		}
		log.Printf("UserDataStream: Fetched position for single symbol %s from REST", symbol)
	} else {
		// Старый вариант - все позиции
		resp, err = u.futuresClient.NewGetPositionRiskService().Do(context.Background())
		if err != nil {
			log.Printf("UserDataStream: ERROR: Failed to fetch positions from REST: %v", err)
			return
		}
		log.Printf("UserDataStream: Found %d positions from REST", len(resp))
	}

	for i, pos := range resp {
		amt, err := strconv.ParseFloat(pos.PositionAmt, 64)
		if err != nil {
			log.Printf("UserDataStream: WARNING: Failed to parse position amount for %s (raw: %s): %v",
				pos.Symbol, pos.PositionAmt, err)
			continue
		}
		if amt != 0 {
			u.watcher.setPosition(pos.Symbol, amt)
			log.Printf("UserDataStream: [%d/%d] Initialized %s position = %.6f from REST",
				i+1, len(resp), pos.Symbol, amt)
		} else {
			log.Printf("UserDataStream: [%d/%d] Skipped zero position for %s", i+1, len(resp), pos.Symbol)
		}
	}
}

func (u *UserDataStream) runWs() error {
	handler := func(event *futures.WsUserDataEvent) {
		if u.watcher != nil {
			u.watcher.HandleWsEvent(event)
		}
	}
	errHandler := func(err error) {
		log.Printf("UserDataStream: WS ERROR HANDLER: %v", err)
	}

	// Проверяем, не остановлен ли поток
	u.mu.Lock()
	if u.isStopped {
		u.mu.Unlock()
		return fmt.Errorf("cannot start WS: stream already stopped")
	}
	u.mu.Unlock()

	// Получаем прокси адрес (если доступен)
	var proxyAddr string
	if u.proxyProvider != nil && u.userID != 0 {
		if addr, ok := u.proxyProvider.GetProxyAddressForUser(u.userID); ok {
			proxyAddr = addr
		}
	}

	// Если прокси указан, устанавливаем переменные окружения
	var originalHTTPProxy, originalHTTPSProxy, originalNoProxy string
	if proxyAddr != "" {
		log.Printf("UserDataStream: Setting up SOCKS5 proxy for user data stream: %s", proxyAddr)
		originalHTTPProxy = os.Getenv("http_proxy")
		originalHTTPSProxy = os.Getenv("https_proxy")
		originalNoProxy = os.Getenv("no_proxy")

		proxyURL := "socks5://" + proxyAddr
		os.Setenv("http_proxy", proxyURL)
		os.Setenv("https_proxy", proxyURL)
		os.Setenv("no_proxy", "")

		// Восстанавливаем исходные настройки после завершения функции
		defer func() {
			os.Setenv("http_proxy", originalHTTPProxy)
			os.Setenv("https_proxy", originalHTTPSProxy)
			os.Setenv("no_proxy", originalNoProxy)
			log.Printf("UserDataStream: Restored original proxy settings after WS connection")
		}()
	}

	// Сохраняем stopC канал для возможности остановки
	doneC, stopC, err := futures.WsUserDataServe(u.listenKey, handler, errHandler)
	if err != nil {
		// Дополнительное логирование при ошибке
		log.Printf("UserDataStream: ERROR: Failed to start WebSocket (proxy: %v): %v", proxyAddr != "", err)

		// Если с прокси не работает, пробуем без прокси для отладки
		if proxyAddr != "" {
			log.Printf("UserDataStream: Attempting to connect without proxy as fallback")
			os.Setenv("http_proxy", "")
			os.Setenv("https_proxy", "")
			os.Setenv("no_proxy", "")

			fallbackDoneC, fallbackStopC, fallbackErr := futures.WsUserDataServe(u.listenKey, handler, errHandler)
			if fallbackErr == nil {
				log.Printf("UserDataStream: Fallback connection succeeded! Proxy configuration issue detected.")
				doneC, stopC = fallbackDoneC, fallbackStopC
			} else {
				log.Printf("UserDataStream: Fallback connection also failed: %v", fallbackErr)
				return fmt.Errorf("failed to start WebSocket with or without proxy: %w", err)
			}
		} else {
			return fmt.Errorf("failed to start WebSocket: %w", err)
		}
	}

	u.mu.Lock()
	// Проверяем, не остановлен ли поток во время установки соединения
	if u.isStopped {
		u.mu.Unlock()
		close(stopC)
		return fmt.Errorf("stream stopped during connection setup")
	}
	u.stopWsChan = stopC
	u.mu.Unlock()

	go func() {
		select {
		case <-doneC:
			log.Println("UserDataStream: WebSocket connection closed normally")
		case <-u.stopChan:
			log.Println("UserDataStream: WebSocket connection stopped by request")
			// Закрываем канал остановки только если он еще не закрыт
			u.mu.Lock()
			if u.stopWsChan != nil {
				close(u.stopWsChan)
				u.stopWsChan = nil
			}
			u.mu.Unlock()
		}
	}()

	log.Printf("UserDataStream: WebSocket connection established successfully (proxy: %v)", proxyAddr != "")
	return nil
}
func (u *UserDataStream) keepAliveLoop() {
	log.Println("UserDataStream: Keep-alive loop started")
	defer log.Println("UserDataStream: Keep-alive loop stopped")

	ticker := time.NewTicker(50 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			u.mu.Lock()
			if u.isStopped {
				u.mu.Unlock()
				return
			}
			u.mu.Unlock()

			if u.listenKey == "" {
				log.Println("UserDataStream: Keep-alive skipped - no listen key")
				continue
			}

			keepAliveTime := time.Now()
			log.Printf("UserDataStream: Sending keep-alive for key %s (last alive: %v ago)",
				u.listenKey, time.Since(u.lastAlive))

			err := u.futuresClient.
				NewKeepaliveUserStreamService().
				ListenKey(u.listenKey).
				Do(context.Background())

			if err != nil {
				log.Printf("UserDataStream: ERROR: Keep-alive failed: %v", err)
				if recreateErr := u.recreateStream(); recreateErr != nil {
					log.Printf("UserDataStream: ERROR: Failed to recreate stream: %v", recreateErr)
				}
			} else {
				u.lastAlive = time.Now()
				log.Printf("UserDataStream: Keep-alive successful for key %s (took %v)",
					u.listenKey, time.Since(keepAliveTime))
			}

		case <-u.stopChan:
			log.Println("UserDataStream: Keep-alive loop stopping by request")
			return
		}
	}
}

func (u *UserDataStream) recreateStream() error {
	u.mu.Lock()
	defer u.mu.Unlock()

	if u.isStopped {
		return fmt.Errorf("cannot recreate stream: already stopped")
	}

	log.Println("UserDataStream: Recreating user data stream")

	// Останавливаем текущий поток
	if u.stopWsChan != nil {
		close(u.stopWsChan)
		u.stopWsChan = nil
	}

	// Создаем новый ключ
	newKey, err := u.futuresClient.NewStartUserStreamService().Do(context.Background())
	if err != nil {
		log.Printf("UserDataStream: ERROR: Failed to recreate listen key: %v", err)
		return err
	}

	log.Printf("UserDataStream: New listen key created: %s", newKey)
	u.listenKey = newKey
	u.lastAlive = time.Now()

	// Запускаем новый WebSocket
	if err := u.runWs(); err != nil {
		log.Printf("UserDataStream: ERROR: Failed to restart WebSocket: %v", err)
		return err
	}

	log.Println("UserDataStream: Stream successfully recreated")
	return nil
}

func (u *UserDataStream) StopWithContext(ctx context.Context) {
	log.Println("UserDataStream: StopWithContext called")

	// Проверяем, не остановлен ли уже поток
	u.mu.Lock()
	if u.isStopped {
		u.mu.Unlock()
		log.Println("UserDataStream: Already stopped, skipping")
		return
	}
	u.isStopped = true
	u.mu.Unlock()

	// Сначала удаляем listen key через работающий прокси
	u.mu.Lock()
	listenKey := u.listenKey
	u.listenKey = ""
	u.mu.Unlock()

	if listenKey != "" {
		log.Printf("UserDataStream: Deleting listen key: %s", listenKey)
		// Используем контекст с большим таймаутом для удаления ключа
		delCtx, delCancel := context.WithTimeout(ctx, 5*time.Second)
		defer delCancel()

		startDelTime := time.Now()
		err := u.futuresClient.NewCloseUserStreamService().ListenKey(listenKey).Do(delCtx)
		if err != nil {
			log.Printf("UserDataStream: WARNING: Failed to delete listen key: %v (attempted via %s proxy)",
				err, func() string {
					if u.proxyProvider != nil {
						if addr, ok := u.proxyProvider.GetProxyAddressForUser(u.userID); ok {
							return addr
						}
					}
					return "direct connection"
				}())
		} else {
			log.Printf("UserDataStream: Listen key %s deleted successfully (took %v)",
				listenKey, time.Since(startDelTime))
		}
	}

	// Останавливаем keep-alive loop
	u.mu.Lock()
	if u.stopChan != nil {
		close(u.stopChan)
		u.stopChan = nil
		log.Println("UserDataStream: Stop channel closed")
	}
	u.mu.Unlock()

	// Останавливаем WebSocket соединение
	u.mu.Lock()
	if u.stopWsChan != nil {
		close(u.stopWsChan)
		u.stopWsChan = nil
		log.Println("UserDataStream: WebSocket stop channel closed")
	}
	u.mu.Unlock()

	// Ждем завершения фоновых процессов с разумным таймаутом
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Ждем максимум 3 секунды для завершения всех операций
		select {
		case <-time.After(3 * time.Second):
			log.Println("UserDataStream: Background processes termination timeout reached")
		}
	}()

	select {
	case <-done:
	case <-ctx.Done():
	}

	log.Println("UserDataStream: All resources cleaned up after stop")
}
func (u *UserDataStream) initializePositionForSymbol(symbol string) error {
	if u.watcher == nil {
		log.Println("UserDataStream: WARNING: PositionWatcher is nil, skipping REST init")
		return fmt.Errorf("position watcher is nil")
	}

	log.Printf("UserDataStream: Initializing position for symbol %s from REST API", symbol)
	resp, err := u.futuresClient.NewGetPositionRiskService().Symbol(symbol).Do(context.Background())
	if err != nil {
		log.Printf("UserDataStream: ERROR: Failed to fetch position for %s from REST: %v", symbol, err)
		return err
	}

	if len(resp) == 0 {
		log.Printf("UserDataStream: No position found for symbol %s", symbol)
		u.watcher.setPosition(symbol, 0)
		return nil
	}

	// Обычно для одного символа будет только одна запись
	pos := resp[0]
	amt, err := strconv.ParseFloat(pos.PositionAmt, 64)
	if err != nil {
		log.Printf("UserDataStream: WARNING: Failed to parse position amount for %s (raw: %s): %v",
			pos.Symbol, pos.PositionAmt, err)
		return err
	}

	u.watcher.setPosition(pos.Symbol, amt)
	log.Printf("UserDataStream: Initialized %s position = %.6f from REST", symbol, amt)
	return nil
}
