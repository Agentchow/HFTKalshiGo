package display

import (
	"sync"
	"time"
)

// State holds per-game display flags. Each *State is accessed exclusively
// from the game's own goroutine (inside gc.Send closures), so individual
// fields require no synchronization.
type State struct {
	DisplayedLIVE   bool
	GameStarted     bool
	Finaled         bool
	LastEdgeDisplay time.Time
	LastPregameWarn time.Time
}

// Tracker maps game EIDs to their display state. The map itself is
// mutex-protected so that concurrent game goroutines can safely create
// entries, but once a *State is returned it is goroutine-local.
type Tracker struct {
	mu     sync.Mutex
	states map[string]*State
}

func NewTracker() *Tracker {
	return &Tracker{
		states: make(map[string]*State),
	}
}

// Get returns the display state for a game, creating one if it
// does not yet exist.
func (t *Tracker) Get(eid string) *State {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.states[eid]
	if !ok {
		s = &State{}
		t.states[eid] = s
	}
	return s
}
