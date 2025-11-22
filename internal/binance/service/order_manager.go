package service

import (
	"context"
	"fmt"
	"log"
	"math"
	"time"

	"github.com/adshao/go-binance/v2/futures"
)

// OrderManager now closes positions using PositionWatcher (no REST GetPositionRisk).
type OrderManager struct {
	FuturesClient *futures.Client
	Watcher       *PositionWatcher
}

func NewOrderManager(futuresClient *futures.Client, watcher *PositionWatcher) *OrderManager {
	return &OrderManager{
		FuturesClient: futuresClient,
		Watcher:       watcher,
	}
}

// CloseFullPosition closes position using cached WS data.
func (om *OrderManager) CloseFullPosition(symbol string) error {
	log.Printf("OrderManager: CloseFullPosition called for %s", symbol)

	// 1. Read cached position
	posAmt := om.Watcher.GetPosition(symbol)
	if posAmt == 0 {
		log.Printf("OrderManager: Position %s already zero", symbol)
		return nil
	}

	absQty := math.Abs(posAmt)
	qtyStr := formatQuantity(absQty)

	var side futures.SideType
	if posAmt > 0 {
		side = futures.SideTypeSell
	} else {
		side = futures.SideTypeBuy
	}

	log.Printf("OrderManager: Closing %s position %f with %s %s", symbol, posAmt, side, qtyStr)

	// 2. Send MARKET close order
	svc := om.FuturesClient.NewCreateOrderService()
	svc.Symbol(symbol).
		Side(side).
		Type(futures.OrderTypeMarket).
		Quantity(qtyStr)

	start := time.Now()
	_, err := svc.Do(context.Background())
	log.Printf("OrderManager: Close order for %s took %v", symbol, time.Since(start))

	if err != nil {
		return fmt.Errorf("OrderManager close error: %w", err)
	}

	return nil
}

func formatQuantity(q float64) string {
	return fmt.Sprintf("%.6f", q)
}
