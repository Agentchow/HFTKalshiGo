package events

import (
	"sync"
)

// Handler processes an event. Returning an error logs it but does not stop dispatch.
type Handler func(Event) error

// Bus is a synchronous in-process event bus.
// Subscribers are invoked in registration order on the publisher's goroutine.
// For async processing, handlers should send to their own channel/goroutine.
type Bus struct {
	mu       sync.RWMutex
	handlers map[EventType][]Handler
}

func NewBus() *Bus {
	return &Bus{
		handlers: make(map[EventType][]Handler),
	}
}

// Subscribe registers a handler for a given event type.
func (b *Bus) Subscribe(eventType EventType, h Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[eventType] = append(b.handlers[eventType], h)
}

// Publish dispatches an event to all registered handlers for its type.
func (b *Bus) Publish(e Event) {
	b.mu.RLock()
	handlers := b.handlers[e.Type]
	b.mu.RUnlock()

	for _, h := range handlers {
		if err := h(e); err != nil {
			// logged but not fatal — one bad handler shouldn't block others
			_ = err
		}
	}
}
