package service

import "sizehunt/internal/binance/entity"

type OrderBook struct {
	Bids []entity.Order
	Asks []entity.Order
}

type BinanceClient interface {
	GetOrderBook(symbol string, limit int, market string) (*OrderBook, error)
	ValidateAPIKey() error
}
