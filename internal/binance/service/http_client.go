package service

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sizehunt/internal/binance/entity"
	"strconv"
)

type BinanceHTTPClient struct {
	BaseURL string
	APIKey  string
}

func NewBinanceHTTPClient(apiKey string) *BinanceHTTPClient {
	return &BinanceHTTPClient{
		BaseURL: "https://api.binance.com", // по умолчанию спот
		APIKey:  apiKey,
	}
}

type OrderBookResponse struct {
	Symbol       string     `json:"symbol"`
	LastUpdateID int64      `json:"lastUpdateId"`
	Bids         [][]string `json:"bids"`
	Asks         [][]string `json:"asks"`
}

func (c *BinanceHTTPClient) GetOrderBook(symbol string, limit int, market string) (*OrderBook, error) {
	var endpoint string
	switch market {
	case "futures":
		endpoint = "https://fapi.binance.com/fapi/v1/depth"
	default:
		endpoint = "https://api.binance.com/api/v3/depth"
	}

	params := url.Values{}
	params.Add("symbol", symbol)
	params.Add("limit", fmt.Sprintf("%d", limit))

	reqURL := endpoint + "?" + params.Encode()

	resp, err := http.Get(reqURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Binance API error: %s", string(body))
	}

	var ob OrderBookResponse
	if err := json.NewDecoder(resp.Body).Decode(&ob); err != nil {
		return nil, err
	}

	// Преобразуем строки в числа
	bids := make([]entity.Order, len(ob.Bids))
	for i, b := range ob.Bids {
		price, _ := strconv.ParseFloat(b[0], 64)
		qty, _ := strconv.ParseFloat(b[1], 64)
		bids[i] = entity.Order{
			Price:     price,
			Quantity:  qty,
			Side:      "BUY",
			UpdatedAt: 0,
		}
	}

	asks := make([]entity.Order, len(ob.Asks))
	for i, a := range ob.Asks {
		price, _ := strconv.ParseFloat(a[0], 64)
		qty, _ := strconv.ParseFloat(a[1], 64)
		asks[i] = entity.Order{
			Price:     price,
			Quantity:  qty,
			Side:      "SELL",
			UpdatedAt: 0,
		}
	}

	return &OrderBook{Bids: bids, Asks: asks}, nil
}

func (c *BinanceHTTPClient) ValidateAPIKey() error {
	if c.APIKey == "" {
		return fmt.Errorf("API key is empty")
	}
	return nil
}
