package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"
)

// OrderManager управляет ордерами на OKX
type OrderManager struct {
	client *OKXHTTPClient
}

// OKXOrderRequest структура запроса для размещения ордера
type OKXOrderRequest struct {
	InstID     string `json:"instId"`
	TdMode     string `json:"tdMode"`            // cross, isolated, cash
	Side       string `json:"side"`              // buy, sell
	OrdType    string `json:"ordType"`           // market, limit, etc.
	Sz         string `json:"sz"`                // размер
	PosSide    string `json:"posSide,omitempty"` // long, short (для hedge mode)
	ReduceOnly bool   `json:"reduceOnly,omitempty"`
}

// OKXOrderResponse структура ответа на размещение ордера
type OKXOrderResponse struct {
	Code string `json:"code"`
	Msg  string `json:"msg"`
	Data []struct {
		OrdID   string `json:"ordId"`
		ClOrdID string `json:"clOrdId"`
		SCode   string `json:"sCode"`
		SMsg    string `json:"sMsg"`
	} `json:"data"`
}

// NewOrderManager создает новый OrderManager
func NewOrderManager(client *OKXHTTPClient) *OrderManager {
	return &OrderManager{
		client: client,
	}
}

// CloseFullPosition закрывает полную позицию на OKX
func (m *OrderManager) CloseFullPosition(instID, instType string) error {
	startTime := time.Now()
	defer func() {
		log.Printf("OKXOrderManager: CloseFullPosition for %s completed (total time: %v)", instID, time.Since(startTime))
	}()

	// Получаем информацию о позиции
	positionFetchStart := time.Now()
	posResp, err := m.client.GetPositionRisk(instID)
	positionFetchDuration := time.Since(positionFetchStart)
	if err != nil {
		return fmt.Errorf("failed to get position: %v", err)
	}
	log.Printf("OKXOrderManager: Position fetch took %v", positionFetchDuration)

	if len(posResp.Data) == 0 {
		log.Printf("OKXOrderManager: No position found for %s", instID)
		return nil
	}

	position := posResp.Data[0]
	posAmt, err := strconv.ParseFloat(position.Pos, 64)
	if err != nil {
		return fmt.Errorf("failed to parse position amount: %v", err)
	}

	if posAmt == 0 {
		log.Printf("OKXOrderManager: Position is zero for %s", instID)
		return nil
	}

	log.Printf("OKXOrderManager: Current position for %s: %.6f, posSide: %s, mgnMode: %s",
		instID, posAmt, position.PosSide, position.MgnMode)

	// Определяем сторону для закрытия
	var side string
	var sz string
	if posAmt > 0 {
		side = "sell"
		sz = fmt.Sprintf("%.8f", posAmt)
	} else {
		side = "buy"
		sz = fmt.Sprintf("%.8f", -posAmt)
	}

	// Создаем ордер для закрытия позиции
	orderReq := OKXOrderRequest{
		InstID:     instID,
		TdMode:     position.MgnMode,
		Side:       side,
		OrdType:    "market",
		Sz:         sz,
		ReduceOnly: true,
	}

	// Добавляем posSide для hedge mode
	if position.PosSide != "net" {
		orderReq.PosSide = position.PosSide
	}

	orderJSON, err := json.Marshal(orderReq)
	if err != nil {
		return fmt.Errorf("failed to marshal order request: %v", err)
	}

	// Отправляем ордер
	path := "/api/v5/trade/order"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	timestamp := m.client.getTimestamp()
	signature := m.client.sign(timestamp, "POST", path, string(orderJSON))

	log.Printf("OKXOrderManager: Placing market %s order for %s (size: %s)", side, instID, sz)
	orderExecutionStart := time.Now()
	respBody, err := m.doPostRequest(ctx, path, string(orderJSON), timestamp, signature)
	orderExecutionDuration := time.Since(orderExecutionStart)
	if err != nil {
		log.Printf("OKXOrderManager: Market order execution FAILED after %v: %v", orderExecutionDuration, err)
		return fmt.Errorf("failed to place close order: %v", err)
	}
	log.Printf("OKXOrderManager: Market order HTTP request completed in %v", orderExecutionDuration)

	var orderResp OKXOrderResponse
	if err := json.Unmarshal(respBody, &orderResp); err != nil {
		return fmt.Errorf("failed to parse order response: %v", err)
	}

	if orderResp.Code != "0" {
		log.Printf("OKXOrderManager: Market order REJECTED: %s - %s (execution time: %v)",
			orderResp.Code, orderResp.Msg, orderExecutionDuration)
		return fmt.Errorf("OKX order error: %s - %s", orderResp.Code, orderResp.Msg)
	}

	if len(orderResp.Data) > 0 {
		data := orderResp.Data[0]
		if data.SCode != "0" {
			log.Printf("OKXOrderManager: Market order REJECTED: %s - %s (execution time: %v)",
				data.SCode, data.SMsg, orderExecutionDuration)
			return fmt.Errorf("OKX order error: %s - %s", data.SCode, data.SMsg)
		}
		log.Printf("OKXOrderManager: ✅ Market order EXECUTED successfully in %v, orderId: %s",
			orderExecutionDuration, data.OrdID)
	}

	return nil
}

