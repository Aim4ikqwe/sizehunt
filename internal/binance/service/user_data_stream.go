package service

import (
	"context"
	"log"
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

func (u *UserDataStream) runWs() error {
	handler := func(event *futures.WsUserDataEvent) {
		if u.watcher != nil {
			u.watcher.HandleWsEvent(event)
		}
	}

	errHandler := func(err error) {
		log.Printf("UserDataStream WS error handler: %v", err)
	}

	doneC, stopC, err := futures.WsUserDataServe(u.listenKey, handler, errHandler)
	if err != nil {
		return err
	}

	// Если нужно ждать закрытия WS:
	go func() {
		<-doneC
		log.Println("UserDataStream WS closed")
	}()

	// Сохраняем канал stopC, если потом захотим останавливать WS вручную
	_ = stopC

	return nil
}

func (u *UserDataStream) keepAliveLoop() {
	ticker := time.NewTicker(25 * time.Minute)
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
