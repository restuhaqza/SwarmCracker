package console

import "sync"

// subBuffer is the number of pending chunks a subscriber may fall behind by
// before output starts being dropped. Output is dropped rather than blocked so
// a slow client can never stall the guest's serial device.
const subBuffer = 256

// Hub fans out guest console output to zero or more subscribers and keeps a
// bounded scrollback that is replayed to new subscribers.
type Hub struct {
	mu     sync.Mutex
	subs   map[*subscription]struct{}
	buf    []byte
	maxBuf int
	closed bool
}

type subscription struct {
	ch     chan []byte
	closed bool
}

// newHub creates a hub that retains up to maxBuf bytes of scrollback.
func newHub(maxBuf int) *Hub {
	return &Hub{
		subs:   make(map[*subscription]struct{}),
		maxBuf: maxBuf,
	}
}

// Broadcast delivers a chunk of console output to every subscriber and appends
// it to the scrollback. The chunk must not be modified by the caller
// afterwards; it is shared between subscribers.
func (h *Hub) Broadcast(p []byte) {
	if len(p) == 0 {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if h.maxBuf > 0 {
		h.buf = append(h.buf, p...)
		if len(h.buf) > h.maxBuf {
			h.buf = h.buf[len(h.buf)-h.maxBuf:]
		}
	}

	for sub := range h.subs {
		if sub.closed {
			continue
		}
		select {
		case sub.ch <- p:
		default:
			// Slow subscriber: drop this chunk instead of blocking the reader.
		}
	}
}

// Subscribe registers a new subscriber. The returned channel receives console
// chunks (scrollback first, then live output) and is closed when the returned
// cancel function is called or the hub is closed. cancel is idempotent.
func (h *Hub) Subscribe() (<-chan []byte, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()

	sub := &subscription{ch: make(chan []byte, subBuffer)}

	if h.closed {
		close(sub.ch)
		sub.closed = true
		return sub.ch, func() {}
	}

	// Queue the scrollback while still holding the lock so it is ordered
	// before any live chunk broadcast to this subscriber.
	if len(h.buf) > 0 {
		history := make([]byte, len(h.buf))
		copy(history, h.buf)
		sub.ch <- history
	}

	h.subs[sub] = struct{}{}

	cancel := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if sub.closed {
			return
		}
		sub.closed = true
		delete(h.subs, sub)
		close(sub.ch)
	}

	return sub.ch, cancel
}

// Close closes the hub and every subscriber channel.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return
	}
	h.closed = true

	for sub := range h.subs {
		if !sub.closed {
			sub.closed = true
			close(sub.ch)
		}
		delete(h.subs, sub)
	}
	h.buf = nil
}
