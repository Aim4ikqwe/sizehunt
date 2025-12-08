package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sizehunt/internal/okx/entity"
	"strconv"
	"time"

	"github.com/sony/gobreaker"
	"golang.org/x/net/proxy"
)

const (
	OKXBaseURL    = "https://www.okx.com"
	OKXAPIVersion = "/api/v5"
)

// OrderBook представляет стакан OKX
type OrderBook struct {
	Bids []entity.Order
	Asks []entity.Order
}

// OKXHTTPClient клиент для работы с OKX REST API
type OKXHTTPClient struct {
	APIKey     string
	SecretKey  string
	Passphrase string
	HTTPClient *http.Client
	cb         *gobreaker.CircuitBreaker
}

// OKXOrderBookResponse структура ответа OKX для ордербука
type OKXOrderBookResponse struct {
	Code string `json:"code"`
	Msg  string `json:"msg"`
	Data []struct {
		Asks [][]string `json:"asks"` // [price, size, deprecated, numOrders]
		Bids [][]string `json:"bids"` // [price, size, deprecated, numOrders]
		Ts   string     `json:"ts"`
	} `json:"data"`
}

// OKXPositionResponse структура ответа OKX для позиций
type OKXPositionResponse struct {
	Code string `json:"code"`
	Msg  string `json:"msg"`
	Data []struct {
		InstID      string `json:"instId"`
		Pos         string `json:"pos"`      // количество позиции
		PosSide     string `json:"posSide"`  // long, short, net
		AvgPx       string `json:"avgPx"`    // средняя цена входа
		Upl         string `json:"upl"`      // нереализованный PnL
		Lever       string `json:"lever"`    // плечо
		InstType    string `json:"instType"` // SWAP, FUTURES, OPTION
		MgnMode     string `json:"mgnMode"`  // cross, isolated
		NotionalUsd string `json:"notionalUsd"`
	} `json:"data"`
}

// NewOKXHTTPClient создает новый HTTP клиент OKX без прокси
func NewOKXHTTPClient(apiKey, secretKey, passphrase string) *OKXHTTPClient {
	return NewOKXHTTPClientWithProxy(apiKey, secretKey, passphrase, "")
}

// NewOKXHTTPClientWithProxy создает новый HTTP клиент OKX с поддержкой прокси
func NewOKXHTTPClientWithProxy(apiKey, secretKey, passphrase, proxyAddr string) *OKXHTTPClient {
	client := &OKXHTTPClient{
		APIKey:     apiKey,
		SecretKey:  secretKey,
		Passphrase: passphrase,
	}

	// Настройка circuit breaker
	client.cb = gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        "okx-api",
		MaxRequests: 3,
		Interval:    5 * time.Second,
		Timeout:     30 * time.Second,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
			return counts.Requests >= 3 && failureRatio >= 0.6
		},
		OnStateChange: func(name string, from gobreaker.State, to gobreaker.State) {
			log.Printf("Circuit breaker '%s' changed from %s to %s", name, from, to)
		},
	})

	// Создаем HTTP клиент с прокси, если указан
	var httpClient *http.Client
	if proxyAddr != "" {
		proxyURL := &url.URL{
			Scheme: "socks5h",
			Host:   proxyAddr,
		}

		dialer, err := proxy.FromURL(proxyURL, proxy.Direct)
		if err != nil {
			log.Printf("Failed to create SOCKS5 dialer: %v", err)
			httpClient = &http.Client{Timeout: 30 * time.Second}
		} else {
			transport := &http.Transport{
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					return dialer.Dial(network, addr)
				},
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
			}
			httpClient = &http.Client{
				Transport: transport,
				Timeout:   30 * time.Second,
			}
		}
	} else {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}

	client.HTTPClient = httpClient
	return client
}

