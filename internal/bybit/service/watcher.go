package service

import (
	"errors"
	"sizehunt/internal/bybit/entity"
)

// Watcher отвечает за мониторинг ордербука Bybit
type Watcher struct {
	client *BybitHTTPClient
}

// NewWatcher создает новый Watcher для Bybit
func NewWatcher(c *BybitHTTPClient) *Watcher {
	return &Watcher{client: c}
}

// DetectLargeOrder ищет крупную заявку в ордербуке
func (w *Watcher) DetectLargeOrder(symbol string, price float64, minQty float64, userLimit int, category string) (*entity.Order, error) {
	limit := userLimit
	if limit > 500 { // Bybit ограничение на глубину
		limit = 500
	}

	book, err := w.client.GetOrderBook(symbol, limit, category)
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

// GetFuturesCB возвращает circuit breaker для фьючерсов (для совместимости с handler)
func (w *Watcher) GetFuturesCB() interface{} {
	if w.client != nil {
		return w.client.GetCircuitBreaker()
	}
	return nil
}
