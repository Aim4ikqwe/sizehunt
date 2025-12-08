package service

import (
	"errors"
	"sizehunt/internal/okx/entity"
)

// Watcher отвечает за мониторинг ордербука OKX
type Watcher struct {
	client *OKXHTTPClient
}

// NewWatcher создает новый Watcher для OKX
func NewWatcher(c *OKXHTTPClient) *Watcher {
	return &Watcher{client: c}
}

// DetectLargeOrder ищет крупную заявку в ордербуке
func (w *Watcher) DetectLargeOrder(instID string, price float64, minQty float64, userLimit int, instType string) (*entity.Order, error) {
	limit := userLimit
	if limit > 400 { // OKX ограничение на глубину
		limit = 400
	}

	book, err := w.client.GetOrderBook(instID, limit, instType)
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
