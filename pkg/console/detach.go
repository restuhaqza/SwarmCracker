// This file owns the local detach sequence shared by every console client: the
// byte filter for Ctrl-P Ctrl-Q, which is intercepted on the client instead of
// being forwarded to the guest.

package console

import (
	"errors"
	"io"
	"sync/atomic"
)

// The Ctrl-P Ctrl-Q detach sequence.
const (
	detachPrefix byte = 0x10 // Ctrl-P
	detachSuffix byte = 0x11 // Ctrl-Q
)

// ErrDetached reports that the user asked to detach from the console. A caller
// that reads it from a DetachReader ends the session without an error.
var ErrDetached = errors.New("console detached")

// readBufferBytes is the chunk size the filter pulls from the underlying
// reader. Console input is typed by a human, so a small buffer is enough and
// keeps the latency of a single keystroke at one read.
const readBufferBytes = 1024

// DetachReader filters the Ctrl-P Ctrl-Q sequence out of a console input
// stream.
//
// Bytes that precede the sequence are still delivered, so a caller forwarding
// them does not lose the tail of what the user typed; once Read returns
// ErrDetached the reader yields nothing more. The filter works across read
// boundaries: a read may end with a pending Ctrl-P byte, and the next read
// decides whether it was a detach prefix or a byte to forward.
//
// A DetachReader is not safe for concurrent use by readers: exactly one
// goroutine reads from it. Any goroutine may however call Detached or watch
// Done while that read is in flight, because both observe atomics.
type DetachReader struct {
	r io.Reader

	// buf is the raw read buffer. pending holds filtered bytes the caller's
	// slice was too small for.
	buf     []byte
	pending []byte
	// pendingErr is delivered once pending is empty: bytes always precede the
	// error that ended them, so a writer never drops the tail of the stream.
	pendingErr error
	// pendingCtrlP carries a prefix byte across reads.
	pendingCtrlP bool

	// detached is set by the reading goroutine and read by whoever owns the
	// transport, so it must be atomic rather than a plain bool.
	detached atomic.Bool
	done     chan struct{}
}

// NewDetachReader wraps r with the detach filter.
func NewDetachReader(r io.Reader) *DetachReader {
	return &DetachReader{
		r:    r,
		buf:  make([]byte, readBufferBytes),
		done: make(chan struct{}),
	}
}

// Read returns input with the detach sequence removed, and ErrDetached once the
// sequence has been seen.
func (d *DetachReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	for {
		if len(d.pending) > 0 {
			n := copy(p, d.pending)
			d.pending = d.pending[n:]
			return n, nil
		}
		if d.pendingErr != nil {
			err := d.pendingErr
			d.pendingErr = nil
			return 0, err
		}
		if d.detached.Load() {
			return 0, ErrDetached
		}

		n, err := d.r.Read(d.buf)
		if n > 0 {
			var detached bool
			d.pending, d.pendingCtrlP, detached = filterDetach(d.pending[:0], d.buf[:n], d.pendingCtrlP)
			if detached {
				d.detached.Store(true)
				close(d.done)
			} else if err != nil {
				d.pendingErr = err
			}
			continue
		}

		if err != nil {
			// A lone Ctrl-P at the end of the stream is input, not a prefix
			// waiting for its suffix.
			if d.pendingCtrlP && errors.Is(err, io.EOF) {
				d.pendingCtrlP = false
				d.pending = append(d.pending, detachPrefix)
				d.pendingErr = err
				continue
			}
			return 0, err
		}
	}
}

// Done returns a channel that is closed when the detach sequence is seen. A
// caller that owns the transport (for example an HTTP request) watches it to
// abort the session promptly instead of waiting for a read to fail.
func (d *DetachReader) Done() <-chan struct{} {
	return d.done
}

// Detached reports whether the detach sequence has been seen. It is safe to
// call from any goroutine while another one reads.
func (d *DetachReader) Detached() bool {
	return d.detached.Load()
}

// filterDetach copies the bytes of src that are not part of the detach sequence
// onto dst. It returns the extended slice, whether a trailing lone Ctrl-P is
// still pending, and whether the full sequence was seen. A lone Ctrl-P is
// withheld until the next byte decides, because it may be the prefix.
func filterDetach(dst, src []byte, pendingPrefix bool) (out []byte, carry, detached bool) {
	for _, b := range src {
		if pendingPrefix {
			pendingPrefix = false
			if b == detachSuffix {
				return dst, false, true
			}
			dst = append(dst, detachPrefix)
		}
		if b == detachPrefix {
			pendingPrefix = true
			continue
		}
		dst = append(dst, b)
	}

	return dst, pendingPrefix, false
}
