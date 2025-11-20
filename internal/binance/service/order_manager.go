// internal/binance/service/order_manager.go
package service

import (
	"context"
	"fmt"
	"github.com/adshao/go-binance/v2"
	"github.com/adshao/go-binance/v2/futures"
	"log"
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

func (om *OrderManager) closeFuturesPosition(symbol, side, quantity string) error {
	// Используем библиотеку для фьючерсов
	service := om.FuturesClient.NewCreateOrderService()
	service.Symbol(symbol)
	service.Side(futures.SideType(side)) // Преобразуем строку в тип
	service.Type(futures.OrderTypeMarket)
	service.Quantity(quantity)
	// service.Timestamp(time.Now().UnixMilli()) // <--- УДАЛЕНО
	// Подпись и выполнение
	_, err := service.Do(context.Background())
	if err != nil {
		log.Printf("OrderManager: Binance Futures API error: %v", err)
		return fmt.Errorf("Binance Futures API error: %w", err)
	}
	log.Printf("OrderManager: Futures position closed successfully for symbol %s", symbol)
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
