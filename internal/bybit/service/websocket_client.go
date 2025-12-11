package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/net/proxy"
)

const (
	BybitPublicWSURL = "wss://stream.bybit.com/v5/public"
)

// BybitDepthStreamData структура для данных ордербука Bybit
type BybitDepthStreamData struct {
	Topic string `json:"topic"` // orderbook.1.BTCUSDT
	Type  string `json:"type"`  // snapshot или delta
	Ts    int64  `json:"ts"`    // timestamp
	Data  struct {
		S   string     `json:"s"`   // symbol
		B   [][]string `json:"b"`   // bids [price, size]
		A   [][]string `json:"a"`   // asks [price, size]
		U   int64      `json:"u"`   // update ID
		Seq int64      `json:"seq"` // sequence number
	} `json:"data"`
}

// BybitWSMessage структура для сообщений WebSocket Bybit
type BybitWSMessage struct {
	Op   string        `json:"op,omitempty"`
	Args []interface{} `json:"args,omitempty"`
}

// BybitWSResponse структура для ответов WebSocket Bybit
type BybitWSResponse struct {
	Success bool   `json:"success,omitempty"`
	RetMsg  string `json:"ret_msg,omitempty"`
	ConnID  string `json:"conn_id,omitempty"`
	ReqID   string `json:"req_id,omitempty"`
	Op      string `json:"op,omitempty"`
}

// BybitWebSocketClient клиент для WebSocket Bybit
type BybitWebSocketClient struct {
	conn      *websocket.Conn
	mu        sync.Mutex
	OnData    func(data *BybitDepthStreamData)
	ctx       context.Context
	cancel    context.CancelFunc
	proxyAddr string
	isRunning bool
	symbols   []string
}

// NewBybitWebSocketClient создает новый WebSocket клиент
func NewBybitWebSocketClient() *BybitWebSocketClient {
	return &BybitWebSocketClient{}
}

// NewBybitWebSocketClientWithProxy создает новый WebSocket клиент с прокси
func NewBybitWebSocketClientWithProxy(proxyAddr string) *BybitWebSocketClient {
	return &BybitWebSocketClient{
		proxyAddr: proxyAddr,
	}
}

// ConnectPublic подключается к публичному WebSocket Bybit
// category может быть "spot", "linear" или "inverse"
func (c *BybitWebSocketClient) ConnectPublic(ctx context.Context, symbols []string, category string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.ctx, c.cancel = context.WithCancel(ctx)
	c.symbols = symbols

	dialer := websocket.DefaultDialer

	// Настройка прокси если указан
	if c.proxyAddr != "" {
		proxyURL := &url.URL{
			Scheme: "socks5",
			Host:   c.proxyAddr,
		}
		proxyDialer, err := proxy.FromURL(proxyURL, proxy.Direct)
		if err != nil {
			log.Printf("BybitWebSocket: Failed to create proxy dialer: %v", err)
		} else {
			dialer = &websocket.Dialer{
				NetDial:          proxyDialer.Dial,
				HandshakeTimeout: 30 * time.Second,
			}
		}
	}

	// Формируем URL в зависимости от категории
	wsURL := BybitPublicWSURL
	if category != "" {
		wsURL = fmt.Sprintf("%s/%s", BybitPublicWSURL, category)
	} else {
		wsURL = fmt.Sprintf("%s/linear", BybitPublicWSURL) // по умолчанию linear
	}

	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		return fmt.Errorf("failed to connect to Bybit WebSocket: %v", err)
	}
	c.conn = conn
	c.isRunning = true

	log.Printf("BybitWebSocket: Connected to public WebSocket for %d symbols", len(symbols))

	// Подписываемся на каналы ордербуков
	if err := c.subscribeToBooks(symbols); err != nil {
		c.conn.Close()
		return fmt.Errorf("failed to subscribe to books: %v", err)
	}

	// Запускаем обработку сообщений
	go c.readMessages()

	// Запускаем ping/pong
	go c.keepAlive()

	return nil
}

// subscribeToBooks подписывается на каналы ордербуков
func (c *BybitWebSocketClient) subscribeToBooks(symbols []string) error {
	args := make([]interface{}, 0, len(symbols))
	for _, symbol := range symbols {
		// Bybit использует формат: orderbook.{depth}.{symbol}
		// depth 1 = топ 1 уровень, depth 200 = топ 200 уровней
		args = append(args, fmt.Sprintf("orderbook.1000.%s", symbol))
	}

	msg := BybitWSMessage{
		Op:   "subscribe",
		Args: args,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal subscribe message: %v", err)
	}

	if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return fmt.Errorf("failed to send subscribe message: %v", err)
	}

	log.Printf("BybitWebSocket: Subscribed to orderbook for %v", symbols)
	return nil
}

// readMessages читает и обрабатывает входящие сообщения
func (c *BybitWebSocketClient) readMessages() {
	defer func() {
		c.mu.Lock()
		c.isRunning = false
		c.mu.Unlock()
		log.Printf("BybitWebSocket: readMessages loop ended")
	}()

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
			_, message, err := c.conn.ReadMessage()
			if err != nil {
				log.Printf("BybitWebSocket: Read error: %v", err)
				return
			}

			// Проверяем, является ли это ответом на subscribe
			var response BybitWSResponse
			if err := json.Unmarshal(message, &response); err == nil {
				if response.Op == "subscribe" || response.Success {
					log.Printf("BybitWebSocket: Subscribe response: success=%v, msg=%s", response.Success, response.RetMsg)
					continue
				}
			}

			// Обрабатываем данные ордербука
			var depthData BybitDepthStreamData
			if err := json.Unmarshal(message, &depthData); err != nil {
				// Игнорируем ошибки парсинга для других типов сообщений
				continue
			}

			// Проверяем, что это обновление ордербука
			if depthData.Topic != "" && (depthData.Type == "snapshot" || depthData.Type == "delta") {
				if c.OnData != nil {
					c.OnData(&depthData)
				}
			}
		}
	}
}

// keepAlive отправляет ping сообщения для поддержания соединения
func (c *BybitWebSocketClient) keepAlive() {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.mu.Lock()
			if c.conn != nil {
				// Bybit использует ping в формате JSON
				pingMsg := map[string]string{"op": "ping"}
				data, _ := json.Marshal(pingMsg)
				if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
					log.Printf("BybitWebSocket: Failed to send ping: %v", err)
				}
			}
			c.mu.Unlock()
		}
	}
}

// Close закрывает WebSocket соединение
func (c *BybitWebSocketClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cancel != nil {
		c.cancel()
	}

	if c.conn != nil {
		// Отписываемся от каналов
		if len(c.symbols) > 0 {
			args := make([]interface{}, 0, len(c.symbols))
			for _, symbol := range c.symbols {
				args = append(args, fmt.Sprintf("orderbook.1000.%s", symbol))
			}

			msg := BybitWSMessage{
				Op:   "unsubscribe",
				Args: args,
			}

			data, _ := json.Marshal(msg)
			c.conn.WriteMessage(websocket.TextMessage, data)
		}

		c.conn.Close()
		c.conn = nil
	}

	c.isRunning = false
	log.Printf("BybitWebSocket: Connection closed")
}

// IsRunning проверяет, запущено ли соединение
func (c *BybitWebSocketClient) IsRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.isRunning
}
