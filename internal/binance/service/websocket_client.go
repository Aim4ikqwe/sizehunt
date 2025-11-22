// internal/binance/service/websocket_client.go
package service

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/adshao/go-binance/v2"
	"github.com/adshao/go-binance/v2/futures"
)

// UnifiedDepthStreamData объединяет данные из разных рынков
type UnifiedDepthStreamData struct {
	Stream string
	Data   *UnifiedDepthEvent
}

// UnifiedDepthEvent объединяет структуры данных для спота и фьючерсов
type UnifiedDepthEvent struct {
	Symbol        string
	EventTime     int64
	FirstUpdateID int64
	LastUpdateID  int64
	Bids          [][]string // [][price, quantity]
	Asks          [][]string // [][price, quantity]
}

type WebSocketClient struct {
	OnData   func(data *UnifiedDepthStreamData)
	stopC    chan struct{}
	doneC    <-chan struct{}
	mu       sync.Mutex // Для защиты доступа к stopC и doneC
	isClosed bool       // Флаг, что соединение уже закрыто
}

func NewWebSocketClient() *WebSocketClient {
	return &WebSocketClient{
		isClosed: false,
	}
}

// ConnectForSpotCombined использует binance.WsCombinedDepthServe для нескольких спотовых символов
func (w *WebSocketClient) ConnectForSpotCombined(ctx context.Context, symbols []string) error {
	doneC, stopC, err := binance.WsCombinedDepthServe(symbols, func(event *binance.WsDepthEvent) {
		// Конвертируем spot событие в унифицированное
		// Преобразуем []binance.Bid/Ask в [][]string
		bids := make([][]string, len(event.Bids))
		for i, bid := range event.Bids {
			bids[i] = []string{bid.Price, bid.Quantity}
		}
		asks := make([][]string, len(event.Asks))
		for i, ask := range event.Asks {
			asks[i] = []string{ask.Price, ask.Quantity}
		}
		unifiedEvent := &UnifiedDepthEvent{
			Symbol:        event.Symbol,
			EventTime:     event.Time,
			FirstUpdateID: event.FirstUpdateID,
			LastUpdateID:  event.LastUpdateID,
			Bids:          bids,
			Asks:          asks,
		}
		streamData := &UnifiedDepthStreamData{
			Stream: fmt.Sprintf("%s@depth", event.Symbol),
			Data:   unifiedEvent,
		}
		if w.OnData != nil {
			w.OnData(streamData)
		}
	}, func(err error) {
		log.Printf("WebSocket error for spot combined stream: %v", err)
	})

	if err != nil {
		return fmt.Errorf("failed to connect to spot combined WebSocket: %w", err)
	}

	w.doneC = doneC
	w.stopC = stopC

	// Горутина для остановки по контексту
	go func() {
		<-ctx.Done()
		log.Printf("Context cancelled, stopping combined WebSocket for spot")
		if w.stopC != nil {
			close(w.stopC)
		}
	}()

	return nil
}

// ConnectForFuturesCombined использует futures.WsCombinedDepthServe для нескольких фьючерсных символов
func (w *WebSocketClient) ConnectForFuturesCombined(ctx context.Context, symbolLevels map[string]string) error {
	doneC, stopC, err := futures.WsCombinedDepthServe(symbolLevels, func(event *futures.WsDepthEvent) {
		// Конвертируем futures событие в унифицированное
		// Преобразуем []futures.Bid/Ask в [][]string
		bids := make([][]string, len(event.Bids))
		for i, bid := range event.Bids {
			bids[i] = []string{bid.Price, bid.Quantity}
		}
		asks := make([][]string, len(event.Asks))
		for i, ask := range event.Asks {
			asks[i] = []string{ask.Price, ask.Quantity}
		}
		unifiedEvent := &UnifiedDepthEvent{
			Symbol:        event.Symbol,
			EventTime:     event.Time,
			FirstUpdateID: event.FirstUpdateID,
			LastUpdateID:  event.LastUpdateID,
			Bids:          bids,
			Asks:          asks,
		}
		streamData := &UnifiedDepthStreamData{
			Stream: fmt.Sprintf("%s@depth", event.Symbol),
			Data:   unifiedEvent,
		}
		if w.OnData != nil {
			w.OnData(streamData)
		}
	}, func(err error) {
		log.Printf("WebSocket error for futures combined stream: %v", err)
	})

	if err != nil {
		return fmt.Errorf("failed to connect to futures combined WebSocket: %w", err)
	}

	w.doneC = doneC
	w.stopC = stopC

	// Горутина для остановки по контексту
	go func() {
		<-ctx.Done()
		log.Printf("Context cancelled, stopping combined WebSocket for futures")
		if w.stopC != nil {
			close(w.stopC)
		}
	}()

	return nil
}

// Close закрывает соединение
func (w *WebSocketClient) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Если клиент уже закрыт - ничего не делаем
	if w.isClosed {
		log.Println("WebSocketClient: Attempt to close already closed connection")
		return
	}

	// Закрываем stopC только если он еще открыт
	if w.stopC != nil {
		select {
		case <-w.stopC:
			// Канал уже закрыт
			log.Println("WebSocketClient: stopC channel already closed")
		default:
			close(w.stopC)
			log.Println("WebSocketClient: stopC channel closed successfully")
		}
		w.stopC = nil
	}

	// Ждем завершения соединения с таймаутом
	if w.doneC != nil {
		select {
		case <-w.doneC:
			log.Println("Combined WebSocket connection closed cleanly.")
		case <-time.After(3 * time.Second):
			log.Println("WebSocketClient: Timeout waiting for connection to close")
		}
		w.doneC = nil
	}

	w.isClosed = true
	log.Println("WebSocketClient: Connection successfully closed and marked as closed")
}
