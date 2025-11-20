// internal/binance/service/http_client.go
package service

import (
	"context"
	"fmt"
	"log"
	"sizehunt/internal/binance/entity"
	"strconv"

	"github.com/adshao/go-binance/v2"         // Основной пакет (спот)
	"github.com/adshao/go-binance/v2/futures" // Пакет фьючерсов
)

type BinanceHTTPClient struct {
	SpotClient    *binance.Client
	FuturesClient *futures.Client
}

func NewBinanceHTTPClient(apiKey, secretKey string) *BinanceHTTPClient {
	client := &BinanceHTTPClient{}
	if apiKey != "" && secretKey != "" {
		client.SpotClient = binance.NewClient(apiKey, secretKey)
		client.FuturesClient = binance.NewFuturesClient(apiKey, secretKey)
	} else {
		// Создаём клиенты без ключей для публичных запросов
		client.SpotClient = binance.NewClient("", "")
		client.FuturesClient = binance.NewFuturesClient("", "")
	}
	return client
}

func (c *BinanceHTTPClient) GetOrderBook(symbol string, limit int, market string) (*OrderBook, error) {
	var bidsData interface{} // Может быть [][]string (для спота) или []futures.Bid/Ask (для фьючерсов)
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
		bidsData = resp.Bids // Тип: []futures.Bid
		asksData = resp.Asks // Тип: []futures.Ask
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
		bidsData = resp.Bids // Тип: [][]string
		asksData = resp.Asks // Тип: [][]string
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
	// Простая проверка, что ключи не пустые
	if c.SpotClient.APIKey == "" && c.FuturesClient.APIKey == "" {
		return fmt.Errorf("API key is empty")
	}
	// Для более точной проверки можно сделать запрос, например, к /api/v3/account
	return nil
}
