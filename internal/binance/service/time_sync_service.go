package service

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/adshao/go-binance/v2/futures"
)

// TimeSyncService handles time synchronization with the exchange
type TimeSyncService struct {
	client     *futures.Client
	timeDiff   time.Duration
	lastUpdate time.Time
	mu         sync.RWMutex
}

// NewTimeSyncService creates a new time synchronization service
func NewTimeSyncService(client *futures.Client) *TimeSyncService {
	service := &TimeSyncService{
		client: client,
	}

	// Initial time sync
	service.SyncTime()

	// Start periodic sync in background
	go service.startPeriodicSync()

	return service
}

// startPeriodicSync starts periodic time synchronization
func (t *TimeSyncService) startPeriodicSync() {
	ticker := time.NewTicker(5 * time.Minute) // Sync every 5 minutes
	defer ticker.Stop()

	for range ticker.C {
		t.SyncTime()
	}
}

// SyncTime synchronizes time with the exchange
func (t *TimeSyncService) SyncTime() {
	localTimeBefore := time.Now()
	serverTime, err := t.client.NewServerTimeService().Do(context.Background())
	if err != nil {
		log.Printf("TimeSyncService: ERROR: Failed to get server time: %v", err)
		return
	}
	localTimeAfter := time.Now()

	// Calculate round trip time
	roundTripTime := localTimeAfter.Sub(localTimeBefore)
	estimatedTransmissionDelay := roundTripTime / 2

	// Calculate server time with transmission delay compensation
	adjustedServerTime := time.UnixMilli(serverTime).Add(estimatedTransmissionDelay)

	// Calculate time difference
	timeDiff := localTimeBefore.Add(estimatedTransmissionDelay).Sub(adjustedServerTime)

	t.mu.Lock()
	t.timeDiff = timeDiff
	t.lastUpdate = time.Now()
	t.mu.Unlock()

	log.Printf("TimeSyncService: Time difference updated: %v (round-trip: %v, transmission delay: %v)",
		timeDiff, roundTripTime, estimatedTransmissionDelay)
}

// GetTimeDiff returns the current time difference
func (t *TimeSyncService) GetTimeDiff() time.Duration {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.timeDiff
}

// CorrectTimestamp applies time correction to a timestamp
func (t *TimeSyncService) CorrectTimestamp(timestamp time.Time) time.Time {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return timestamp.Add(t.timeDiff)
}

// GetCorrectedTime returns the current time with correction applied
func (t *TimeSyncService) GetCorrectedTime() time.Time {
	return t.CorrectTimestamp(time.Now())
}
