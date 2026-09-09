package service

import (
	"sync"
	"time"

	"github.com/Mark7888/two-place-paste/desktop/internal/localui"
)

// eventBuffer is how far behind one UI tab may fall before its events start
// being dropped. Events are refresh hints, not a log: a tab that missed one
// still refreshes on the next.
const eventBuffer = 16

// hub fans service events out to the connected UI tabs.
type hub struct {
	mu   sync.Mutex
	next int
	subs map[int]chan localui.Event
	now  func() time.Time
}

func newHub(now func() time.Time) *hub {
	return &hub{subs: map[int]chan localui.Event{}, now: now}
}

// subscribe returns a channel of events and the function that ends it. The
// channel is closed by the unsubscribe function, so a reader ranging over it
// terminates.
func (h *hub) subscribe() (<-chan localui.Event, func()) {
	ch := make(chan localui.Event, eventBuffer)
	h.mu.Lock()
	id := h.next
	h.next++
	h.subs[id] = ch
	h.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			if sub, ok := h.subs[id]; ok {
				delete(h.subs, id)
				close(sub)
			}
		})
	}
}

// publish delivers an event to every subscriber, dropping it for any that is
// not keeping up. A slow tab must never block the client's own goroutine,
// which is where most of these events originate.
func (h *hub) publish(ev localui.Event) {
	if ev.At.IsZero() {
		ev.At = h.now()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// close ends every subscription, which is what stops the UI's event streams
// when the service shuts down.
func (h *hub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, ch := range h.subs {
		delete(h.subs, id)
		close(ch)
	}
}
