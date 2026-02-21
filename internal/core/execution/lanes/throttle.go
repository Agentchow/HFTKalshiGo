package lanes

import (
	"sync"
	"time"
)

// Throttle enforces a minimum interval between order placements.
type Throttle struct {
	mu       sync.Mutex
	interval time.Duration
	lastSend time.Time
}

func NewThrottle(intervalMs int64) *Throttle {
	return &Throttle{
		interval: time.Duration(intervalMs) * time.Millisecond,
	}
}

// Allow returns true if enough time has elapsed since the last order.
func (t *Throttle) Allow() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return time.Since(t.lastSend) >= t.interval
}

// Touch records the current time as the last send.
func (t *Throttle) Touch() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastSend = time.Now()
}
