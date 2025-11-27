package service

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"sizehunt/internal/binance/entity"
	"sizehunt/internal/config"
	"strconv"
	"time"

	"github.com/adshao/go-binance/v2"
	"github.com/adshao/go-binance/v2/futures"
	"github.com/sony/gobreaker"
	"golang.org/x/net/proxy"
)

type BinanceHTTPClient struct {
	SpotClient    *binance.Client
	FuturesClient *futures.Client
	futuresCB     *gobreaker.CircuitBreaker // Добавляем circuit breaker для фьючерсов
	spotCB        *gobreaker.CircuitBreaker // Добавляем circuit breaker для спота
}

// NewBinanceHTTPClientWithProxy creates a new client with SOCKS5 proxy support
func NewBinanceHTTPClientWithProxy(apiKey, secretKey, proxyAddr string, cfg *config.Config) *BinanceHTTPClient {
	client := &BinanceHTTPClient{}

	// Настройка circuit breakers
	client.futuresCB = gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        "binance-futures",
		MaxRequests: 3, // минимальное количество запросов для открытия цепи
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

	client.spotCB = gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        "binance-spot",
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
	// Create HTTP client with SOCKS5 proxy if provided
	var httpClient *http.Client
	if proxyAddr != "" {
		proxyURL := &url.URL{
			Scheme: "socks5",
			Host:   proxyAddr,
		}

		dialer, err := proxy.FromURL(proxyURL, proxy.Direct)
		if err != nil {
			log.Printf("Failed to create SOCKS5 dialer: %v", err)
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
	}

	// Initialize clients with proper options
	if apiKey != "" && secretKey != "" {
		// Spot client
		if httpClient != nil {
			client.SpotClient = binance.NewClient(apiKey, secretKey)
			client.SpotClient.HTTPClient = httpClient
		} else {
			client.SpotClient = binance.NewClient(apiKey, secretKey)
		}

		// Futures client
		if httpClient != nil {
			client.FuturesClient = futures.NewClient(apiKey, secretKey)
			client.FuturesClient.HTTPClient = httpClient
		} else {
			client.FuturesClient = futures.NewClient(apiKey, secretKey)
		}
	} else {
		// Public clients without API keys
		if httpClient != nil {
			client.SpotClient = binance.NewClient("", "")
			client.SpotClient.HTTPClient = httpClient
			client.FuturesClient = futures.NewClient("", "")
			client.FuturesClient.HTTPClient = httpClient
		} else {
			client.SpotClient = binance.NewClient("", "")
			client.FuturesClient = futures.NewClient("", "")
		}
	}

	return client
}

func NewBinanceHTTPClient(apiKey, secretKey string) *BinanceHTTPClient {
	return NewBinanceHTTPClientWithProxy(apiKey, secretKey, "", nil)
}

func (c *BinanceHTTPClient) GetOrderBook(symbol string, limit int, market string) (*OrderBook, error) {
	var bidsData interface{}
	var asksData interface{}
	switch market {
	case "futures":
		if c.FuturesClient == nil {
			return nil, fmt.Errorf("futures client not initialized, API keys required")
		}
		service := c.FuturesClient.NewDepthService()
		service.Symbol(symbol)
		if limit > 0 {
			service.Limit(limit)
		}
		resp, err := service.Do(context.Background())
		if err != nil {
			return nil, err
		}
		bidsData = resp.Bids
		asksData = resp.Asks
	case "spot":
		fallthrough
	default:
		if c.SpotClient == nil {
			return nil, fmt.Errorf("spot client not initialized, API keys required")
		}
		service := c.SpotClient.NewDepthService()
		service.Symbol(symbol)
		if limit > 0 {
			service.Limit(limit)
		}
		resp, err := service.Do(context.Background())
		if err != nil {
			return nil, err
		}
		bidsData = resp.Bids
		asksData = resp.Asks

	}

	// Преобразуем bids
	var bids []entity.Order
	switch v := bidsData.(type) {
	case [][]string: // Для спота
		bids = make([]entity.Order, len(v))
		for i, b := range v {
			price, _ := strconv.ParseFloat(b[0], 64)
			qty, _ := strconv.ParseFloat(b[1], 64)
			bids[i] = entity.Order{
				Price:     price,
				Quantity:  qty,
				Side:      "BUY",
				UpdatedAt: 0,
			}
		}
	case []futures.Bid: // Для фьючерсов
		bids = make([]entity.Order, len(v))
		for i, b := range v {
			price, _ := strconv.ParseFloat(b.Price, 64)
			qty, _ := strconv.ParseFloat(b.Quantity, 64)
			bids[i] = entity.Order{
				Price:     price,
				Quantity:  qty,
				Side:      "BUY",
				UpdatedAt: 0,
			}
		}
	default:
		log.Printf("GetOrderBook: Unexpected bids data type: %T", bidsData)
		return nil, fmt.Errorf("unexpected bids data type: %T", bidsData)
	}

	// Преобразуем asks
	var asks []entity.Order
	switch v := asksData.(type) {
	case [][]string: // Для спота
		asks = make([]entity.Order, len(v))
		for i, a := range v {
			price, _ := strconv.ParseFloat(a[0], 64)
			qty, _ := strconv.ParseFloat(a[1], 64)
			asks[i] = entity.Order{
				Price:     price,
				Quantity:  qty,
				Side:      "SELL",
				UpdatedAt: 0,
			}
		}
	case []futures.Ask: // Для фьючерсов
		asks = make([]entity.Order, len(v))
		for i, a := range v {
			price, _ := strconv.ParseFloat(a.Price, 64)
			qty, _ := strconv.ParseFloat(a.Quantity, 64)
			asks[i] = entity.Order{
				Price:     price,
				Quantity:  qty,
				Side:      "SELL",
				UpdatedAt: 0,
			}
		}
	default:
		log.Printf("GetOrderBook: Unexpected asks data type: %T", asksData)
		return nil, fmt.Errorf("unexpected asks data type: %T", asksData)
	}
	return &OrderBook{Bids: bids, Asks: asks}, nil
}

func (c *BinanceHTTPClient) ValidateAPIKey() error {
	if c.SpotClient.APIKey == "" && c.FuturesClient.APIKey == "" {
		return fmt.Errorf("API key is empty")
	}
	return nil
}
