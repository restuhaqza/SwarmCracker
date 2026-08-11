package executor

import (
	"sync"
	"testing"
)

// TestSendEventRaceWithClose proves the TOCTOU race in sendEvent:
// sendEvent checks eventsClosed under lock, releases the lock, then sends
// on e.events. If Close() runs between the check and the send, the channel
// is closed and the send panics ("send on closed channel").
//
// Run with -race and -count to increase the chance of triggering.
func TestSendEventRaceWithClose(t *testing.T) {
	exec := newTestExecutor()

	var wg sync.WaitGroup

	// Sender goroutines hammer sendEvent concurrently.
	const senders = 8
	for i := 0; i < senders; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 2000; j++ {
				exec.sendEvent(Event{
					Task:    nil,
					Message: "race-test",
				})
			}
		}(i)
	}

	// Close concurrently while senders are still active.
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = exec.Close()
	}()

	wg.Wait()

	// After Close, sendEvent must return false, never panic.
	if ok := exec.sendEvent(Event{Message: "after-close"}); ok {
		t.Fatal("sendEvent returned true after Close; expected false")
	}
}

// TestSendEventAfterClose verifies the basic contract: sendEvent returns
// false (not panic) once the executor is closed.
func TestSendEventAfterClose(t *testing.T) {
	exec := newTestExecutor()

	if err := exec.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	if ok := exec.sendEvent(Event{Message: "after-close"}); ok {
		t.Fatal("sendEvent returned true after Close; expected false")
	}
}

// newTestExecutor builds a FirecrackerExecutor with no external deps.
// Close() only touches the events machinery when networkMgr is nil,
// so a zero-config executor is enough for these tests.
func newTestExecutor() *FirecrackerExecutor {
	return &FirecrackerExecutor{
		events: make(chan Event, 100),
	}
}
