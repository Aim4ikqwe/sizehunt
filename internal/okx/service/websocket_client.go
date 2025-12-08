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
	OKXPublicWSURL  = "wss://ws.okx.com:8443/ws/v5/public"
	OKXPrivateWSURL = "wss://ws.okx.com:8443/ws/v5/private"
)

// UnifiedDepthStreamData унифицированная структура для данных ордербука
type UnifiedDepthStreamData struct {
	Arg struct {
		Channel string `json:"channel"`
		InstID  string `json:"instId"`
	} `json:"arg"`
	Action string `json:"action"` // snapshot или update
	Data   []struct {
		Asks      [][]string `json:"asks"`
		Bids      [][]string `json:"bids"`
		Ts        string     `json:"ts"`
		Checksum  int64      `json:"checksum"`
		SeqID     int64      `json:"seqId"`
		PrevSeqID int64      `json:"prevSeqId"`
	} `json:"data"`
}

// OKXWSMessage структура для сообщений WebSocket OKX
type OKXWSMessage struct {
	Op   string        `json:"op,omitempty"`
	Args []interface{} `json:"args,omitempty"`
}

// OKXWSResponse структура для ответов WebSocket OKX
type OKXWSResponse struct {
	Event   string `json:"event,omitempty"`
	Code    string `json:"code,omitempty"`
	Msg     string `json:"msg,omitempty"`
	ConnID  string `json:"connId,omitempty"`
	Channel string `json:"channel,omitempty"`
}

// WebSocketClient клиент для WebSocket OKX
type WebSocketClient struct {
	conn      *websocket.Conn
	mu        sync.Mutex
	OnData    func(data *UnifiedDepthStreamData)
	ctx       context.Context
	cancel    context.CancelFunc
	proxyAddr string
	isRunning bool
	instIDs   []string
}

// NewWebSocketClient создает новый WebSocket клиент
func NewWebSocketClient() *WebSocketClient {
	return &WebSocketClient{}
}

// NewWebSocketClientWithProxy создает новый WebSocket клиент с прокси
func NewWebSocketClientWithProxy(proxyAddr string) *WebSocketClient {
	return &WebSocketClient{
		proxyAddr: proxyAddr,
	}
}

// ConnectPublic подключается к публичному WebSocket OKX
func (c *WebSocketClient) ConnectPublic(ctx context.Context, instIDs []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.ctx, c.cancel = context.WithCancel(ctx)
	c.instIDs = instIDs

	dialer := websocket.DefaultDialer

	// Настройка прокси если указан
	if c.proxyAddr != "" {
		proxyURL := &url.URL{
			Scheme: "socks5",
			Host:   c.proxyAddr,
		}
		proxyDialer, err := proxy.FromURL(proxyURL, proxy.Direct)
		if err != nil {
			log.Printf("OKXWebSocket: Failed to create proxy dialer: %v", err)
		} else {
			dialer = &websocket.Dialer{
				NetDial:          proxyDialer.Dial,
				HandshakeTimeout: 30 * time.Second,
			}
		}
	}

	conn, _, err := dialer.Dial(OKXPublicWSURL, nil)
	if err != nil {
		return fmt.Errorf("failed to connect to OKX WebSocket: %v", err)
	}
	c.conn = conn
	c.isRunning = true

	log.Printf("OKXWebSocket: Connected to public WebSocket for %d instruments", len(instIDs))

	// Подписываемся на каналы ордербуков
	if err := c.subscribeToBooks(instIDs); err != nil {
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
func (c *WebSocketClient) subscribeToBooks(instIDs []string) error {
	args := make([]interface{}, 0, len(instIDs))
	for _, instID := range instIDs {
		args = append(args, map[string]string{
			"channel": "books",
			"instId":  instID,
		})
	}

	msg := OKXWSMessage{
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

	log.Printf("OKXWebSocket: Subscribed to books for %v", instIDs)
	return nil
}

// readMessages читает и обрабатывает входящие сообщения
func (c *WebSocketClient) readMessages() {
	defer func() {
		c.mu.Lock()
		c.isRunning = false
		c.mu.Unlock()
		log.Printf("OKXWebSocket: readMessages loop ended")
	}()

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
			_, message, err := c.conn.ReadMessage()
			if err != nil {
				log.Printf("OKXWebSocket: Read error: %v", err)
				return
			}

			// Проверяем, является ли это ответом на subscribe или event
			var response OKXWSResponse
			if err := json.Unmarshal(message, &response); err == nil {
				if response.Event != "" {
					if response.Event == "error" {
						log.Printf("OKXWebSocket: Error from server: code=%s, msg=%s", response.Code, response.Msg)
					} else {
						log.Printf("OKXWebSocket: Event: %s, channel: %s", response.Event, response.Channel)
					}
					continue
				}
			}

			// Обрабатываем данные ордербука
			var depthData UnifiedDepthStreamData
			if err := json.Unmarshal(message, &depthData); err != nil {
				log.Printf("OKXWebSocket: Failed to parse depth data: %v", err)
				continue
			}

			if depthData.Arg.Channel == "books" && len(depthData.Data) > 0 {
				if c.OnData != nil {
					c.OnData(&depthData)
				}
			}
		}
	}
}

// keepAlive отправляет ping сообщения для поддержания соединения
func (c *WebSocketClient) keepAlive() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.mu.Lock()
			if c.conn != nil {
				if err := c.conn.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
					log.Printf("OKXWebSocket: Failed to send ping: %v", err)
				}
			}
			c.mu.Unlock()
		}
	}
}

// Close закрывает WebSocket соединение
func (c *WebSocketClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cancel != nil {
		c.cancel()
	}

	if c.conn != nil {
		// Отписываемся от каналов
		if len(c.instIDs) > 0 {
			args := make([]interface{}, 0, len(c.instIDs))
			for _, instID := range c.instIDs {
				args = append(args, map[string]string{
					"channel": "books",
					"instId":  instID,
				})
			}

			msg := OKXWSMessage{
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
	log.Printf("OKXWebSocket: Connection closed")
}

// IsRunning проверяет, запущено ли соединение
func (c *WebSocketClient) IsRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.isRunning
}
