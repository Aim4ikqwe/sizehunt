package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
)

// OrderManager управляет размещением и закрытием ордеров на Bybit
type OrderManager struct {
	client *BybitHTTPClient
}

// BybitOrderRequest структура запроса на создание ордера
type BybitOrderRequest struct {
	Category       string `json:"category"`
	Symbol         string `json:"symbol"`
	Side           string `json:"side"`      // Buy, Sell
	OrderType      string `json:"orderType"` // Market, Limit
	Qty            string `json:"qty"`
	TimeInForce    string `json:"timeInForce,omitempty"` // GTC, IOC, FOK
	ReduceOnly     bool   `json:"reduceOnly,omitempty"`
	CloseOnTrigger bool   `json:"closeOnTrigger,omitempty"`
}

// BybitOrderResponse структура ответа на создание ордера
type BybitOrderResponse struct {
	RetCode int    `json:"retCode"`
	RetMsg  string `json:"retMsg"`
	Result  struct {
		OrderID     string `json:"orderId"`
		OrderLinkID string `json:"orderLinkId"`
	} `json:"result"`
	Time int64 `json:"time"`
}

// NewOrderManager создает новый OrderManager
func NewOrderManager(client *BybitHTTPClient) *OrderManager {
	return &OrderManager{client: client}
}

// CloseFullPosition закрывает всю позицию по символу
func (m *OrderManager) CloseFullPosition(symbol, category string) error {
	// Получаем информацию о позиции
	posResp, err := m.client.GetPositionRisk(symbol, category)
	if err != nil {
		return fmt.Errorf("failed to get position: %v", err)
	}

	if len(posResp.Result.List) == 0 {
		return fmt.Errorf("no position found for %s", symbol)
	}

	pos := posResp.Result.List[0]
	size, err := strconv.ParseFloat(pos.Size, 64)
	if err != nil {
		return fmt.Errorf("failed to parse position size: %v", err)
	}

	if size == 0 {
		return fmt.Errorf("position is already zero for %s", symbol)
	}

	// Определяем направление для закрытия (противоположное текущей позиции)
	closeSide := "Buy"
	if pos.Side == "Buy" {
		closeSide = "Sell"
	}

	return m.PlaceMarketOrder(symbol, closeSide, category, math.Abs(size), true)
}

// ClosePositionWithSize закрывает позицию определенного размера
func (m *OrderManager) ClosePositionWithSize(symbol string, size float64, side, category string) error {
	if size == 0 {
		return fmt.Errorf("size cannot be zero")
	}

	// Определяем направление для закрытия (противоположное текущей позиции)
	closeSide := "Buy"
	if side == "Buy" {
		closeSide = "Sell"
	}

	return m.PlaceMarketOrder(symbol, closeSide, category, math.Abs(size), true)
}

// PlaceMarketOrder размещает рыночный ордер
func (m *OrderManager) PlaceMarketOrder(symbol, side, category string, qty float64, reduceOnly bool) error {
	orderReq := BybitOrderRequest{
		Category:    category,
		Symbol:      symbol,
		Side:        side,
		OrderType:   "Market",
		Qty:         strconv.FormatFloat(qty, 'f', -1, 64),
		TimeInForce: "IOC",
		ReduceOnly:  reduceOnly,
	}

	reqBody, err := json.Marshal(orderReq)
	if err != nil {
		return fmt.Errorf("failed to marshal order request: %v", err)
	}

	// Выполняем POST запрос для создания ордера
	resp, err := m.doPostRequest(BybitAPIVersion+"/order/create", reqBody)
	if err != nil {
		return fmt.Errorf("failed to place order: %v", err)
	}

	var orderResp BybitOrderResponse
	if err := json.Unmarshal(resp, &orderResp); err != nil {
		return fmt.Errorf("failed to parse order response: %v", err)
	}

	if orderResp.RetCode != 0 {
		return fmt.Errorf("Bybit order error: %d - %s", orderResp.RetCode, orderResp.RetMsg)
	}

	log.Printf("BybitOrderManager: Order placed successfully: %s, side=%s, qty=%.6f, orderId=%s",
		symbol, side, qty, orderResp.Result.OrderID)

	return nil
}

// doPostRequest выполняет POST запрос с аутентификацией
func (m *OrderManager) doPostRequest(path string, body []byte) ([]byte, error) {
	reqURL := BybitBaseURL + path

	req, err := http.NewRequest("POST", reqURL, bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	// Добавляем заголовки аутентификации
	timestamp := m.client.getTimestamp()
	recvWindow := "5000"
	signature := m.client.sign(timestamp, recvWindow, string(body))

	req.Header.Set("X-BAPI-API-KEY", m.client.APIKey)
	req.Header.Set("X-BAPI-SIGN", signature)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-TIMESTAMP", timestamp)
	req.Header.Set("X-BAPI-RECV-WINDOW", recvWindow)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %v", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %v", err)
	}

	return respBody, nil
}
