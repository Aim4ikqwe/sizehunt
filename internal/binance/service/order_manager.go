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
	return manager
}

// CloseFullPosition closes position using cached WS data for specific user only
func (om *OrderManager) CloseFullPosition(symbol string) error {
	startTime := time.Now()
	log.Printf("OrderManager: Starting CloseFullPosition for %s at %v", symbol, startTime)
	defer func() {
		log.Printf("OrderManager: CloseFullPosition for %s completed (total time: %v)", symbol, time.Since(startTime))
	}()

	// 1. Read cached position
	posAmt := om.Watcher.GetPosition(symbol)
	if posAmt == 0 {
		log.Printf("OrderManager: Position %s already zero, no action needed", symbol)
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

	log.Printf("OrderManager: Closing %s position %.6f with MARKET %s %s", symbol, posAmt, side, qtyStr)

	// 2. Send MARKET close order
	svc := om.FuturesClient.NewCreateOrderService()
	svc.Symbol(symbol).
		Side(side).
		Type(futures.OrderTypeMarket).
		Quantity(qtyStr).
		ReduceOnly(true).
		NewOrderResponseType(futures.NewOrderRespTypeRESULT) // Получаем подробный ответ

	orderStartTime := time.Now()
	log.Printf("OrderManager: Sending close order for %s at %v", symbol, orderStartTime)
	// FIX: Use correct response type - *futures.CreateOrderResponse instead of *futures.Order
	resp, err := svc.Do(context.Background())
	orderDuration := time.Since(orderStartTime)

	if err != nil {
		log.Printf("OrderManager: ERROR: Close order failed for %s: %v (took %v)", symbol, err, orderDuration)
		return fmt.Errorf("OrderManager close error: %w", err)
	}

	log.Printf("OrderManager: Close order executed successfully for %s (took %v)", symbol, orderDuration)
	// FIX: Access fields correctly from CreateOrderResponse
	log.Printf("OrderManager: Order details - ID: %d, Status: %s, Executed Qty: %s, Price: %s",
		resp.OrderID, resp.Status, resp.ExecutedQuantity, resp.Price)

	// Асинхронно освежаем позицию после отправки ордера (без блокировки основного потока)
	go func() {
		time.Sleep(50 * time.Millisecond) // небольшая пауза, чтобы WS успел принести апдейт
		log.Printf("OrderManager: Forcing position refresh from REST API for %s (post-close)", symbol)
		if err := om.refreshPositionFromREST(symbol); err != nil {
			log.Printf("OrderManager: WARNING: Failed to refresh position from REST: %v", err)
		} else {
			log.Printf("OrderManager: Position for %s refreshed from REST", symbol)
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
