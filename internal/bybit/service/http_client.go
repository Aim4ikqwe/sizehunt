package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sizehunt/internal/bybit/entity"
	"strconv"
	"time"

	"github.com/sony/gobreaker"
	"golang.org/x/net/proxy"
)

const (
	BybitBaseURL    = "https://api.bybit.com"
	BybitAPIVersion = "/v5"
)

// OrderBook представляет стакан Bybit
type OrderBook struct {
	Bids []entity.Order
	Asks []entity.Order
}

// BybitHTTPClient клиент для работы с Bybit REST API v5
type BybitHTTPClient struct {
	APIKey     string
	SecretKey  string
	HTTPClient *http.Client
	cb         *gobreaker.CircuitBreaker
}

// BybitOrderBookResponse структура ответа Bybit для ордербука
type BybitOrderBookResponse struct {
	RetCode int    `json:"retCode"`
	RetMsg  string `json:"retMsg"`
	Result  struct {
		S  string     `json:"s"`  // symbol
		B  [][]string `json:"b"`  // bids [price, size]
		A  [][]string `json:"a"`  // asks [price, size]
		Ts int64      `json:"ts"` // timestamp
		U  int64      `json:"u"`  // update ID
	} `json:"result"`
	Time int64 `json:"time"`
}

// BybitPositionResponse структура ответа Bybit для позиций
type BybitPositionResponse struct {
	RetCode int    `json:"retCode"`
	RetMsg  string `json:"retMsg"`
	Result  struct {
		List []struct {
			Symbol         string `json:"symbol"`
			Side           string `json:"side"`           // Buy, Sell
			Size           string `json:"size"`           // позиция
			PositionValue  string `json:"positionValue"`  // стоимость позиции
			AvgPrice       string `json:"avgPrice"`       // средняя цена входа
			UnrealisedPnl  string `json:"unrealisedPnl"`  // нереализованный PnL
			Leverage       string `json:"leverage"`       // плечо
			PositionStatus string `json:"positionStatus"` // Normal, Liq, Adl
			TakeProfit     string `json:"takeProfit"`
			StopLoss       string `json:"stopLoss"`
			TrailingStop   string `json:"trailingStop"`
		} `json:"list"`
	} `json:"result"`
	Time int64 `json:"time"`
}

// BybitServerTimeResponse структура ответа для времени сервера
type BybitServerTimeResponse struct {
	RetCode int    `json:"retCode"`
	RetMsg  string `json:"retMsg"`
	Result  struct {
		TimeSecond string `json:"timeSecond"`
		TimeNano   string `json:"timeNano"`
	} `json:"result"`
	Time int64 `json:"time"`
}

// NewBybitHTTPClient создает новый HTTP клиент Bybit без прокси
func NewBybitHTTPClient(apiKey, secretKey string) *BybitHTTPClient {
	return NewBybitHTTPClientWithProxy(apiKey, secretKey, "")
}

