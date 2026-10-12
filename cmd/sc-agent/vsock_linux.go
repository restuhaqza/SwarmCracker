//go:build linux

package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

// acceptPollTimeout bounds how long Accept waits for a pending connection
// before re-checking whether the listener was closed. A bounded wait lets a
// concurrent Close be observed without blocking a goroutine forever in the
// kernel.
const acceptPollTimeout = 200 * time.Millisecond

// Listen returns a listener bound to AF_VSOCK on the given port.
//
// Go's net package cannot wrap an AF_VSOCK socket: net.FileListener derives the
// address family from getsockname and only recognises Inet4, Inet6 and Unix,
// returning EPROTONOSUPPORT for anything else (see net/file_posix.go,
// newFileFD). Listen therefore binds the socket directly through x/sys/unix and
// serves it with the minimal net.Listener/net.Conn implementation below,
// layered on unix.Accept4/unix.Read/unix.Write.
func Listen(port uint32) (net.Listener, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("create AF_VSOCK socket: %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: port}); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("bind AF_VSOCK port %d: %w", port, err)
	}
	if err := unix.Listen(fd, 128); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("listen on AF_VSOCK port %d: %w", port, err)
	}
	return &vsockListener{fd: fd, port: port}, nil
}

// vsockListener is a minimal net.Listener over an AF_VSOCK socket. Its
// underlying fd is non-blocking, so Accept polls in bounded slices to stay
// responsive to Close.
type vsockListener struct {
	fd     int
	port   uint32
	closed atomic.Bool
}

var _ net.Listener = (*vsockListener)(nil)

// Accept waits for and returns the next connection. Once Close has been called
// it returns net.ErrClosed; other errors are returned to the caller, which is
// expected to retry transient failures.
func (l *vsockListener) Accept() (net.Conn, error) {
	for {
		nfd, _, err := unix.Accept4(l.fd, unix.SOCK_CLOEXEC)
		switch {
		case err == nil:
			return &vsockConn{fd: nfd}, nil
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.EAGAIN):
			// No pending connection: wait for readiness, then retry. The
			// timeout is what makes a concurrent Close observable.
			pfds := []unix.PollFd{{Fd: int32(l.fd), Events: unix.POLLIN}} //nolint:gosec // G115: a socket descriptor fits in int32
			if _, perr := unix.Poll(pfds, int(acceptPollTimeout/time.Millisecond)); perr != nil && !errors.Is(perr, unix.EINTR) {
				if l.closed.Load() {
					return nil, net.ErrClosed
				}
				return nil, fmt.Errorf("poll AF_VSOCK listener: %w", perr)
			}
			continue
		case l.closed.Load() || errors.Is(err, unix.EBADF):
			return nil, net.ErrClosed
		default:
			return nil, fmt.Errorf("accept AF_VSOCK connection: %w", err)
		}
	}
}

// Close stops the listener and unblocks Accept. A second Close returns
// net.ErrClosed, matching the net package's own listeners.
func (l *vsockListener) Close() error {
	if l.closed.Swap(true) {
		return net.ErrClosed
	}
	if err := unix.Close(l.fd); err != nil {
		return fmt.Errorf("close AF_VSOCK listener: %w", err)
	}
	return nil
}

// Addr reports the listener's address. The CID is VMADDR_CID_ANY: the listener
// accepts connections addressed to any local context ID.
func (l *vsockListener) Addr() net.Addr {
	return &vsockAddr{cid: unix.VMADDR_CID_ANY, port: l.port}
}

// vsockAddr identifies an AF_VSOCK endpoint.
type vsockAddr struct {
	cid  uint32
	port uint32
}

var _ net.Addr = (*vsockAddr)(nil)

func (a *vsockAddr) Network() string { return "vsock" }

func (a *vsockAddr) String() string { return fmt.Sprintf("%d:%d", a.cid, a.port) }

// vsockConn is a minimal net.Conn over a connected AF_VSOCK socket. Deadlines
// are best-effort: they are applied as SO_RCVTIMEO/SO_SNDTIMEO, so an expired
// deadline surfaces as a net.Error whose Timeout reports true rather than as a
// hard context cancellation. Linux evaluates those options when a wait begins,
// so changing a deadline does not interrupt a read already blocked in
// unix.Read; callers must not rely on SetReadDeadline to wake an in-flight read.
type vsockConn struct {
	fd     int
	closed atomic.Bool
}

var _ net.Conn = (*vsockConn)(nil)

