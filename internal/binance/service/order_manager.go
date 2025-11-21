// internal/binance/service/order_manager.go
package service

import (
	"context"
	"fmt"
	"log"
	"math"
	"strconv"
	"time"

	"github.com/adshao/go-binance/v2"
	"github.com/adshao/go-binance/v2/futures"
)

type OrderManager struct {
	SpotClient    *binance.Client
	FuturesClient *futures.Client
}

func NewOrderManager(apiKey, secretKey string) *OrderManager {
	// Инициализируем оба клиента
	spotClient := binance.NewClient(apiKey, secretKey)
	futuresClient := binance.NewFuturesClient(apiKey, secretKey)
	return &OrderManager{
		SpotClient:    spotClient,
		FuturesClient: futuresClient,
	}
}

// Старый метод ClosePosition (можешь оставить, если используется где-то ещё, или удалить)
func (om *OrderManager) ClosePosition(symbol, side, quantity string, market string) error {
	log.Printf("OrderManager: ClosePosition called for symbol %s, side %s, quantity %s, market %s", symbol, side, quantity, market)
	switch market {
	case "futures":
		return om.closeFuturesPosition(symbol, side, quantity)
	case "spot":
		return om.closeSpotPosition(symbol, side, quantity)
	default:
		return fmt.Errorf("unsupported market for closing: %s", market)
	}
}

// НОВЫЙ метод CloseFullPosition
func (om *OrderManager) CloseFullPosition(symbol, side, market string) error {
	log.Printf("OrderManager: CloseFullPosition called for symbol %s, side %s, market %s", symbol, side, market)
	switch market {
	case "futures":
		return om.closeFullFuturesPosition(symbol)
	case "spot":
		// Для спота логика другая (продажа всего баланса), оставим как есть или заглушку
		return fmt.Errorf("CloseFullPosition not supported for spot yet")
	default:
		return fmt.Errorf("unsupported market for closing: %s", market)
	}
}

func (om *OrderManager) closeFuturesPosition(symbol, side, quantity string) error {
	// Используем библиотеку для фьючерсов
	service := om.FuturesClient.NewCreateOrderService()
	service.Symbol(symbol)
	service.Side(futures.SideType(side)) // Преобразуем строку в тип
	service.Type(futures.OrderTypeMarket)
	service.Quantity(quantity)
	// service.Timestamp(time.Now().UnixMilli()) // <--- УДАЛЕНО (если было)
	// Подпись и выполнение
	_, err := service.Do(context.Background())
	if err != nil {
		log.Printf("OrderManager: Binance Futures API error: %v", err)
		return fmt.Errorf("Binance Futures API error: %w", err)
	}
	log.Printf("OrderManager: Futures position closed successfully for symbol %s", symbol)
	return nil
}

// ИСПРАВЛЕННЫЙ метод для закрытия всей фьючерсной позиции через CreateOrderService
// Теперь указываем и Type, и ClosePosition, но НЕ указываем Quantity.
func (om *OrderManager) closeFullFuturesPosition(symbol string) error {
	// Шаг 1: Получить текущую позицию
	log.Printf("OrderManager: closeFullFuturesPosition: Fetching current position for %s", symbol)
	startGetPositionTime := time.Now()
	service := om.FuturesClient.NewGetPositionRiskService() // Используем существующий клиент
	service.Symbol(symbol)

	positions, err := service.Do(context.Background())
	getPositionDuration := time.Since(startGetPositionTime)
	log.Printf("OrderManager: GetPositionRisk API call for %s took %v", symbol, getPositionDuration)
	if err != nil {
		log.Printf("OrderManager: closeFullFuturesPosition: GetPositionRisk API error for %s: %v", symbol, err)
		return fmt.Errorf("Binance Futures API error on GetPositionRisk: %w", err)
	}

	if len(positions) == 0 {
		log.Printf("OrderManager: closeFullFuturesPosition: No position data returned for %s", symbol)
		return fmt.Errorf("no position data found for symbol %s", symbol)
	}

	pos := positions[0]
	// Шаг 1.1: Проверить, открыта ли позиция
	posAmtStr := pos.PositionAmt
	posAmt, err := strconv.ParseFloat(posAmtStr, 64) // Обязательно проверяем ошибку
	if err != nil {
		log.Printf("OrderManager: closeFullFuturesPosition: Error parsing PositionAmt '%s' for %s: %v", posAmtStr, symbol, err)
		return fmt.Errorf("error parsing position amount: %w", err)
	}

	if posAmt == 0 {
		log.Printf("OrderManager: closeFullFuturesPosition: Position for %s is already closed (amount: 0)", symbol)
		return nil // Позиции нет, считаем, что закрыта
	}

	log.Printf("OrderManager: closeFullFuturesPosition: Found open position for %s, amount: %.6f", symbol, posAmt)

	// Шаг 2: Определить сторону закрытия
	var closeSide futures.SideType
	if posAmt > 0 {
		closeSide = futures.SideTypeSell // Если позиция long, закрываем продажей
	} else {
		closeSide = futures.SideTypeBuy // Если позиция short, закрываем покупкой
	}
	// Используем абсолютное значение для количества
	quantity := fmt.Sprintf("%.6f", math.Abs(posAmt)) // Используем 6 знаков после запятой как буфер, можно уточнить по stepSize

	// Шаг 3: Выставить ордер на закрытие
	log.Printf("OrderManager: closeFullFuturesPosition: Placing %s market order to close position for %s, quantity %s", closeSide, symbol, quantity)
	startCloseTime := time.Now()
	orderService := om.FuturesClient.NewCreateOrderService() // Используем существующий клиент
	orderService.Symbol(symbol)
	orderService.Side(closeSide) // futures.SideType
	orderService.Type(futures.OrderTypeMarket)
	orderService.Quantity(quantity)

	_, err = orderService.Do(context.Background())
	getPositionCloseDuration := time.Since(startCloseTime)
	log.Printf("OrderManager: GetPositionRisk API close for %s took %v", symbol, getPositionCloseDuration)
	if err != nil {
		log.Printf("OrderManager: Binance Futures API error on closeFullFuturesPosition for %s: %v", symbol, err)
		return fmt.Errorf("Binance Futures API error on closeFullFuturesPosition: %w", err)
	}

	log.Printf("OrderManager: FULL Futures position for %s closed successfully", symbol)
	return nil
}

func (om *OrderManager) closeSpotPosition(symbol, side, quantity string) error {
	// Используем библиотеку для спота
	service := om.SpotClient.NewCreateOrderService()
	service.Symbol(symbol)
	service.Side(binance.SideType(side)) // Преобразуем строку в тип
	service.Type(binance.OrderTypeMarket)
	service.Quantity(quantity)
	// service.Timestamp(time.Now().UnixMilli()) // <--- УДАЛЕНО (если было)
	// Подпись и выполнение
	_, err := service.Do(context.Background())
	if err != nil {
		log.Printf("OrderManager: Binance Spot API error: %v", err)
		return fmt.Errorf("Binance Spot API error: %w", err)
	}
	log.Printf("OrderManager: Spot position closed successfully for symbol %s", symbol)
	return nil
}
