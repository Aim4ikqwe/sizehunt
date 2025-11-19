package service

import (
	"errors"
	"sizehunt/internal/binance/entity"
)

type Watcher struct {
	client BinanceClient
}

func NewWatcher(c BinanceClient) *Watcher {
	return &Watcher{client: c}
}

func (w *Watcher) DetectLargeOrder(symbol string, price float64, minQty float64, userLimit int, market string) (*entity.Order, error) {
	limit := userLimit
	if limit > 3000 {
		limit = 3000
	}

	book, err := w.client.GetOrderBook(symbol, limit, market)
	if err != nil {
		return nil, err
	}

	for _, o := range book.Bids {
		if o.Price == price && o.Quantity >= minQty {
			return &o, nil
		}
	}

	for _, o := range book.Asks {
		if o.Price == price && o.Quantity >= minQty {
			return &o, nil
		}
	}

	return nil, errors.New("order not found")
}
