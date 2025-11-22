// internal/binance/service/user_data_stream.go
package service

import (
	"context"
	"log"
	"strconv"
	"time"

	"github.com/adshao/go-binance/v2/futures"
)

type UserDataStream struct {
	futuresClient *futures.Client
	watcher       *PositionWatcher
	listenKey     string
	stopChan      chan struct{}
}

func NewUserDataStream(client *futures.Client, watcher *PositionWatcher) *UserDataStream {
	return &UserDataStream{
		futuresClient: client,
		watcher:       watcher,
		stopChan:      make(chan struct{}),
	}
}

func (u *UserDataStream) Start() error {
	key, err := u.futuresClient.NewStartUserStreamService().Do(context.Background())
	if err != nil {
		return err
	}
	u.listenKey = key
	log.Printf("UserDataStream: listenKey created: %s", key)

	// 🔥 ИНИЦИАЛИЗАЦИЯ ПОЗИЦИЙ ЧЕРЕЗ REST — СРАЗУ ПОСЛЕ СОЗДАНИЯ ПОТОКА
	u.initializePositionsFromREST()

	go u.keepAliveLoop()
	go func() {
		for {
			err := u.runWs()
			if err != nil {
				log.Printf("UserDataStream: WS error: %v", err)
			}
			select {
			case <-u.stopChan:
				return
			default:
				time.Sleep(2 * time.Second)
			}
		}
	}()
	return nil
}

// initializePositionsFromREST загружает текущие позиции через REST и обновляет PositionWatcher
func (u *UserDataStream) initializePositionsFromREST() {
	if u.watcher == nil {
		log.Println("UserDataStream: PositionWatcher is nil, skipping REST init")
		return
	}
	resp, err := u.futuresClient.NewGetPositionRiskService().Do(context.Background())
	if err != nil {
		log.Printf("UserDataStream: failed to fetch positions from REST: %v", err)
		return
	}
	for _, pos := range resp {
		amt, err := strconv.ParseFloat(pos.PositionAmt, 64)
		if err != nil {
			log.Printf("UserDataStream: failed to parse position amount for %s: %v", pos.Symbol, err)
			continue
		}
		u.watcher.setPosition(pos.Symbol, amt)
		log.Printf("UserDataStream: Initialized %s = %.6f from REST", pos.Symbol, amt)
	}
}

func (u *UserDataStream) runWs() error {
	handler := func(event *futures.WsUserDataEvent) {
		if u.watcher != nil {
			u.watcher.HandleWsEvent(event)
		}
	}
	errHandler := func(err error) {
		log.Printf("UserDataStream WS error handler: %v", err)
	}
	doneC, _, err := futures.WsUserDataServe(u.listenKey, handler, errHandler)
	if err != nil {
		return err
	}
	go func() {
		<-doneC
		log.Println("UserDataStream WS closed")
	}()
	return nil
}

func (u *UserDataStream) keepAliveLoop() {
	// 🔥 Увеличь частоту keep-alive до 55 минут (Binance требует <60 мин)
	ticker := time.NewTicker(55 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if u.listenKey == "" {
				continue
			}
			err := u.futuresClient.
				NewKeepaliveUserStreamService().
				ListenKey(u.listenKey).
				Do(context.Background())
			if err != nil {
				log.Printf("UserDataStream: keepalive failed: %v", err)
			} else {
				log.Printf("UserDataStream: keepalive OK for key %s", u.listenKey)
			}
		case <-u.stopChan:
			return
		}
	}
}

func (u *UserDataStream) Stop() {
	close(u.stopChan)
}
