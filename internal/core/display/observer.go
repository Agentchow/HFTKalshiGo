package display

import (
	"sync"
	"time"

	"github.com/charleschow/hft-trading/internal/core/state/game"
	"github.com/charleschow/hft-trading/internal/events"
)

const edgeDisplayThrottle = 30 * time.Second

// Displayer knows how to format and print game state for a given event.
type Displayer interface {
	DisplayGame(gc *game.GameContext, eventType string)
}

// DisplayObserver implements game.GameObserver.
// It delegates display formatting to the sport-specific Displayer and
// throttles PRICE_UPDATE edge prints.
type DisplayObserver struct {
	lookup   func(events.Sport) (Displayer, bool)
	mu       sync.Mutex
	lastEdge map[string]time.Time
}

// NewObserver creates a DisplayObserver. The lookup function maps a sport
// to its Displayer (typically wrapping strategy.Registry.Get).
func NewObserver(lookup func(events.Sport) (Displayer, bool)) *DisplayObserver {
	return &DisplayObserver{
		lookup:   lookup,
		lastEdge: make(map[string]time.Time),
	}
}

func (d *DisplayObserver) OnGameEvent(gc *game.GameContext, eventType string) {
	disp, ok := d.lookup(gc.Sport)
	if !ok {
		return
	}

	if eventType == "PRICE_UPDATE" {
		d.mu.Lock()
		last, exists := d.lastEdge[gc.EID]
		if exists && time.Since(last) < edgeDisplayThrottle {
			d.mu.Unlock()
			return
		}
		if gc.Game.HasSignificantEdge() {
			d.lastEdge[gc.EID] = time.Now()
			d.mu.Unlock()
			disp.DisplayGame(gc, "EDGE")
		} else {
			d.mu.Unlock()
		}
		return
	}

	disp.DisplayGame(gc, eventType)
}