// sign создает подпись для OKX API
func (c *OKXHTTPClient) sign(timestamp, method, requestPath, body string) string {
	message := timestamp + method + requestPath + body
	h := hmac.New(sha256.New, []byte(c.SecretKey))
	h.Write([]byte(message))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// getTimestamp возвращает текущее время в формате ISO 8601
func (c *OKXHTTPClient) getTimestamp() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

// doRequest выполняет HTTP запрос к OKX API
func (c *OKXHTTPClient) doRequest(method, path string, body string) ([]byte, error) {
	timestamp := c.getTimestamp()
	signature := c.sign(timestamp, method, path, body)

	reqURL := OKXBaseURL + path

	var req *http.Request
	var err error

	if body != "" {
		req, err = http.NewRequest(method, reqURL, nil)
	} else {
		req, err = http.NewRequest(method, reqURL, nil)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	// Добавляем заголовки аутентификации OKX
	if c.APIKey != "" {
		req.Header.Set("OK-ACCESS-KEY", c.APIKey)
		req.Header.Set("OK-ACCESS-SIGN", signature)
		req.Header.Set("OK-ACCESS-TIMESTAMP", timestamp)
		req.Header.Set("OK-ACCESS-PASSPHRASE", c.Passphrase)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
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

// GetOrderBook получает ордербук для инструмента OKX
func (c *OKXHTTPClient) GetOrderBook(instID string, depth int, instType string) (*OrderBook, error) {
	start := time.Now()

	// Используем circuit breaker
	result, err := c.cb.Execute(func() (interface{}, error) {
		path := fmt.Sprintf("%s/market/books?instId=%s&sz=%d", OKXAPIVersion, instID, depth)

		respBody, err := c.doRequest("GET", path, "")
		if err != nil {
			return nil, err
		}

		var okxResp OKXOrderBookResponse
		if err := json.Unmarshal(respBody, &okxResp); err != nil {
			return nil, fmt.Errorf("failed to parse response: %v", err)
		}

		if okxResp.Code != "0" {
			return nil, fmt.Errorf("OKX API error: %s - %s", okxResp.Code, okxResp.Msg)
		}

		if len(okxResp.Data) == 0 {
			return nil, fmt.Errorf("no orderbook data returned for %s", instID)
		}

		// Конвертируем данные в наш формат
		orderBook := &OrderBook{
			Bids: make([]entity.Order, 0, len(okxResp.Data[0].Bids)),
			Asks: make([]entity.Order, 0, len(okxResp.Data[0].Asks)),
		}

		for _, bid := range okxResp.Data[0].Bids {
			if len(bid) >= 2 {
				price, _ := strconv.ParseFloat(bid[0], 64)
				qty, _ := strconv.ParseFloat(bid[1], 64)
				orderBook.Bids = append(orderBook.Bids, entity.Order{
					Price:          price,
					Quantity:       qty,
					Side:           "buy",
					InstrumentType: instType,
					InstrumentID:   instID,
				})
			}
		}

		for _, ask := range okxResp.Data[0].Asks {
			if len(ask) >= 2 {
				price, _ := strconv.ParseFloat(ask[0], 64)
				qty, _ := strconv.ParseFloat(ask[1], 64)
				orderBook.Asks = append(orderBook.Asks, entity.Order{
					Price:          price,
					Quantity:       qty,
					Side:           "sell",
					InstrumentType: instType,
					InstrumentID:   instID,
				})
			}
		}

		return orderBook, nil
	})

	duration := time.Since(start).Seconds()
	log.Printf("OKXHTTPClient: GetOrderBook for %s took %.3fs", instID, duration)

	if err != nil {
		return nil, err
	}

	return result.(*OrderBook), nil
}

// GetPositionRisk получает информацию о позиции
func (c *OKXHTTPClient) GetPositionRisk(instID string) (*OKXPositionResponse, error) {
	path := fmt.Sprintf("%s/account/positions?instId=%s", OKXAPIVersion, instID)

	respBody, err := c.doRequest("GET", path, "")
	if err != nil {
		return nil, err
	}

	var posResp OKXPositionResponse
	if err := json.Unmarshal(respBody, &posResp); err != nil {
		return nil, fmt.Errorf("failed to parse position response: %v", err)
	}

	if posResp.Code != "0" {
		return nil, fmt.Errorf("OKX API error: %s - %s", posResp.Code, posResp.Msg)
	}

	return &posResp, nil
}

// ValidateAPIKey проверяет валидность API ключей
func (c *OKXHTTPClient) ValidateAPIKey() error {
	if c.APIKey == "" || c.SecretKey == "" || c.Passphrase == "" {
		return fmt.Errorf("API key, secret key, or passphrase is empty")
	}

	// Делаем тестовый запрос для проверки ключей
	path := fmt.Sprintf("%s/account/balance", OKXAPIVersion)
	_, err := c.doRequest("GET", path, "")
	if err != nil {
		return fmt.Errorf("API key validation failed: %v", err)
	}

	return nil
}

// GetCircuitBreaker возвращает circuit breaker для использования в других компонентах
func (c *OKXHTTPClient) GetCircuitBreaker() *gobreaker.CircuitBreaker {
	return c.cb
}
