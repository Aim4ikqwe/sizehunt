package service

import "sizehunt/internal/binance/entity"

type BinanceClient interface {
	GetOrderBook(symbol string, limit int) (*OrderBook, error)
}

type OrderBook struct {
	Bids []entity.Order
	Asks []entity.Order
}
