package service

import (
	"context"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/adshao/go-binance/v2/futures"
)

// OrderManager now closes positions using PositionWatcher (no REST GetPositionRisk).
type OrderManager struct {
	FuturesClient *futures.Client
	Watcher       *PositionWatcher
}

func NewOrderManager(futuresClient *futures.Client, watcher *PositionWatcher) *OrderManager {
	manager := &OrderManager{
		FuturesClient: futuresClient,
		Watcher:       watcher,
	}

	log.Println("OrderManager: Created new instance")
	return manager
}

// CloseFullPosition closes position using cached WS data.
func (om *OrderManager) CloseFullPosition(symbol string) error {
	startTime := time.Now()
	defer func() {
		log.Printf("OrderManager: CloseFullPosition for %s completed (total time: %v)", symbol, time.Since(startTime))
	}()

	log.Printf("OrderManager: CloseFullPosition called for %s", symbol)

	// 1. Read cached position
	posAmt := om.Watcher.GetPosition(symbol)
	if posAmt == 0 {
		log.Printf("OrderManager: Position %s already zero, no action needed", symbol)
		return nil
	}

	log.Printf("OrderManager: Current position for %s: %.6f", symbol, posAmt)

	absQty := math.Abs(posAmt)
	qtyStr := formatQuantity(absQty)

	log.Printf("OrderManager: Calculated close quantity: %s (absolute value of %.6f)", qtyStr, posAmt)

	var side futures.SideType
	if posAmt > 0 {
		side = futures.SideTypeSell
		log.Printf("OrderManager: Position is LONG (%.6f), will close with SELL order", posAmt)
	} else {
		side = futures.SideTypeBuy
		log.Printf("OrderManager: Position is SHORT (%.6f), will close with BUY order", posAmt)
	}

	log.Printf("OrderManager: Executing MARKET close order for %s: %s %s", symbol, side, qtyStr)

	// 2. Send MARKET close order
	svc := om.FuturesClient.NewCreateOrderService()
	svc.Symbol(symbol).
		Side(side).
		Type(futures.OrderTypeMarket).
		Quantity(qtyStr).
		NewOrderResponseType(futures.NewOrderRespTypeRESULT) // Получаем подробный ответ

	orderStartTime := time.Now()
	resp, err := svc.Do(context.Background())
	orderDuration := time.Since(orderStartTime)

	if err != nil {
		log.Printf("OrderManager: ERROR: Close order failed for %s: %v (took %v)", symbol, err, orderDuration)
		return fmt.Errorf("OrderManager close error: %w", err)
	}

	log.Printf("OrderManager: Close order executed successfully for %s (took %v)", symbol, orderDuration)
	log.Printf("OrderManager: Order details - ID: %d, Status: %s, Executed Qty: %s, Price: %s",
		resp.OrderID, resp.Status, resp.ExecutedQuantity, resp.Price)

	// Ждем обновления позиции через WebSocket
	_ = time.Now()
	maxWaitTime := 5 * time.Second
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	log.Printf("OrderManager: Waiting for position update (max wait: %v)", maxWaitTime)

	for {
		select {
		case <-ticker.C:
			newPosAmt := om.Watcher.GetPosition(symbol)
			log.Printf("OrderManager: Position update check - %s: %.6f (target: 0)", symbol, newPosAmt)

			if math.Abs(newPosAmt) < 0.000001 { // небольшой порог для учета точности
				log.Printf("OrderManager: Position successfully closed to zero: %.6f", newPosAmt)
				return nil
			}

		case <-time.After(maxWaitTime):
			log.Printf("OrderManager: WARNING: Position not updated to zero after %v. Final position: %.6f",
				maxWaitTime, om.Watcher.GetPosition(symbol))

			// Проверяем позицию через REST как fallback
			log.Println("OrderManager: Checking position via REST API as fallback")
			resp, err := om.FuturesClient.NewGetPositionRiskService().Symbol(symbol).Do(context.Background())
			if err != nil {
				log.Printf("OrderManager: ERROR: REST position check failed: %v", err)
			} else if len(resp) > 0 {
				restPosAmt, _ := strconv.ParseFloat(resp[0].PositionAmt, 64)
				log.Printf("OrderManager: REST position check - %s: %.6f", symbol, restPosAmt)

				if math.Abs(restPosAmt) < 0.000001 {
					log.Printf("OrderManager: Position confirmed closed via REST: %.6f", restPosAmt)
					return nil
				}
			}

			if err != nil || len(resp) == 0 {
				return fmt.Errorf("position not confirmed closed after %v, no REST data available", maxWaitTime)
			}

			return fmt.Errorf("position not closed after %v, still at %.6f", maxWaitTime, om.Watcher.GetPosition(symbol))
		}
	}
}

func formatQuantity(q float64) string {
	// Форматируем с 6 знаками после запятой, но убираем конечные нули
	formatted := fmt.Sprintf("%.6f", q)
	// Убираем конечные нули и возможную точку
	formatted = strings.TrimRight(formatted, "0")
	formatted = strings.TrimRight(formatted, ".")

	if formatted == "" {
		return "0"
	}

	log.Printf("OrderManager: Formatted quantity %.6f -> %s", q, formatted)
	return formatted
}
