package main

import (
	"bytes"
	"errors"
	"io"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func TestParseFlagsDefaults(t *testing.T) {
	opts, err := parseFlags(nil, io.Discard)
	if err != nil {
		t.Fatalf("parseFlags(nil) error = %v", err)
	}
	if opts.port != agent.DefaultPort {
		t.Errorf("default port = %d, want %d", opts.port, agent.DefaultPort)
	}
	if opts.version {
		t.Errorf("default version = true, want false")
	}
}

func TestParseFlagsPort(t *testing.T) {
	opts, err := parseFlags([]string{"-port", "1234"}, io.Discard)
	if err != nil {
		t.Fatalf("parseFlags(-port 1234) error = %v", err)
	}
	if opts.port != 1234 {
		t.Errorf("port = %d, want 1234", opts.port)
	}
}

func TestParseFlagsPortOutOfRange(t *testing.T) {
	_, err := parseFlags([]string{"-port", "5000000000"}, io.Discard)
	if err == nil {
		t.Fatal("parseFlags with an out-of-range port: expected error, got nil")
	}
}

func TestParseFlagsUnknownFlag(t *testing.T) {
	if _, err := parseFlags([]string{"-nope"}, io.Discard); err == nil {
		t.Fatal("parseFlags with an unknown flag: expected error, got nil")
	}
}

func TestRunVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-version"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run(-version) exit code = %d, want 0 (stderr: %s)", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != agent.Version {
		t.Fatalf("run(-version) stdout = %q, want %q", got, agent.Version)
	}
}

func TestRunUnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-nope"}, &stdout, &stderr); code != 2 {
		t.Fatalf("run(-nope) exit code = %d, want 2", code)
	}
}

// TestListenUnsupportedOS exercises the non-linux stub. On linux Listen is the
// real AF_VSOCK implementation, which needs a real vsock device, so it is
// skipped rather than faked.
func TestListenUnsupportedOS(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("Listen is implemented on linux; the AF_VSOCK path needs a real device")
	}
	_, err := Listen(agent.DefaultPort)
	if err == nil {
		t.Fatal("Listen on non-linux: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "vsock is only supported on linux") {
		t.Fatalf("Listen error = %q, want it to mention linux-only support", err)
	}
}

// fakeListener lets the accept loop be driven without a real vsock device.
type fakeListener struct {
	accept func() (net.Conn, error)
}

func (l *fakeListener) Accept() (net.Conn, error) { return l.accept() }
func (l *fakeListener) Close() error              { return nil }
func (l *fakeListener) Addr() net.Addr            { return &net.TCPAddr{} }

// TestServeHandlesConnection drives one protocol exchange through serve and
// checks that the loop returns cleanly once the listener reports net.ErrClosed.
func TestServeHandlesConnection(t *testing.T) {
	log.Logger = zerolog.Nop()

	client, guest := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })

	var calls int
	l := &fakeListener{accept: func() (net.Conn, error) {
		calls++
		if calls == 1 {
			return guest, nil
		}
		return nil, net.ErrClosed
	}}

	done := make(chan int, 1)
	go func() { done <- serve(l) }()

	if err := agent.WriteFrame(client, agent.Request{Op: "hello", Version: agent.Version}); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	var msg agent.Message
	if _, err := agent.ReadFrame(client, &msg); err != nil {
		t.Fatalf("read hello reply: %v", err)
	}
	if msg.Type != "hello" || msg.Version != agent.Version {
		t.Fatalf("hello reply = %+v, want type=hello version=%s", msg, agent.Version)
	}

	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("serve exit code = %d, want 0", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not return after the listener reported net.ErrClosed")
	}
}

// TestServeRetriesTransientAcceptError checks that a non-fatal Accept error is
// retried rather than terminating the loop.
func TestServeRetriesTransientAcceptError(t *testing.T) {
	log.Logger = zerolog.Nop()

	var calls int
	l := &fakeListener{accept: func() (net.Conn, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("transient accept failure")
		}
		return nil, net.ErrClosed
	}}

	done := make(chan int, 1)
	go func() { done <- serve(l) }()

	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("serve exit code = %d, want 0", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not survive a transient accept error")
	}
}
