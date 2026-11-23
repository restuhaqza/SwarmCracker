package agent

import (
	"archive/tar"
	"bytes"
	"io"
	"net"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCopyToReportsENOSPCAndDoesNotHang is the defect a 2 GiB copy into a 2 GiB workspace
// found on a real host: the guest's disk filled, the write failed, and the command hung with
// no error, no exit status and no line anywhere.
//
// The deadlock is a pair of blocks: the guest answers the failure and waits for the caller,
// while the caller is still writing into a socket the guest no longer reads. Either side
// breaking it is enough; this pins the client's half — the copy returns a typed no_space
// failure instead of waiting forever.
func TestCopyToReportsENOSPCAndDoesNotHang(t *testing.T) {
	// The default window is 30s; the test's bound is 10s, so it runs with a short one. What
	// this pins is the outcome, not the duration.
	old := stallWindow
	stallWindow = 200 * time.Millisecond
	t.Cleanup(func() { stallWindow = old })

	local, remote := net.Pipe()
	// The guest: takes the request, reads a little, then answers no_space and stops reading
	// — exactly what a full disk does.
	go func() {
		var req Request
		if _, err := ReadFrame(remote, &req); err != nil {
			return
		}
		var msg Message
		_, _ = ReadStreamFrame(remote, &msg) // one data frame, then the disk is full
		_ = WriteFrame(remote, Message{Type: "error", ID: req.ID, ErrorCode: CodeNoSpace,
			Error: "write /workspace/big: no space left on device"})
		// Deliberately keeps the connection open and reads nothing more, as the guest did.
	}()

	c := testClient(local)
	src := bytes.NewReader(bytes.Repeat([]byte("x"), 8<<20)) // more than a socket buffer

	done := make(chan error, 1)
	go func() { done <- c.CopyToRooted(t.Context(), "big", src, 0o644) }()

	select {
	case err := <-done:
		require.Error(t, err, "a full disk must fail the copy")
		assert.NotErrorIs(t, err, io.EOF)
		// Either the guest's own typed failure arrived, or the watchdog reported the stall.
		// Neither may be a timeout of the caller's context: the caller set no deadline.
		if code := CodeOf(err); code == CodeNoSpace {
			assert.Contains(t, err.Error(), "no space", "the guest's reason reaches the caller")
		} else {
			assert.Equal(t, CodeDeadlineExceeded, code, "a peer that stops reading is a stall, not a silent wait: %v", err)
			assert.Contains(t, err.Error(), "stalled", "%v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the copy hung: no error, no exit — the failure this test exists for")
	}
}

// TestCopyToFailsWhenTheGuestStopsReading pins the watchdog on its own: the guest is healthy
// enough to answer the request, then reads nothing, and the client must give up rather than
// wait for room that never comes.
func TestCopyToFailsWhenTheGuestStopsReading(t *testing.T) {
	old := stallWindow
	stallWindow = 200 * time.Millisecond
	t.Cleanup(func() { stallWindow = old })

	local, remote := net.Pipe()
	defer func() { _ = remote.Close() }()
	go func() {
		var req Request
		if _, err := ReadFrame(remote, &req); err != nil {
			return
		}
		// Reads nothing at all, and never answers.
	}()

	c := testClient(local)
	src := bytes.NewReader(bytes.Repeat([]byte("x"), 4<<20))

	done := make(chan error, 1)
	go func() { done <- c.CopyToRooted(t.Context(), "big", src, 0o644) }()

	select {
	case err := <-done:
		require.Error(t, err)
		assert.Equal(t, CodeDeadlineExceeded, CodeOf(err), "a stall is reported as one: %v", err)
		assert.Contains(t, err.Error(), "stalled", "%v", err)
		assert.Contains(t, err.Error(), "bytes", "the message says how much got through: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("a guest that stopped reading must not park the caller forever")
	}
}

// TestAHealthyLargeCopyIsNotCutByTheWatchdog is the other half, and the reason the bound is
// progress rather than elapsed time: a transfer that keeps moving is never interrupted, even
// when it takes many times the stall window. Without this, the fix for the hang would cut
// every legitimate large copy.
func TestAHealthyLargeCopyIsNotCutByTheWatchdog(t *testing.T) {
	old := stallWindow
	stallWindow = 100 * time.Millisecond
	t.Cleanup(func() { stallWindow = old })

	local, remote := net.Pipe()
	go func() {
		var req Request
		if _, err := ReadFrame(remote, &req); err != nil {
			return
		}
		for {
			var msg Message
			if _, err := ReadStreamFrame(remote, &msg); err != nil {
				return
			}
			if len(msg.Data) == 0 {
				_ = WriteFrame(remote, Message{Type: "ok", ID: req.ID})
				return
			}
		}
	}()

	c := testClient(local)
	// A reader that delivers slowly but never stops: 12 chunks at ~30ms each is 360ms, more
	// than three stall windows, and no window passes with zero progress.
	src := &trickleReader{chunk: bytes.Repeat([]byte("y"), 4096), total: 12, pause: 30 * time.Millisecond}

	done := make(chan error, 1)
	go func() { done <- c.CopyToRooted(t.Context(), "slow-but-fine", src, 0o644) }()

	select {
	case err := <-done:
		require.NoError(t, err, "a transfer that keeps moving must never be cut for taking long")
	case <-time.After(15 * time.Second):
		t.Fatal("a healthy slow copy hung")
	}
}

// trickleReader hands out fixed chunks with a pause between them, so a stream moves bytes
// steadily while taking longer than any single stall window.
type trickleReader struct {
	chunk []byte
	total int
	pause time.Duration
	sent  int
}

func (r *trickleReader) Read(p []byte) (int, error) {
	if r.sent >= r.total {
		return 0, io.EOF
	}
	if r.sent > 0 {
		time.Sleep(r.pause)
	}
	n := copy(p, r.chunk)
	r.sent++
	return n, nil
}

// TestANoSpaceWriteIsClassifiedAndAnswered pins the guest's half of the defect: a write the
// kernel refuses for lack of space is reported as no_space — not as the generic internal
// failure it used to be — and the failure is the one handlePut/handleUntar answer with.
//
// The error is the kernel's own ENOSPC, wrapped the way a file write wraps it, so this covers
// the errno mapping rather than a mock string.
func TestANoSpaceWriteIsClassifiedAndAnswered(t *testing.T) {
	// A write that reaches the kernel and is refused for space is the real thing; this is the
	// same errno a full filesystem returns.
	refused := &os.PathError{Op: "write", Path: "/workspace/big", Err: syscall.ENOSPC}

	code, message := failureCode(classifyPathError(refused))
	assert.Equal(t, CodeNoSpace, code, "a full disk is its own code, not internal: %v", message)
	assert.Contains(t, message, "no space", "and the guest's own words survive: %v", message)

	// A quota refusal is the same answer: the disk has space, this caller may not have it.
	code, _ = failureCode(classifyPathError(&os.PathError{Op: "write", Path: "/workspace/big", Err: syscall.EDQUOT}))
	assert.Equal(t, CodeNoSpace, code)

	// Everything else keeps its old classification.
	code, _ = failureCode(classifyPathError(&os.PathError{Op: "open", Path: "x", Err: syscall.EIO}))
	assert.Equal(t, CodeInternal, code, "an unrelated kernel failure is still internal")
}

// TestUntarWithAFullDiskAnswersAndLogs is the end of the chain: an untar whose extraction fails
// for lack of space answers with a typed frame instead of going quiet, and leaves a line on the
// console — the trace `sandbox logs` reads, and the one this failure had none of.
func TestUntarWithAFullDiskAnswersAndLogs(t *testing.T) {
	var console bytes.Buffer
	oldConsole := consoleLog
	consoleLog = &console
	t.Cleanup(func() { consoleLog = oldConsole })

	conn, _ := newPipePair(t)
	require.NoError(t, WriteFrame(conn, Request{ID: "1", Op: CapUntar, Path: "out"}))

	// An archive whose body the agent cannot place: a single entry naming a path outside the
	// tree it was given, which extraction refuses. What matters here is that the failure is
	// answered and logged rather than swallowed.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "../escape", Mode: 0o644, Size: 3, Typeflag: tar.TypeReg}))
	_, err := tw.Write([]byte("abc"))
	require.NoError(t, err)
	require.NoError(t, tw.Close())

	// net.Pipe is unbuffered, so the frames are written from another goroutine: this end must
	// be reading the reply while the other is still sending.
	go func() {
		_ = WriteFrame(conn, Message{ID: "1", Type: "file", Data: buf.Bytes()})
		_ = WriteFrame(conn, Message{ID: "1", Type: "file"})
	}()

	var reply Message
	_, err = ReadFrame(conn, &reply)
	require.NoError(t, err)
	assert.Equal(t, "error", reply.Type, "the guest answers a failed extraction instead of going quiet")
	assert.NotEmpty(t, reply.ErrorCode)

	assert.Contains(t, console.String(), "untar", "the failure is on the console an operator reads: %q", console.String())
	assert.Contains(t, console.String(), "failed", "%q", console.String())
}

// TestCopyHearsTheGuestsAnswerWhileStillSending is the defect the 3 GiB host run exposed, and
// the reason the previous fix was only half of one.
//
// The guest answered no_space after a few hundred kilobytes and then stopped reading — exactly
// what a full disk does. The client could not hear it: it wrote its whole stream first and only
// read afterwards, so it stayed blocked writing frames nobody would read until the watchdog
// broke the connection 30 seconds later. The caller saw deadline_exceeded for a disk that was
// full, which is the wrong answer and the wrong code.
//
// The test's bound is deliberately short — a fraction of the stall window — because returning
// *at all* is not the property; returning with the guest's own answer, promptly, is.
func TestCopyHearsTheGuestsAnswerWhileStillSending(t *testing.T) {
	oldWindow := stallWindow
	stallWindow = 10 * time.Second
	t.Cleanup(func() { stallWindow = oldWindow })

	local, remote := net.Pipe()
	defer func() { _ = remote.Close() }()
	go func() {
		var req Request
		if _, err := ReadFrame(remote, &req); err != nil {
			return
		}
		// Take one frame, then fail the way a full disk does: answer, and read nothing more.
		var msg Message
		_, _ = ReadStreamFrame(remote, &msg)
		_ = WriteFrame(remote, Message{Type: "error", ID: req.ID, ErrorCode: CodeNoSpace,
			Error: "write /workspace/big3g.bin.sc-put-tmp: no space left on device"})
	}()

	c := testClient(local)
	// Far more than the guest will accept: the send is still going when the answer arrives.
	src := bytes.NewReader(bytes.Repeat([]byte("z"), 64<<20))

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- c.CopyToRooted(t.Context(), "big3g.bin", src, 0o644) }()

	select {
	case err := <-done:
		elapsed := time.Since(start)
		require.Error(t, err, "a full disk must fail the copy")
		assert.Equal(t, CodeNoSpace, CodeOf(err),
			"the caller gets the guest's own answer, not a stall: %v", err)
		assert.Less(t, elapsed, stallWindow,
			"the answer arrives while the send is still running, not after the watchdog: %v", elapsed)
	case <-time.After(30 * time.Second):
		t.Fatal("the copy did not return: the guest's answer was never read")
	}
}

// TestUntarHearsTheGuestsAnswerWhileStillSending is the same property for the tree transfer,
// which had the identical shape.
func TestUntarHearsTheGuestsAnswerWhileStillSending(t *testing.T) {
	oldWindow := stallWindow
	stallWindow = 10 * time.Second
	t.Cleanup(func() { stallWindow = oldWindow })

	local, remote := net.Pipe()
	defer func() { _ = remote.Close() }()
	go func() {
		var req Request
		if _, err := ReadFrame(remote, &req); err != nil {
			return
		}
		var msg Message
		_, _ = ReadStreamFrame(remote, &msg)
		_ = WriteFrame(remote, Message{Type: "error", ID: req.ID, ErrorCode: CodeNoSpace,
			Error: "extract: no space left on device"})
	}()

	c := testClient(local)
	src := bytes.NewReader(bytes.Repeat([]byte("t"), 64<<20))

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := c.Untar(t.Context(), "tree", src)
		done <- err
	}()

	select {
	case err := <-done:
		elapsed := time.Since(start)
		require.Error(t, err)
		assert.Equal(t, CodeNoSpace, CodeOf(err), "the tree transfer hears it too: %v", err)
		assert.Less(t, elapsed, stallWindow, "and promptly: %v", elapsed)
	case <-time.After(30 * time.Second):
		t.Fatal("the untar did not return: the guest's answer was never read")
	}
}
