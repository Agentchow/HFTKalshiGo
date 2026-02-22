package telemetry

import (
	"sync"
	"sync/atomic"
	"time"
)

type Counter struct {
	val atomic.Int64
}

func (c *Counter) Inc()          { c.val.Add(1) }
func (c *Counter) Add(n int64)   { c.val.Add(n) }
func (c *Counter) Value() int64  { return c.val.Load() }

type Gauge struct {
	val atomic.Int64
}

func (g *Gauge) Set(v int64)    { g.val.Store(v) }
func (g *Gauge) Inc()           { g.val.Add(1) }
func (g *Gauge) Dec()           { g.val.Add(-1) }
func (g *Gauge) Value() int64   { return g.val.Load() }

type LatencyTracker struct {
	mu      sync.Mutex
	samples []time.Duration
	maxKeep int
}

func NewLatencyTracker(maxKeep int) *LatencyTracker {
	return &LatencyTracker{maxKeep: maxKeep}
}

func (lt *LatencyTracker) Record(d time.Duration) {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	lt.samples = append(lt.samples, d)
	if len(lt.samples) > lt.maxKeep {
		lt.samples = lt.samples[len(lt.samples)-lt.maxKeep:]
	}
}

func (lt *LatencyTracker) P50() time.Duration { return lt.percentile(0.50) }
func (lt *LatencyTracker) P99() time.Duration { return lt.percentile(0.99) }

func (lt *LatencyTracker) percentile(p float64) time.Duration {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	if len(lt.samples) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(lt.samples))
	copy(sorted, lt.samples)
	// insertion sort — samples are small
	for i := 1; i < len(sorted); i++ {
		key := sorted[i]
		j := i - 1
		for j >= 0 && sorted[j] > key {
			sorted[j+1] = sorted[j]
			j--
		}
		sorted[j+1] = key
	}
	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}

// Metrics is the global metrics registry.
var Metrics = struct {
	WebhooksReceived   Counter
	WebhookParseErrors Counter
	EventsProcessed    Counter
	ScoreChanges       Counter
	OrderIntents       Counter
	OrdersSent         Counter
	OrderErrors        Counter
	ActiveGames        Gauge
	WebhookLatency     *LatencyTracker
	OrderE2ELatency    *LatencyTracker
	RateLimiterWait    *LatencyTracker
	InboxOverflows     Counter
}{
	WebhookLatency:  NewLatencyTracker(1000),
	OrderE2ELatency: NewLatencyTracker(1000),
	RateLimiterWait: NewLatencyTracker(1000),
}
