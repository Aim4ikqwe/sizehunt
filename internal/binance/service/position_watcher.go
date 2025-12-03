// internal/binance/service/position_watcher.go
package service

import (
	"encoding/json"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/adshao/go-binance/v2/futures"
)

// PositionWatcher stores futures positions updated via WS
// and provides fast GetPosition() for OrderManager.
type PositionWatcher struct {
	mu         sync.RWMutex
	positions  map[string]float64   // symbol -> amount
	lastUpdate map[string]time.Time // symbol -> last update time
}

func NewPositionWatcher() *PositionWatcher {
	watcher := &PositionWatcher{
		positions:  make(map[string]float64),
		lastUpdate: make(map[string]time.Time),
	}

	log.Println("PositionWatcher: Created new instance")
	return watcher
}

// GetPosition returns last cached amount for the symbol
func (w *PositionWatcher) GetPosition(symbol string) float64 {
	startTime := time.Now()
	defer func() {
		log.Printf("PositionWatcher: GetPosition for %s took %v", symbol, time.Since(startTime))
	}()

	w.mu.RLock()
	defer w.mu.RUnlock()

	amount := w.positions[symbol]
	lastUpdate := w.lastUpdate[symbol]

	log.Printf("PositionWatcher: GetPosition - %s = %.6f (last updated: %v ago)",
		symbol, amount, time.Since(lastUpdate))

	return amount
}

// GetPositionWithTimestamp returns position amount and last update timestamp
func (w *PositionWatcher) GetPositionWithTimestamp(symbol string) (float64, time.Time) {
	w.mu.RLock()
	defer w.mu.RUnlock()

	return w.positions[symbol], w.lastUpdate[symbol]
}

// setPosition updates cache
func (w *PositionWatcher) setPosition(symbol string, amt float64) {
	startTime := time.Now()
	defer func() {
		log.Printf("PositionWatcher: setPosition for %s took %v", symbol, time.Since(startTime))
	}()

	w.mu.Lock()
	defer w.mu.Unlock()

	w.positions[symbol] = amt
	w.lastUpdate[symbol] = time.Now()

}

// HandleWsEvent parses WsUserDataEvent and extracts futures account updates
func (w *PositionWatcher) HandleWsEvent(e *futures.WsUserDataEvent) {
	startTime := time.Now()
	defer func() {
		log.Printf("PositionWatcher: HandleWsEvent took %v", time.Since(startTime))
	}()

	if e == nil {
		log.Println("PositionWatcher: WARNING: Received nil event")
		return
	}

	log.Printf("PositionWatcher: Processing WS event (type: %s, time: %d)", e.Event, e.Time)

	// convert event to generic JSON map
	var raw map[string]json.RawMessage
	b, err := json.Marshal(e)
	if err != nil {
		log.Printf("PositionWatcher: ERROR: JSON marshal error: %v", err)
		return
	}

	if err := json.Unmarshal(b, &raw); err != nil {
		log.Printf("PositionWatcher: ERROR: JSON unmarshal error: %v", err)
		return
	}

	// futures account updates are under key "a"
	accountRaw, ok := raw["a"]
	if !ok {
		log.Println("PositionWatcher: WARNING: No account data in event")
		return
	}

	// parse structure containing positions P[]
	var account struct {
		P []struct {
			Symbol           string `json:"s"`
			PositionAmt      string `json:"pa"`
			EntryPrice       string `json:"ep"`
			MarkPrice        string `json:"mp"`
			UnrealizedProfit string `json:"up"`
		} `json:"P"`
	}

	if err := json.Unmarshal(accountRaw, &account); err != nil {
		log.Printf("PositionWatcher: ERROR: Failed to decode account update: %v", err)
		return
	}

	log.Printf("PositionWatcher: Found %d positions in update", len(account.P))

	// update each position
	for _, p := range account.P {
		amt, err := strconv.ParseFloat(p.PositionAmt, 64)
		if err != nil {
			log.Printf("PositionWatcher: ERROR: Failed to parse position amount for %s (raw: %s): %v",
				p.Symbol, p.PositionAmt, err)
			continue
		}

		entryPrice, _ := strconv.ParseFloat(p.EntryPrice, 64)
		markPrice, _ := strconv.ParseFloat(p.MarkPrice, 64)
		unrealizedProfit, _ := strconv.ParseFloat(p.UnrealizedProfit, 64)

		oldAmt, _ := w.GetPositionWithTimestamp(p.Symbol)
		change := amt - oldAmt

		log.Printf("PositionWatcher: Processing position update for %s:", p.Symbol)
		log.Printf("  Current amount: %.6f", amt)
		log.Printf("  Previous amount: %.6f", oldAmt)
		log.Printf("  Change: %.6f", change)
		log.Printf("  Entry price: %.8f", entryPrice)
		log.Printf("  Mark price: %.8f", markPrice)
		log.Printf("  Unrealized PnL: %.8f", unrealizedProfit)

		w.setPosition(p.Symbol, amt)
	}

	log.Printf("PositionWatcher: Event processing completed successfully")
}
