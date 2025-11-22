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

	// 🔥 КРИТИЧЕСКИ ВАЖНО: Не ждем синхронного обновления через WebSocket
	// Запускаем асинхронное обновление и возвращаем управление
	go func() {
		time.Sleep(500 * time.Millisecond) // Небольшая задержка для отправки ордера
		log.Printf("OrderManager: Forcing position refresh from REST API for %s", symbol)
		if err := om.refreshPositionFromREST(symbol); err != nil {
			log.Printf("OrderManager: WARNING: Failed to refresh position from REST: %v", err)
		} else {
			log.Printf("OrderManager: Position for %s successfully refreshed from REST", symbol)
		}
	}()

	log.Printf("OrderManager: Position close order sent, async refresh scheduled")
	return nil // 🔥 Возвращаем управление сразу после отправки ордера
}

// Новый метод для принудительного обновления позиции через REST API
func (om *OrderManager) refreshPositionFromREST(symbol string) error {
	resp, err := om.FuturesClient.NewGetPositionRiskService().Symbol(symbol).Do(context.Background())
	if err != nil {

		return fmt.Errorf("REST position refresh failed: %w", err)
	}

	if len(resp) == 0 {
		return fmt.Errorf("no position data returned from REST API")
	}

	newPosAmt, _ := strconv.ParseFloat(resp[0].PositionAmt, 64)
	om.Watcher.setPosition(symbol, newPosAmt)
	log.Printf("OrderManager: Position for %s successfully refreshed from REST: %.6f", symbol, newPosAmt)
	return nil
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

	return formatted
}
