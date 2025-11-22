// internal/binance/service/user_data_stream.go
package service

import (
	"context"
	"fmt"
	"log"
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
	isStopped     bool // Флаг для отслеживания состояния
}

func NewUserDataStream(client *futures.Client, watcher *PositionWatcher) *UserDataStream {
	return &UserDataStream{
		futuresClient: client,
		watcher:       watcher,
		stopChan:      make(chan struct{}),
		doneChan:      make(chan struct{}),
		lastAlive:     time.Now(),
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

	// Инициализация позиций через REST
	log.Println("UserDataStream: Initializing positions from REST API")
	u.initializePositionsFromREST()
	log.Println("UserDataStream: Positions initialization completed")

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

func (u *UserDataStream) initializePositionsFromREST() {
	if u.watcher == nil {
		log.Println("UserDataStream: WARNING: PositionWatcher is nil, skipping REST init")
		return
	}

	resp, err := u.futuresClient.NewGetPositionRiskService().Do(context.Background())
	if err != nil {
		log.Printf("UserDataStream: ERROR: Failed to fetch positions from REST: %v", err)
		return
	}

	log.Printf("UserDataStream: Found %d positions from REST", len(resp))
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

	// Сохраняем stopC канал для возможности остановки
	doneC, stopC, err := futures.WsUserDataServe(u.listenKey, handler, errHandler)
	if err != nil {
		log.Printf("UserDataStream: ERROR: Failed to start WebSocket: %v", err)
		return fmt.Errorf("failed to start WebSocket: %w", err)
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

	// Создаем контекст с отменой для внутренних операций
	stopCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	// Останавливаем keep-alive и другие горутины
	if u.stopChan != nil {
		close(u.stopChan)
		u.stopChan = nil
		log.Println("UserDataStream: Stop channel closed")
	}

	// Закрываем WebSocket соединение
	u.mu.Lock()
	if u.stopWsChan != nil {
		close(u.stopWsChan)
		u.stopWsChan = nil
		log.Println("UserDataStream: WebSocket stop channel closed")
	}
	u.mu.Unlock()

	// Удаляем listen key с таймаутом
	if u.listenKey != "" {
		log.Printf("UserDataStream: Deleting listen key: %s", u.listenKey)

		delCtx, delCancel := context.WithTimeout(stopCtx, 2*time.Second)
		defer delCancel()

		err := u.futuresClient.NewCloseUserStreamService().ListenKey(u.listenKey).Do(delCtx)
		if err != nil {
			log.Printf("UserDataStream: WARNING: Failed to delete listen key: %v", err)
		} else {
			log.Printf("UserDataStream: Listen key %s deleted successfully", u.listenKey)
		}

		u.listenKey = ""
	}

	// Ждем завершения в отдельной горутине с таймаутом
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-u.doneChan:
			log.Println("UserDataStream: All background processes stopped")
		case <-time.After(3 * time.Second):
			log.Println("UserDataStream: WARNING: Background processes may still be running after timeout")
		}
	}()

	select {
	case <-done:
		log.Println("UserDataStream: Stop completed successfully")
	case <-stopCtx.Done():
		log.Println("UserDataStream: Stop completed with context timeout")
	}
}
