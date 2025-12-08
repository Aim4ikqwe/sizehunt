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

const (
	OKXPrivateTradingWSURL = "wss://ws.okx.com:8443/ws/v5/private"
)

// OrderRequest структура запроса для размещения ордера через WebSocket
type WSOrderRequest struct {
	InstID     string `json:"instId"`
	TdMode     string `json:"tdMode"`
	Side       string `json:"side"`
	OrdType    string `json:"ordType"`
	Sz         string `json:"sz"`
	PosSide    string `json:"posSide,omitempty"`
	ReduceOnly string `json:"reduceOnly,omitempty"`
}

// WSOrderResponse ответ на размещение ордера
type WSOrderResponse struct {
	ID   string `json:"id"`
	Op   string `json:"op"`
	Code string `json:"code"`
	Msg  string `json:"msg"`
	Data []struct {
		OrdID   string `json:"ordId"`
		ClOrdID string `json:"clOrdId"`
		SCode   string `json:"sCode"`
		SMsg    string `json:"sMsg"`
	} `json:"data"`
}

// TradingWebSocket клиент для размещения ордеров через WebSocket
type TradingWebSocket struct {
	conn        *websocket.Conn
	mu          sync.RWMutex
	responses   map[string]chan *WSOrderResponse
	apiKey      string
	secretKey   string
	passphrase  string
	isConnected bool
	isAuthd     bool
	ctx         context.Context
	cancel      context.CancelFunc
	requestID   int64
	proxyAddr   string
}

// NewTradingWebSocket создает новый TradingWebSocket
func NewTradingWebSocket(ctx context.Context, apiKey, secretKey, passphrase, proxyAddr string) *TradingWebSocket {
	childCtx, cancel := context.WithCancel(ctx)
	return &TradingWebSocket{
		responses:  make(map[string]chan *WSOrderResponse),
		apiKey:     apiKey,
		secretKey:  secretKey,
		passphrase: passphrase,
		ctx:        childCtx,
		cancel:     cancel,
		proxyAddr:  proxyAddr,
	}
}

// Connect подключается к приватному WebSocket и аутентифицируется
func (tw *TradingWebSocket) Connect() error {
	tw.mu.Lock()
	if tw.isConnected {
		tw.mu.Unlock()
		return nil
	}
	tw.mu.Unlock()

	// TODO: Добавить поддержку прокси если нужно
	dialer := websocket.DefaultDialer
	conn, _, err := dialer.Dial(OKXPrivateTradingWSURL, nil)
	if err != nil {
		return fmt.Errorf("failed to connect to OKX trading WebSocket: %v", err)
	}

	tw.mu.Lock()
	tw.conn = conn
	tw.isConnected = true
	tw.mu.Unlock()

	log.Printf("OKXTradingWS: Connected to trading WebSocket")

	// Запускаем обработку сообщений
	go tw.readMessages()
	go tw.keepAlive()

	// Аутентификация
	if err := tw.authenticate(); err != nil {
		tw.Close()
		return fmt.Errorf("authentication failed: %v", err)
	}

	// Ждем подтверждения аутентификации
	time.Sleep(500 * time.Millisecond)

	log.Printf("OKXTradingWS: Authenticated successfully")
	return nil
}

// authenticate выполняет аутентификацию
func (tw *TradingWebSocket) authenticate() error {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	message := timestamp + "GET" + "/users/self/verify"

	h := hmac.New(sha256.New, []byte(tw.secretKey))
	h.Write([]byte(message))
	signature := base64.StdEncoding.EncodeToString(h.Sum(nil))

	loginMsg := map[string]interface{}{
		"op": "login",
		"args": []map[string]string{{
			"apiKey":     tw.apiKey,
			"passphrase": tw.passphrase,
			"timestamp":  timestamp,
			"sign":       signature,
		}},
	}

	tw.mu.RLock()
	conn := tw.conn
	tw.mu.RUnlock()

	if err := conn.WriteJSON(loginMsg); err != nil {
		return fmt.Errorf("failed to send login message: %v", err)
	}

	log.Printf("OKXTradingWS: Authentication message sent")
	return nil
}

