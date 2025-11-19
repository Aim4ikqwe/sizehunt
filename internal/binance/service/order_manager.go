package service

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	endpoint := fmt.Sprintf("%s/fapi/v1/order", om.BaseURL)

	params := url.Values{}
	params.Add("symbol", symbol)
	params.Add("side", side)
	params.Add("type", "MARKET")
	params.Add("quantity", quantity)

	// Подпись (упрощённо, в реальности нужна подпись по HMAC)
	// signature := sign(params.Encode(), om.SecretKey)
	// params.Add("signature", signature)

	reqURL := endpoint + "?" + params.Encode()

	req, err := http.NewRequest("POST", reqURL, nil)
	if err != nil {
		return err
	}

	req.Header.Set("X-MBX-APIKEY", om.APIKey)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Binance API error: %s", string(body))
	}

	return nil
}
