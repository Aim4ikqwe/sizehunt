package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Position представляет позицию на OKX
type Position struct {
	InstID      string
	InstType    string
	Pos         float64 // Размер позиции (+ long, - short)
	PosSide     string  // long, short, net
	AvgPx       float64 // Средняя цена входа
	Upl         float64 // Нереализованный PnL
	Lever       string  // Плечо
	MgnMode     string  // cross, isolated
	NotionalUsd string  // Размер в USD
	LastUpdate  time.Time
}

// OKXPositionData структура данных позиции из WebSocket
type OKXPositionData struct {
	Arg struct {
		Channel  string `json:"channel"`
		InstType string `json:"instType"`
	} `json:"arg"`
	Data []struct {
		InstID      string `json:"instId"`
		InstType    string `json:"instType"`
		Pos         string `json:"pos"`
		PosSide     string `json:"posSide"`
		AvgPx       string `json:"avgPx"`
		Upl         string `json:"upl"`
		Lever       string `json:"lever"`
		MgnMode     string `json:"mgnMode"`
		NotionalUsd string `json:"notionalUsd"`
		UTime       string `json:"uTime"`
	} `json:"data"`
}

// PositionWatcher отслеживает позиции пользователя через WebSocket
type PositionWatcher struct {
	positions  map[string]*Position // instId -> Position
	mu         sync.RWMutex
	wsConn     *websocket.Conn
	ctx        context.Context
	cancel     context.CancelFunc
	apiKey     string
	secretKey  string
	passphrase string
	isRunning  bool
}

// NewPositionWatcher создает новый PositionWatcher
func NewPositionWatcher(ctx context.Context, apiKey, secretKey, passphrase string) *PositionWatcher {
	childCtx, cancel := context.WithCancel(ctx)
	return &PositionWatcher{
		positions:  make(map[string]*Position),
		ctx:        childCtx,
		cancel:     cancel,
		apiKey:     apiKey,
		secretKey:  secretKey,
		passphrase: passphrase,
		isRunning:  false,
	}
}

// Start запускает PositionWatcher с подключением к приватному WebSocket
func (pw *PositionWatcher) Start(proxyAddr string) error {
	pw.mu.Lock()
	if pw.isRunning {
		pw.mu.Unlock()
		return nil
	}
	pw.isRunning = true
	pw.mu.Unlock()

	// Подключаемся к приватному WebSocket
	dialer := websocket.DefaultDialer

	// TODO: Добавить поддержку прокси если нужно
	conn, _, err := dialer.Dial(OKXPrivateWSURL, nil)
	if err != nil {
		pw.mu.Lock()
		pw.isRunning = false
		pw.mu.Unlock()
		return fmt.Errorf("failed to connect to OKX private WebSocket: %v", err)
	}

	pw.mu.Lock()
	pw.wsConn = conn
	pw.mu.Unlock()

	log.Printf("OKXPositionWatcher: Connected to private WebSocket")

	// Аутентификация
	if err := pw.authenticate(); err != nil {
		conn.Close()
		pw.mu.Lock()
		pw.isRunning = false
		pw.mu.Unlock()
		return fmt.Errorf("failed to authenticate: %v", err)
	}

	// Подписываемся на позиции
	if err := pw.subscribeToPositions(); err != nil {
		conn.Close()
		pw.mu.Lock()
		pw.isRunning = false
		pw.mu.Unlock()
		return fmt.Errorf("failed to subscribe to positions: %v", err)
	}

	// Запускаем обработку сообщений
	go pw.readMessages()
	go pw.keepAlive()

	log.Printf("OKXPositionWatcher: Started successfully")
	return nil
}

// authenticate выполняет аутентификацию на приватном WebSocket
func (pw *PositionWatcher) authenticate() error {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	message := timestamp + "GET" + "/users/self/verify"

	h := hmac.New(sha256.New, []byte(pw.secretKey))
	h.Write([]byte(message))
	signature := base64.StdEncoding.EncodeToString(h.Sum(nil))

	loginMsg := map[string]interface{}{
		"op": "login",
		"args": []map[string]string{{
			"apiKey":     pw.apiKey,
			"passphrase": pw.passphrase,
			"timestamp":  timestamp,
			"sign":       signature,
		}},
	}

	data, err := json.Marshal(loginMsg)
	if err != nil {
		return fmt.Errorf("failed to marshal login message: %v", err)
	}

	pw.mu.RLock()
	conn := pw.wsConn
	pw.mu.RUnlock()

	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return fmt.Errorf("failed to send login message: %v", err)
	}

	log.Printf("OKXPositionWatcher: Authentication message sent")

	// Ждем ответа на аутентификацию
	time.Sleep(500 * time.Millisecond)

	return nil
}

// subscribeToPositions подписывается на обновления позиций
func (pw *PositionWatcher) subscribeToPositions() error {
	// Подписываемся на все типы инструментов
	args := []interface{}{
		map[string]string{
			"channel":  "positions",
			"instType": "SWAP",
		},
		map[string]string{
			"channel":  "positions",
			"instType": "FUTURES",
		},
	}

	msg := OKXWSMessage{
		Op:   "subscribe",
		Args: args,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal subscribe message: %v", err)
	}

	pw.mu.RLock()
	conn := pw.wsConn
	pw.mu.RUnlock()

	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return fmt.Errorf("failed to send subscribe message: %v", err)
	}

	log.Printf("OKXPositionWatcher: Subscribed to positions channel")
	return nil
}