// PlaceMarketOrder размещает рыночный ордер для закрытия позиции
func (tw *TradingWebSocket) PlaceMarketOrder(instID string, positionSize float64, posSide, mgnMode string) error {
	startTime := time.Now()
	defer func() {
		log.Printf("OKXTradingWS: PlaceMarketOrder completed in %v", time.Since(startTime))
	}()

	tw.mu.RLock()
	if !tw.isConnected {
		tw.mu.RUnlock()
		return fmt.Errorf("not connected to trading WebSocket")
	}
	if !tw.isAuthd {
		tw.mu.RUnlock()
		return fmt.Errorf("trading WebSocket not authenticated")
	}
	tw.mu.RUnlock()

	// Генерируем уникальный ID запроса
	requestID := tw.generateRequestID()

	// Определяем сторону для закрытия
	var side string
	var sz string
	if positionSize > 0 {
		side = "sell"
		sz = fmt.Sprintf("%.8f", positionSize)
	} else {
		side = "buy"
		sz = fmt.Sprintf("%.8f", -positionSize)
	}

	// Создаем запрос
	orderReq := WSOrderRequest{
		InstID:     instID,
		TdMode:     mgnMode,
		Side:       side,
		OrdType:    "market",
		Sz:         sz,
		ReduceOnly: "true",
	}

	if posSide != "net" {
		orderReq.PosSide = posSide
	}

	msg := map[string]interface{}{
		"id":   requestID,
		"op":   "order",
		"args": []interface{}{orderReq},
	}

	// Создаем канал для ответа
	responseChan := make(chan *WSOrderResponse, 1)
	tw.mu.Lock()
	tw.responses[requestID] = responseChan
	tw.mu.Unlock()

	// Отправляем ордер
	log.Printf("OKXTradingWS: Placing market %s order for %s (size: %s) via WebSocket", side, instID, sz)
	orderSendStart := time.Now()

	tw.mu.RLock()
	conn := tw.conn
	tw.mu.RUnlock()

	if err := conn.WriteJSON(msg); err != nil {
		tw.mu.Lock()
		delete(tw.responses, requestID)
		tw.mu.Unlock()
		return fmt.Errorf("failed to send order: %v", err)
	}

	// Ждем ответа с таймаутом
	select {
	case resp := <-responseChan:
		executionTime := time.Since(orderSendStart)

		if resp.Code != "0" {
			log.Printf("OKXTradingWS: Market order REJECTED: %s - %s (execution time: %v)",
				resp.Code, resp.Msg, executionTime)
			return fmt.Errorf("OKX order error: %s - %s", resp.Code, resp.Msg)
		}

		if len(resp.Data) > 0 {
			data := resp.Data[0]
			if data.SCode != "0" {
				log.Printf("OKXTradingWS: Market order REJECTED: %s - %s (execution time: %v)",
					data.SCode, data.SMsg, executionTime)
				return fmt.Errorf("OKX order error: %s - %s", data.SCode, data.SMsg)
			}
			log.Printf("OKXTradingWS: ✅ Market order EXECUTED in %v, orderId: %s (via WebSocket)",
				executionTime, data.OrdID)
		}

		return nil

	case <-time.After(10 * time.Second):
		tw.mu.Lock()
		delete(tw.responses, requestID)
		tw.mu.Unlock()
		return fmt.Errorf("timeout waiting for order response")
	}
}

// readMessages обрабатывает входящие сообщения
func (tw *TradingWebSocket) readMessages() {
	defer func() {
		tw.mu.Lock()
		tw.isConnected = false
		tw.mu.Unlock()
		log.Printf("OKXTradingWS: readMessages loop ended")
	}()

	for {
		select {
		case <-tw.ctx.Done():
			return
		default:
			tw.mu.RLock()
			conn := tw.conn
			tw.mu.RUnlock()

			if conn == nil {
				return
			}

			_, message, err := conn.ReadMessage()
			if err != nil {
				log.Printf("OKXTradingWS: Read error: %v", err)
				return
			}

			// Игнорируем pong сообщения
			if string(message) == "pong" {
				continue
			}

			// Парсим сообщение
			var respMap map[string]interface{}
			if err := json.Unmarshal(message, &respMap); err != nil {
				continue
			}

			// Обрабатываем события
			if event, ok := respMap["event"].(string); ok {
				if event == "login" {
					if code, ok := respMap["code"].(string); ok && code == "0" {
						tw.mu.Lock()
						tw.isAuthd = true
						tw.mu.Unlock()
						log.Printf("OKXTradingWS: Login successful")
					} else {
						log.Printf("OKXTradingWS: Login failed: %v", respMap["msg"])
					}
				}
				continue
			}

			// Обрабатываем ответы на ордера
			if id, ok := respMap["id"].(string); ok {
				log.Printf("OKXTradingWS: Got response for order ID: %s", id)
				var orderResp WSOrderResponse
				if err := json.Unmarshal(message, &orderResp); err == nil {
					tw.mu.Lock()
					if ch, exists := tw.responses[id]; exists {
						log.Printf("OKXTradingWS: Sending response to channel for ID: %s", id)
						ch <- &orderResp
						delete(tw.responses, id)
					} else {
						log.Printf("OKXTradingWS: WARNING: No channel found for ID: %s", id)
					}
					tw.mu.Unlock()
				} else {
					log.Printf("OKXTradingWS: Failed to unmarshal order response: %v", err)
				}
			}
		}
	}
}

// keepAlive отправляет ping сообщения
func (tw *TradingWebSocket) keepAlive() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-tw.ctx.Done():
			return
		case <-ticker.C:
			tw.mu.RLock()
			conn := tw.conn
			tw.mu.RUnlock()

			if conn != nil {
				if err := conn.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
					log.Printf("OKXTradingWS: Failed to send ping: %v", err)
				}
			}
		}
	}
}

// generateRequestID генерирует уникальный ID запроса
func (tw *TradingWebSocket) generateRequestID() string {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	tw.requestID++
	return fmt.Sprintf("auto-close-%d-%d", time.Now().UnixNano(), tw.requestID)
}

// IsConnected проверяет, подключен ли WebSocket
func (tw *TradingWebSocket) IsConnected() bool {
	tw.mu.RLock()
	defer tw.mu.RUnlock()
	return tw.isConnected && tw.isAuthd
}

// Close закрывает WebSocket соединение
func (tw *TradingWebSocket) Close() {
	tw.mu.Lock()
	defer tw.mu.Unlock()

	if tw.cancel != nil {
		tw.cancel()
	}

	if tw.conn != nil {
		tw.conn.Close()
		tw.conn = nil
	}

	tw.isConnected = false
	tw.isAuthd = false
	log.Printf("OKXTradingWS: Closed")
}