// Read reads from the connection. End-of-stream is reported as io.EOF.
func (c *vsockConn) Read(p []byte) (int, error) {
	for {
		n, err := unix.Read(c.fd, p)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return n, wrapConnError("read", err)
		}
		if n == 0 && len(p) > 0 {
			return 0, io.EOF
		}
		return n, nil
	}
}

// Write writes all of p to the connection, retrying partial writes so the
// io.Writer contract (a short write returns an error) holds.
func (c *vsockConn) Write(p []byte) (int, error) {
	written := 0
	for written < len(p) {
		n, err := unix.Write(c.fd, p[written:])
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return written, wrapConnError("write", err)
		}
		if n == 0 {
			return written, io.ErrShortWrite
		}
		written += n
	}
	return written, nil
}

// Close closes the connection. A second Close returns net.ErrClosed.
func (c *vsockConn) Close() error {
	if c.closed.Swap(true) {
		return net.ErrClosed
	}
	if err := unix.Close(c.fd); err != nil {
		return fmt.Errorf("close AF_VSOCK connection: %w", err)
	}
	return nil
}

// LocalAddr returns the local vsock address, or a zero address if it cannot be
// determined.
func (c *vsockConn) LocalAddr() net.Addr { return sockaddrVMAddr(c.fd, unix.Getsockname) }

// RemoteAddr returns the peer's vsock address, or a zero address if it cannot
// be determined.
func (c *vsockConn) RemoteAddr() net.Addr { return sockaddrVMAddr(c.fd, unix.Getpeername) }

// SetDeadline sets both the read and write deadlines.
func (c *vsockConn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}

// SetReadDeadline sets the read deadline as a best-effort SO_RCVTIMEO. A change
// only affects waits that begin after it and never interrupts a read already
// blocked in the kernel, so it must not be used to wake an in-flight read.
func (c *vsockConn) SetReadDeadline(t time.Time) error {
	return setConnTimeout(c.fd, unix.SO_RCVTIMEO, t)
}

// SetWriteDeadline sets the write deadline as a best-effort SO_SNDTIMEO. As with
// SetReadDeadline, it cannot interrupt an in-flight write.
func (c *vsockConn) SetWriteDeadline(t time.Time) error {
	return setConnTimeout(c.fd, unix.SO_SNDTIMEO, t)
}

// sockaddrVMAddr converts a getsockname/getpeername result into a net.Addr.
func sockaddrVMAddr(fd int, get func(int) (unix.Sockaddr, error)) net.Addr {
	sa, err := get(fd)
	if err != nil {
		return &vsockAddr{}
	}
	if vm, ok := sa.(*unix.SockaddrVM); ok {
		return &vsockAddr{cid: vm.CID, port: vm.Port}
	}
	return &vsockAddr{}
}

// setConnTimeout applies a best-effort socket timeout. A zero t clears the
// timeout; a t in the past makes the next operation time out immediately.
func setConnTimeout(fd, opt int, t time.Time) error {
	tv := unix.NsecToTimeval(0) // zero => no timeout
	if !t.IsZero() {
		d := time.Until(t)
		if d <= 0 {
			// SO_RCVTIMEO/SO_SNDTIMEO treat an all-zero value as "no timeout",
			// so an already-expired deadline needs a minimal non-zero value to
			// fail fast instead of blocking forever.
			tv = unix.Timeval{Usec: 1}
		} else {
			tv = unix.NsecToTimeval(d.Nanoseconds())
		}
	}
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, opt, &tv); err != nil {
		return fmt.Errorf("set AF_VSOCK timeout: %w", err)
	}
	return nil
}

// wrapConnError classifies a socket error, mapping a timeout into a net.Error
// so callers can distinguish it from a hard failure.
func wrapConnError(op string, err error) error {
	if errors.Is(err, unix.EAGAIN) {
		return &connTimeoutError{op: op, err: err}
	}
	return fmt.Errorf("vsock %s: %w", op, err)
}

// connTimeoutError reports a best-effort socket timeout as a net.Error.
type connTimeoutError struct {
	op  string
	err error
}

var _ net.Error = (*connTimeoutError)(nil)

func (e *connTimeoutError) Error() string   { return fmt.Sprintf("vsock %s: %v", e.op, e.err) }
func (e *connTimeoutError) Unwrap() error   { return e.err }
func (e *connTimeoutError) Timeout() bool   { return true }
func (e *connTimeoutError) Temporary() bool { return true }
