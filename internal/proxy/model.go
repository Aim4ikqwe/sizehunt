package proxy

import (
	"time"
)

type ProxyConfig struct {
	ID         int64
	UserID     int64
	SSAddr     string
	SSPort     int // ДОБАВЬТЕ ЭТО ПОЛЕ - порт сервера
	SSMethod   string
	SSPassword string
	LocalPort  int    // локальный порт для прокси
	Status     string // "running", "stopped", "error"
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type ProxyInstance struct {
	Config      *ProxyConfig
	ContainerID string
	Status      string
}
