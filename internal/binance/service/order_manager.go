package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"
)

type OrderManager struct {
	APIKey    string
	SecretKey string
	BaseURL   string
}

func NewOrderManager(apiKey, secretKey, baseURL string) *OrderManager {
	return &OrderManager{
		APIKey:    apiKey,
		SecretKey: secretKey,
		BaseURL:   baseURL,
	}
}

type ClosePositionRequest struct {
	Symbol   string `json:"symbol"`
	Side     string `json:"side"` // "BUY" or "SELL"
	Type     string `json:"type"` // "MARKET"
	Quantity string `json:"quantity"`
}

func (om *OrderManager) ClosePosition(symbol, side, quantity string) error {
	log.Printf("OrderManager: ClosePosition called for symbol %s, side %s, quantity %s", symbol, side, quantity)

	endpoint := fmt.Sprintf("%s/fapi/v1/order", om.BaseURL)

	params := url.Values{}
	params.Add("symbol", symbol)
	params.Add("side", side)
	params.Add("type", "MARKET")
	params.Add("quantity", quantity)
	params.Add("timestamp", fmt.Sprintf("%d", time.Now().UnixMilli()))

	// Генерация подписи
	signature := om.generateSignature(params.Encode())
	params.Add("signature", signature)

	reqURL := endpoint + "?" + params.Encode()

	log.Printf("OrderManager: Sending request to %s with params: %s", reqURL, params.Encode())

	req, err := http.NewRequest("POST", reqURL, nil)
	if err != nil {
		log.Printf("OrderManager: Failed to create request: %v", err)
		return err
	}

	req.Header.Set("X-MBX-APIKEY", om.APIKey)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("OrderManager: Failed to send request: %v", err)
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		log.Printf("OrderManager: Binance API error: %s (status: %d)", string(body), resp.StatusCode)
		return fmt.Errorf("Binance API error: %s", string(body))
	}

	log.Printf("OrderManager: Position closed successfully for symbol %s, response: %s", symbol, string(body))
	return nil
}

func (om *OrderManager) generateSignature(message string) string {
	log.Printf("OrderManager: Generating signature for message: %s", message)
	key := []byte(om.SecretKey)
	h := hmac.New(sha256.New, key)
	h.Write([]byte(message))
	signature := hex.EncodeToString(h.Sum(nil))
	log.Printf("OrderManager: Generated signature: %s", signature)
	return signature
}
