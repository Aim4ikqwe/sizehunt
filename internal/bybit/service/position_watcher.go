package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/net/proxy"
)

// Position представляет позицию пользователя на Bybit
type Position struct {
	Symbol         string
	Side           string  // Buy, Sell
	Size           float64 // размер позиции
	AvgPrice       float64 // средняя цена входа
	UnrealisedPnl  float64 // нереализованный PnL
	PositionValue  float64 // стоимость позиции
	Leverage       string  // плечо
	Category       string  // linear, inverse
	PositionStatus string  // Normal, Liq, Adl
}

// PositionWatcher отслеживает позиции в реальном времени через WebSocket
type PositionWatcher struct {
	conn             *websocket.Conn
	mu               sync.RWMutex
	ctx              context.Context
	cancel           context.CancelFunc
	apiKey           string
	secretKey        string
	proxyAddr        string
	positions        map[string]*Position // symbol -> position
	isRunning        bool
	wsManager        *WebSocketManager
	userID           int64
	onPositionUpdate func(symbol string, pos *Position)
	httpClient       *BybitHTTPClient // HTTP клиент для загрузки начальных позиций
}

// NewPositionWatcher создает новый PositionWatcher
func NewPositionWatcher(ctx context.Context, apiKey, secretKey string) *PositionWatcher {
	watcherCtx, cancel := context.WithCancel(ctx)
	return &PositionWatcher{
		ctx:       watcherCtx,
		cancel:    cancel,
		apiKey:    apiKey,
		secretKey: secretKey,
		positions: make(map[string]*Position),
	}
}

// SetWebSocketManager устанавливает ссылку на WebSocketManager
func (pw *PositionWatcher) SetWebSocketManager(wsm *WebSocketManager) {
	pw.mu.Lock()
	defer pw.mu.Unlock()
	pw.wsManager = wsm
}

// SetUserID устанавливает ID пользователя
func (pw *PositionWatcher) SetUserID(userID int64) {
	pw.mu.Lock()
	defer pw.mu.Unlock()
	pw.userID = userID
}

// Start запускает PositionWatcher
func (pw *PositionWatcher) Start(proxyAddr string, httpClient *BybitHTTPClient) error {
	pw.mu.Lock()
	if pw.isRunning {
		pw.mu.Unlock()
		return nil
	}
	pw.proxyAddr = proxyAddr
	pw.httpClient = httpClient
	pw.mu.Unlock()

	if err := pw.connect(); err != nil {
		return err
	}

	go pw.readMessages()
	go pw.keepAlive()

	pw.mu.Lock()
	pw.isRunning = true
	pw.mu.Unlock()

	log.Printf("BybitPositionWatcher: Started for user %d", pw.userID)

	// Загружаем начальные позиции через REST API
	go pw.loadInitialPositions()

	return nil
}

// loadInitialPositions загружает начальные позиции через REST API
func (pw *PositionWatcher) loadInitialPositions() {
	pw.mu.RLock()
	client := pw.httpClient
	pw.mu.RUnlock()

	if client == nil {
		log.Printf("BybitPositionWatcher: No HTTP client, skipping initial position load")
		return
	}

	// Загружаем все linear позиции
	posResp, err := client.GetPositionRisk("", "linear")
	if err != nil {
		log.Printf("BybitPositionWatcher: Failed to load initial positions: %v", err)
		return
	}

	pw.mu.Lock()
	defer pw.mu.Unlock()

	for _, p := range posResp.Result.List {
		size, _ := strconv.ParseFloat(p.Size, 64)
		if size == 0 {
			continue // Пропускаем пустые позиции
		}

		avgPrice, _ := strconv.ParseFloat(p.AvgPrice, 64)
		uPnl, _ := strconv.ParseFloat(p.UnrealisedPnl, 64)

		pos := &Position{
			Symbol:         p.Symbol,
			Side:           p.Side,
			Size:           size,
			AvgPrice:       avgPrice,
			UnrealisedPnl:  uPnl,
			Category:       "linear",
			PositionStatus: p.PositionStatus,
		}

		pw.positions[p.Symbol] = pos
		log.Printf("BybitPositionWatcher: Loaded initial position for %s: size=%.6f, side=%s", p.Symbol, size, p.Side)
	}

	log.Printf("BybitPositionWatcher: Loaded %d initial positions", len(pw.positions))
}

// connect подключается к приватному WebSocket Bybit
func (pw *PositionWatcher) connect() error {
	wsURL := "wss://stream.bybit.com/v5/private"

	dialer := websocket.DefaultDialer

	// Настройка прокси если указан
	if pw.proxyAddr != "" {
		proxyURL := &url.URL{
			Scheme: "socks5",
			Host:   pw.proxyAddr,
		}
		proxyDialer, err := proxy.FromURL(proxyURL, proxy.Direct)
		if err != nil {
			log.Printf("BybitPositionWatcher: Failed to create proxy dialer: %v", err)
		} else {
			dialer = &websocket.Dialer{
				NetDial:          proxyDialer.Dial,
				HandshakeTimeout: 30 * time.Second,
			}
		}
	}

	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		return fmt.Errorf("failed to connect to Bybit private WebSocket: %v", err)
	}

	pw.mu.Lock()
	pw.conn = conn
	pw.mu.Unlock()

	// Аутентификация
	if err := pw.authenticate(); err != nil {
		conn.Close()
		return fmt.Errorf("authentication failed: %v", err)
	}

	// Подписка на позиции
	if err := pw.subscribeToPositions(); err != nil {
		conn.Close()
		return fmt.Errorf("subscription failed: %v", err)
	}

	log.Printf("BybitPositionWatcher: Connected and authenticated")
	return nil
}

