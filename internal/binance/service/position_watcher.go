package service

import (
	"encoding/json"
	"log"
	"strconv"
	"sync"

	"github.com/adshao/go-binance/v2/futures"
)

// PositionWatcher stores futures positions updated via WS
// and provides fast GetPosition() for OrderManager.
type PositionWatcher struct {
	mu        sync.RWMutex
	positions map[string]float64 // symbol -> amount
}

func NewPositionWatcher() *PositionWatcher {
	return &PositionWatcher{
		positions: make(map[string]float64),
	}
}

// GetPosition returns last cached amount for the symbol
func (w *PositionWatcher) GetPosition(symbol string) float64 {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.positions[symbol]
}

// setPosition updates cache
func (w *PositionWatcher) setPosition(symbol string, amt float64) {
	w.mu.Lock()
	w.positions[symbol] = amt
	w.mu.Unlock()
}

// HandleWsEvent parses WsUserDataEvent and extracts futures account updates
func (w *PositionWatcher) HandleWsEvent(e *futures.WsUserDataEvent) {
	if e == nil {
		return
	}

	// convert event to generic JSON map
	var raw map[string]json.RawMessage
	b, err := json.Marshal(e)
	if err != nil {
		log.Printf("PositionWatcher: JSON marshal error: %v", err)
		return
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		log.Printf("PositionWatcher: JSON unmarshal error: %v", err)
		return
	}

	// futures account updates are under key "a"
	accountRaw, ok := raw["a"]
	if !ok {
		return
	}

	// parse structure containing positions P[]
	var account struct {
		P []struct {
			Symbol      string `json:"s"`
			PositionAmt string `json:"pa"`
		} `json:"P"`
	}

	if err := json.Unmarshal(accountRaw, &account); err != nil {
		log.Printf("PositionWatcher: failed to decode account update: %v", err)
		return
	}

	// update each position
	for _, p := range account.P {
		amt, err := strconv.ParseFloat(p.PositionAmt, 64)
		if err != nil {
			log.Printf("PositionWatcher: failed to parse amt %s: %v", p.Symbol, err)
			continue
		}
		w.setPosition(p.Symbol, amt)
		log.Printf("PositionWatcher: %s updated = %.6f", p.Symbol, amt)
	}
}
