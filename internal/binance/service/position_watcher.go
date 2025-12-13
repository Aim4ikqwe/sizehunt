// internal/binance/service/position_watcher.go
package service

import (
	"context"
	"encoding/json"
	"log"
	"strconv"
	"sync"
	"time"

	"sizehunt/internal/binance/repository"

	"github.com/adshao/go-binance/v2/futures"
)

// PositionWatcher stores futures positions updated via WS
// and provides fast GetPosition() for OrderManager.
type PositionWatcher struct {
	mu           sync.RWMutex
	positions    map[string]float64   // symbol -> amount
	lastUpdate   map[string]time.Time // symbol -> last update time
	signalRepo   repository.SignalRepository
	websocketMgr *WebSocketManager // для вызова GracefulStopProxyForUser
	userID       int64             // ID пользователя для связи с WebSocketManager
}

func NewPositionWatcher(signalRepo repository.SignalRepository) *PositionWatcher {
	watcher := &PositionWatcher{
		positions:  make(map[string]float64),
		lastUpdate: make(map[string]time.Time),
		signalRepo: signalRepo,
	}

	log.Println("PositionWatcher: Created new instance")
	return watcher
}

// SetWebSocketManager устанавливает ссылку на WebSocketManager
func (w *PositionWatcher) SetWebSocketManager(wsm *WebSocketManager) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.websocketMgr = wsm
}

// SetUserID устанавливает ID пользователя
func (w *PositionWatcher) SetUserID(userID int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.userID = userID
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

	if amt == 0 {
		go w.checkAndRemoveSignalsIfZeroPosition(symbol)
	}
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
		log.Printf(" Entry price: %.8f", entryPrice)
		log.Printf("  Mark price: %.8f", markPrice)
		log.Printf("  Unrealized PnL: %.8f", unrealizedProfit)

		w.setPosition(p.Symbol, amt)
	}

	log.Printf("PositionWatcher: Event processing completed successfully")
}

// checkAndRemoveSignalsIfZeroPosition проверяет и удаляет сигналы, если позиция равна 0
func (w *PositionWatcher) checkAndRemoveSignalsIfZeroPosition(symbol string) {
	if w.signalRepo == nil {
		log.Printf("PositionWatcher: Signal repository is nil, skipping signal removal for %s", symbol)
		return
	}

	ctx := context.Background()

	// Получаем все активные сигналы из базы данных
	allSignals, err := w.signalRepo.GetAllActiveSignals(ctx)
	if err != nil {
		log.Printf("PositionWatcher: ERROR: Failed to get all active signals: %v", err)
		return
	}

	log.Printf("PositionWatcher: Found %d active signals in total, checking for symbol %s", len(allSignals), symbol)

	// Фильтруем сигналы по символу
	var signalsForSymbol []*repository.SignalDB
	var userIDs []int64 // для отслеживания ID пользователей, у которых были сигналы
	for _, signal := range allSignals {
		if signal.Symbol == symbol {
			signalsForSymbol = append(signalsForSymbol, signal)
			// Сохраняем ID пользователя, чтобы вызвать остановку прокси позже
			userIDs = append(userIDs, signal.UserID)
		}
	}

	log.Printf("PositionWatcher: Found %d active signals for symbol %s, checking for removal", len(signalsForSymbol), symbol)

	for _, signal := range signalsForSymbol {
		// Проверяем, что позиция действительно равна 0
		if w.GetPosition(symbol) == 0 {
			// Деактивируем сигнал в базе данных
			if err := w.signalRepo.Deactivate(ctx, signal.ID); err != nil {
				log.Printf("PositionWatcher: ERROR: Failed to deactivate signal %d: %v", signal.ID, err)
				continue
			}

			log.Printf("PositionWatcher: Signal %d for symbol %s has been deactivated because position is 0", signal.ID, symbol)

			// Удаляем сигнал из памяти MarketDepthWatcher
			if w.websocketMgr != nil {
				log.Printf("PositionWatcher: Removing signal %d from memory for user %d", signal.ID, signal.UserID)
				w.websocketMgr.RemoveSignalFromMemory(signal.UserID, signal.ID)
			}
		}
	}

	// После деактивации сигналов, проверяем, нужно ли остановить прокси для пользователей
	for _, userID := range userIDs {
		if w.websocketMgr != nil {
			log.Printf("PositionWatcher: Calling GracefulStopProxyForUser for user %d", userID)
			w.websocketMgr.GracefulStopProxyForUser(userID)
		}
	}
}