// authenticate выполняет аутентификацию на WebSocket
func (pw *PositionWatcher) authenticate() error {
	expires := time.Now().UnixMilli() + 10000

	// Создаем подпись
	message := fmt.Sprintf("GET/realtime%d", expires)
	h := hmac.New(sha256.New, []byte(pw.secretKey))
	h.Write([]byte(message))
	signature := hex.EncodeToString(h.Sum(nil))

	authMsg := map[string]interface{}{
		"op":   "auth",
		"args": []interface{}{pw.apiKey, expires, signature},
	}

	data, err := json.Marshal(authMsg)
	if err != nil {
		return err
	}

	if err := pw.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return err
	}

	// Ждем ответ аутентификации
	_, msg, err := pw.conn.ReadMessage()
	if err != nil {
		return err
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(msg, &resp); err != nil {
		return err
	}

	if success, ok := resp["success"].(bool); !ok || !success {
		return fmt.Errorf("auth failed: %v", resp)
	}

	return nil
}

// subscribeToPositions подписывается на обновления позиций
func (pw *PositionWatcher) subscribeToPositions() error {
	subMsg := map[string]interface{}{
		"op":   "subscribe",
		"args": []string{"position"},
	}

	data, err := json.Marshal(subMsg)
	if err != nil {
		return err
	}

	return pw.conn.WriteMessage(websocket.TextMessage, data)
}

// readMessages читает и обрабатывает входящие сообщения
func (pw *PositionWatcher) readMessages() {
	defer func() {
		pw.mu.Lock()
		pw.isRunning = false
		pw.mu.Unlock()
		log.Printf("BybitPositionWatcher: readMessages loop ended")
	}()

	for {
		select {
		case <-pw.ctx.Done():
			return
		default:
			_, message, err := pw.conn.ReadMessage()
			if err != nil {
				log.Printf("BybitPositionWatcher: Read error: %v", err)
				return
			}

			pw.processMessage(message)
		}
	}
}

// BybitPositionData структура данных позиции от WebSocket
type BybitPositionData struct {
	Topic string `json:"topic"`
	Data  []struct {
		Symbol         string `json:"symbol"`
		Side           string `json:"side"`
		Size           string `json:"size"`
		AvgPrice       string `json:"avgPrice"`
		PositionValue  string `json:"positionValue"`
		UnrealisedPnl  string `json:"unrealisedPnl"`
		Leverage       string `json:"leverage"`
		PositionStatus string `json:"positionStatus"`
		Category       string `json:"category"`
	} `json:"data"`
}

// processMessage обрабатывает входящее сообщение
func (pw *PositionWatcher) processMessage(message []byte) {
	var posData BybitPositionData
	if err := json.Unmarshal(message, &posData); err != nil {
		return
	}

	if posData.Topic != "position" {
		return
	}

	pw.mu.Lock()
	defer pw.mu.Unlock()

	for _, p := range posData.Data {
		size, _ := strconv.ParseFloat(p.Size, 64)
		avgPrice, _ := strconv.ParseFloat(p.AvgPrice, 64)
		posValue, _ := strconv.ParseFloat(p.PositionValue, 64)
		uPnl, _ := strconv.ParseFloat(p.UnrealisedPnl, 64)

		pos := &Position{
			Symbol:         p.Symbol,
			Side:           p.Side,
			Size:           size,
			AvgPrice:       avgPrice,
			PositionValue:  posValue,
			UnrealisedPnl:  uPnl,
			Leverage:       p.Leverage,
			Category:       p.Category,
			PositionStatus: p.PositionStatus,
		}

		pw.positions[p.Symbol] = pos

		if pw.onPositionUpdate != nil {
			pw.onPositionUpdate(p.Symbol, pos)
		}

		log.Printf("BybitPositionWatcher: Position update for %s: size=%.6f, side=%s", p.Symbol, size, p.Side)
	}
}

// keepAlive отправляет ping сообщения для поддержания соединения
func (pw *PositionWatcher) keepAlive() {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-pw.ctx.Done():
			return
		case <-ticker.C:
			pw.mu.Lock()
			if pw.conn != nil {
				pingMsg := map[string]string{"op": "ping"}
				data, _ := json.Marshal(pingMsg)
				if err := pw.conn.WriteMessage(websocket.TextMessage, data); err != nil {
					log.Printf("BybitPositionWatcher: Failed to send ping: %v", err)
				}
			}
			pw.mu.Unlock()
		}
	}
}

// GetPosition возвращает позицию по символу
func (pw *PositionWatcher) GetPosition(symbol string) (*Position, bool) {
	pw.mu.RLock()
	defer pw.mu.RUnlock()
	pos, ok := pw.positions[symbol]
	return pos, ok
}

// IsRunning проверяет, запущен ли PositionWatcher
func (pw *PositionWatcher) IsRunning() bool {
	pw.mu.RLock()
	defer pw.mu.RUnlock()
	return pw.isRunning
}

// Stop останавливает PositionWatcher
func (pw *PositionWatcher) Stop() {
	pw.mu.Lock()
	defer pw.mu.Unlock()

	if pw.cancel != nil {
		pw.cancel()
	}

	if pw.conn != nil {
		pw.conn.Close()
		pw.conn = nil
	}

	pw.isRunning = false
	log.Printf("BybitPositionWatcher: Stopped")
}