// ClosePositionWithSize закрывает позицию с уже известным размером (из WebSocket)
func (m *OrderManager) ClosePositionWithSize(instID string, positionSize float64, posSide, mgnMode string) error {
	startTime := time.Now()
	defer func() {
		log.Printf("OKXOrderManager: ClosePositionWithSize for %s completed (total time: %v)", instID, time.Since(startTime))
	}()

	if positionSize == 0 {
		log.Printf("OKXOrderManager: Position is zero for %s, nothing to close", instID)
		return nil
	}

	log.Printf("OKXOrderManager: Closing position for %s: size=%.6f, side=%s, mgnMode=%s (from WebSocket)",
		instID, positionSize, posSide, mgnMode)

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

	// Создаем ордер для закрытия позиции
	orderReq := OKXOrderRequest{
		InstID:     instID,
		TdMode:     mgnMode,
		Side:       side,
		OrdType:    "market",
		Sz:         sz,
		ReduceOnly: true,
	}

	// Добавляем posSide для hedge mode
	if posSide != "net" {
		orderReq.PosSide = posSide
	}

	orderJSON, err := json.Marshal(orderReq)
	if err != nil {
		return fmt.Errorf("failed to marshal order request: %v", err)
	}

	// Отправляем ордер
	path := "/api/v5/trade/order"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	timestamp := m.client.getTimestamp()
	signature := m.client.sign(timestamp, "POST", path, string(orderJSON))

	log.Printf("OKXOrderManager: Placing market %s order for %s (size: %s)", side, instID, sz)
	orderExecutionStart := time.Now()
	respBody, err := m.doPostRequest(ctx, path, string(orderJSON), timestamp, signature)
	orderExecutionDuration := time.Since(orderExecutionStart)
	if err != nil {
		log.Printf("OKXOrderManager: Market order execution FAILED after %v: %v", orderExecutionDuration, err)
		return fmt.Errorf("failed to place close order: %v", err)
	}
	log.Printf("OKXOrderManager: Market order HTTP request completed in %v", orderExecutionDuration)

	var orderResp OKXOrderResponse
	if err := json.Unmarshal(respBody, &orderResp); err != nil {
		return fmt.Errorf("failed to parse order response: %v", err)
	}

	if orderResp.Code != "0" {
		log.Printf("OKXOrderManager: Market order REJECTED: %s - %s (execution time: %v)",
			orderResp.Code, orderResp.Msg, orderExecutionDuration)
		return fmt.Errorf("OKX order error: %s - %s", orderResp.Code, orderResp.Msg)
	}

	if len(orderResp.Data) > 0 {
		data := orderResp.Data[0]
		if data.SCode != "0" {
			log.Printf("OKXOrderManager: Market order REJECTED: %s - %s (execution time: %v)",
				data.SCode, data.SMsg, orderExecutionDuration)
			return fmt.Errorf("OKX order error: %s - %s", data.SCode, data.SMsg)
		}
		log.Printf("OKXOrderManager: ✅ Market order EXECUTED successfully in %v, orderId: %s (WebSocket data)",
			orderExecutionDuration, data.OrdID)
	}

	return nil
}

// doPostRequest выполняет POST запрос к OKX API
func (m *OrderManager) doPostRequest(ctx context.Context, path, body, timestamp, signature string) ([]byte, error) {
	reqURL := OKXBaseURL + path
	req, err := http.NewRequestWithContext(ctx, "POST", reqURL, bytes.NewBufferString(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("OK-ACCESS-KEY", m.client.APIKey)
	req.Header.Set("OK-ACCESS-SIGN", signature)
	req.Header.Set("OK-ACCESS-TIMESTAMP", timestamp)
	req.Header.Set("OK-ACCESS-PASSPHRASE", m.client.Passphrase)
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

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}
