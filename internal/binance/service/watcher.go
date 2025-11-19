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

// DetectLargeOrder ищет заявку >= minQty по цене price
// userLimit — лимит заявок, введённый пользователем, максимум 3000
func (w *Watcher) DetectLargeOrder(symbol string, price float64, minQty float64, userLimit int) (*entity.Order, error) {
	limit := userLimit
	if limit > 3000 {
		limit = 3000
	}

	book, err := w.client.GetOrderBook(symbol, limit)
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