// readMessages читает и обрабатывает входящие сообщения
func (pw *PositionWatcher) readMessages() {
	defer func() {
		pw.mu.Lock()
		pw.isRunning = false
		pw.mu.Unlock()
		log.Printf("OKXPositionWatcher: readMessages loop ended")
	}()

	for {
		select {
		case <-pw.ctx.Done():
			return
		default:
			pw.mu.RLock()
			conn := pw.wsConn
			pw.mu.RUnlock()

			if conn == nil {
				return
			}

			_, message, err := conn.ReadMessage()
			if err != nil {
				log.Printf("OKXPositionWatcher: Read error: %v", err)
				return
			}

			// Проверяем тип сообщения
			var response OKXWSResponse
			if err := json.Unmarshal(message, &response); err == nil {
				if response.Event != "" {
					if response.Event == "error" {
						log.Printf("OKXPositionWatcher: Error from server: code=%s, msg=%s", response.Code, response.Msg)
					} else if response.Event == "login" {
						if response.Code == "0" {
							log.Printf("OKXPositionWatcher: Login successful")
						} else {
							log.Printf("OKXPositionWatcher: Login failed: %s", response.Msg)
						}
					} else {
						log.Printf("OKXPositionWatcher: Event: %s", response.Event)
					}
					continue
				}
			}

			// Обрабатываем данные позиций
			var posData OKXPositionData
			if err := json.Unmarshal(message, &posData); err != nil {
				continue
			}

			if posData.Arg.Channel == "positions" && len(posData.Data) > 0 {
				pw.updatePositions(&posData)
			}
		}
	}
}

// updatePositions обновляет позиции из WebSocket данных
func (pw *PositionWatcher) updatePositions(data *OKXPositionData) {
	pw.mu.Lock()
	defer pw.mu.Unlock()

	for _, posData := range data.Data {
		pos, _ := strconv.ParseFloat(posData.Pos, 64)
		avgPx, _ := strconv.ParseFloat(posData.AvgPx, 64)
		upl, _ := strconv.ParseFloat(posData.Upl, 64)

		position := &Position{
			InstID:      posData.InstID,
			InstType:    posData.InstType,
			Pos:         pos,
			PosSide:     posData.PosSide,
			AvgPx:       avgPx,
			Upl:         upl,
			Lever:       posData.Lever,
			MgnMode:     posData.MgnMode,
			NotionalUsd: posData.NotionalUsd,
			LastUpdate:  time.Now(),
		}

		pw.positions[posData.InstID] = position

		log.Printf("OKXPositionWatcher: Position updated for %s: pos=%.6f, side=%s, pnl=%.2f",
			posData.InstID, pos, posData.PosSide, upl)
	}
}

// keepAlive отправляет ping сообщения
func (pw *PositionWatcher) keepAlive() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-pw.ctx.Done():
			return
		case <-ticker.C:
			pw.mu.RLock()
			conn := pw.wsConn
			pw.mu.RUnlock()

			if conn != nil {
				if err := conn.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
					log.Printf("OKXPositionWatcher: Failed to send ping: %v", err)
				}
			}
		}
	}
}

// GetPosition возвращает позицию по instID
func (pw *PositionWatcher) GetPosition(instID string) (*Position, bool) {
	pw.mu.RLock()
	defer pw.mu.RUnlock()

	pos, exists := pw.positions[instID]
	return pos, exists
}

// GetAllPositions возвращает все позиции
func (pw *PositionWatcher) GetAllPositions() map[string]*Position {
	pw.mu.RLock()
	defer pw.mu.RUnlock()

	// Создаем копию
	positions := make(map[string]*Position, len(pw.positions))
	for k, v := range pw.positions {
		posCopy := *v
		positions[k] = &posCopy
	}
	return positions
}

// HasOpenPosition проверяет наличие открытой позиции
func (pw *PositionWatcher) HasOpenPosition(instID string) bool {
	pw.mu.RLock()
	defer pw.mu.RUnlock()

	pos, exists := pw.positions[instID]
	if !exists {
		return false
	}
	return pos.Pos != 0
}

// Stop останавливает PositionWatcher
func (pw *PositionWatcher) Stop() {
	pw.mu.Lock()
	defer pw.mu.Unlock()

	if pw.cancel != nil {
		pw.cancel()
	}

	if pw.wsConn != nil {
		// Отписываемся от каналов
		unsubMsg := map[string]interface{}{
			"op": "unsubscribe",
			"args": []map[string]string{{
				"channel":  "positions",
				"instType": "SWAP",
			}},
		}
		data, _ := json.Marshal(unsubMsg)
		pw.wsConn.WriteMessage(websocket.TextMessage, data)

		pw.wsConn.Close()
		pw.wsConn = nil
	}

	pw.isRunning = false
	log.Printf("OKXPositionWatcher: Stopped")
}

// IsRunning проверяет, запущен ли watcher
func (pw *PositionWatcher) IsRunning() bool {
	pw.mu.RLock()
	defer pw.mu.RUnlock()
	return pw.isRunning
}
