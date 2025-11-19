package service

import (
	"context"
	"encoding/json"
	"log"

	"github.com/gorilla/websocket"
)

type WebSocketClient struct {
	conn   *websocket.Conn
	URL    string
	OnData func(data *DepthStreamData)
}

type DepthStreamData struct {
	Stream string      `json:"stream"`
	Data   DepthUpdate `json:"data"`
}

type DepthUpdate struct {
	EventType     string     `json:"e"`
	EventTime     int64      `json:"E"`
	Symbol        string     `json:"s"`
	FirstUpdateID int64      `json:"U"`
	LastUpdateID  int64      `json:"u"`
	Bids          [][]string `json:"b"`
	Asks          [][]string `json:"a"`
}

func NewWebSocketClient(url string) *WebSocketClient {
	return &WebSocketClient{
		URL: url,
	}
}

func (w *WebSocketClient) Connect(ctx context.Context) error {
	conn, _, err := websocket.DefaultDialer.Dial(w.URL, nil)
	if err != nil {
		return err
	}
	w.conn = conn

	go w.readLoop(ctx)
	return nil
}

func (w *WebSocketClient) readLoop(ctx context.Context) {
	defer w.conn.Close()

	for {
		select {
		case <-ctx.Done():
			return
		default:
			_, message, err := w.conn.ReadMessage()
			if err != nil {
				log.Println("WebSocket read error:", err)
				return
			}

			var data DepthStreamData
			if err := json.Unmarshal(message, &data); err != nil {
				log.Println("Failed to unmarshal depth data:", err)
				continue
			}

			if w.OnData != nil {
				w.OnData(&data)
			}
		}
	}
}

func (w *WebSocketClient) Close() error {
	if w.conn != nil {
		return w.conn.Close()
	}
	return nil
}