// NewBybitHTTPClientWithProxy создает новый HTTP клиент Bybit с поддержкой прокси
func NewBybitHTTPClientWithProxy(apiKey, secretKey, proxyAddr string) *BybitHTTPClient {
	client := &BybitHTTPClient{
		APIKey:    apiKey,
		SecretKey: secretKey,
	}

	// Настройка circuit breaker
	client.cb = gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        "bybit-api",
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

// sign создает HMAC SHA256 подпись для Bybit API
func (c *BybitHTTPClient) sign(timestamp, recvWindow, queryString string) string {
	message := timestamp + c.APIKey + recvWindow + queryString
	h := hmac.New(sha256.New, []byte(c.SecretKey))
	h.Write([]byte(message))
	return hex.EncodeToString(h.Sum(nil))
}

// getTimestamp возвращает текущее время в миллисекундах
func (c *BybitHTTPClient) getTimestamp() string {
	return strconv.FormatInt(time.Now().UnixMilli(), 10)
}

// doRequest выполняет HTTP запрос к Bybit API
func (c *BybitHTTPClient) doRequest(method, path string, params map[string]string, signed bool) ([]byte, error) {
	reqURL := BybitBaseURL + path

	// Формируем query string
	queryString := ""
	if len(params) > 0 {
		values := url.Values{}
		for k, v := range params {
			values.Add(k, v)
		}
		queryString = values.Encode()
		if method == "GET" {
			reqURL += "?" + queryString
		}
	}

	req, err := http.NewRequest(method, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	// Добавляем заголовки аутентификации Bybit
	if signed && c.APIKey != "" {
		timestamp := c.getTimestamp()
		recvWindow := "5000"
		signature := c.sign(timestamp, recvWindow, queryString)

		req.Header.Set("X-BAPI-API-KEY", c.APIKey)
		req.Header.Set("X-BAPI-SIGN", signature)
		req.Header.Set("X-BAPI-SIGN-TYPE", "2")
		req.Header.Set("X-BAPI-TIMESTAMP", timestamp)
		req.Header.Set("X-BAPI-RECV-WINDOW", recvWindow)
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

// GetOrderBook получает ордербук для символа Bybit
func (c *BybitHTTPClient) GetOrderBook(symbol string, depth int, category string) (*OrderBook, error) {
	start := time.Now()

	// Используем circuit breaker
	result, err := c.cb.Execute(func() (interface{}, error) {
		params := map[string]string{
			"category": category,
			"symbol":   symbol,
			"limit":    strconv.Itoa(depth),
		}

		path := BybitAPIVersion + "/market/orderbook"
		respBody, err := c.doRequest("GET", path, params, false)
		if err != nil {
			return nil, err
		}

		var bybitResp BybitOrderBookResponse
		if err := json.Unmarshal(respBody, &bybitResp); err != nil {
			return nil, fmt.Errorf("failed to parse response: %v", err)
		}

		if bybitResp.RetCode != 0 {
			return nil, fmt.Errorf("Bybit API error: %d - %s", bybitResp.RetCode, bybitResp.RetMsg)
		}

		// Конвертируем данные в наш формат
		orderBook := &OrderBook{
			Bids: make([]entity.Order, 0, len(bybitResp.Result.B)),
			Asks: make([]entity.Order, 0, len(bybitResp.Result.A)),
		}

		for _, bid := range bybitResp.Result.B {
			if len(bid) >= 2 {
				price, _ := strconv.ParseFloat(bid[0], 64)
				qty, _ := strconv.ParseFloat(bid[1], 64)
				orderBook.Bids = append(orderBook.Bids, entity.Order{
					Price:    price,
					Quantity: qty,
					Side:     "buy",
					Category: category,
					Symbol:   symbol,
				})
			}
		}

		for _, ask := range bybitResp.Result.A {
			if len(ask) >= 2 {
				price, _ := strconv.ParseFloat(ask[0], 64)
				qty, _ := strconv.ParseFloat(ask[1], 64)
				orderBook.Asks = append(orderBook.Asks, entity.Order{
					Price:    price,
					Quantity: qty,
					Side:     "sell",
					Category: category,
					Symbol:   symbol,
				})
			}
		}

		return orderBook, nil
	})

	duration := time.Since(start).Seconds()
	log.Printf("BybitHTTPClient: GetOrderBook for %s took %.3fs", symbol, duration)

	if err != nil {
		return nil, err
	}

	return result.(*OrderBook), nil
}

// Ping проверяет доступность API и поддерживает соединение
func (c *BybitHTTPClient) Ping() error {
	path := BybitAPIVersion + "/market/time"
	_, err := c.doRequest("GET", path, nil, false)
	return err
}

// GetPositionRisk получает информацию о позиции
// Если symbol пустой, возвращает все позиции для settleCoin=USDT
func (c *BybitHTTPClient) GetPositionRisk(symbol, category string) (*BybitPositionResponse, error) {
	params := map[string]string{
		"category": category,
	}

	// Если symbol не указан, используем settleCoin для получения всех позиций
	if symbol != "" {
		params["symbol"] = symbol
	} else {
		params["settleCoin"] = "USDT" // Получаем все USDT-M позиции
	}

	path := BybitAPIVersion + "/position/list"
	respBody, err := c.doRequest("GET", path, params, true)
	if err != nil {
		return nil, err
	}

	var posResp BybitPositionResponse
	if err := json.Unmarshal(respBody, &posResp); err != nil {
		return nil, fmt.Errorf("failed to parse position response: %v", err)
	}

	if posResp.RetCode != 0 {
		return nil, fmt.Errorf("Bybit API error: %d - %s", posResp.RetCode, posResp.RetMsg)
	}

	return &posResp, nil
}

// ValidateAPIKey проверяет валидность API ключей
func (c *BybitHTTPClient) ValidateAPIKey() error {
	if c.APIKey == "" || c.SecretKey == "" {
		return fmt.Errorf("API key or secret key is empty")
	}

	// Делаем тестовый запрос для проверки ключей
	params := map[string]string{
		"accountType": "UNIFIED",
	}
	path := BybitAPIVersion + "/account/wallet-balance"
	_, err := c.doRequest("GET", path, params, true)
	if err != nil {
		return fmt.Errorf("API key validation failed: %v", err)
	}

	return nil
}

// GetCircuitBreaker возвращает circuit breaker для использования в других компонентах
func (c *BybitHTTPClient) GetCircuitBreaker() *gobreaker.CircuitBreaker {
	return c.cb
}
