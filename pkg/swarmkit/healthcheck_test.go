package swarmkit

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	gogotypes "github.com/gogo/protobuf/types"
	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shortHealthcheckDir creates a temporary directory with a short path: Unix
// socket paths are limited to ~104 bytes and t.TempDir can exceed that on some
// hosts.
func shortHealthcheckDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sc-hc-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// readMuxHandshakeLine reads one '\n'-terminated line byte-by-byte, so it never
// over-reads into the framed hello request that follows the handshake.
func readMuxHandshakeLine(r io.Reader) (string, error) {
	var line []byte
	buf := make([]byte, 1)
	for len(line) < 64 {
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", err
		}
		if buf[0] == '\n' {
			return string(line), nil
		}
		line = append(line, buf[0])
	}
	return "", fmt.Errorf("handshake line too long")
}

// fakeVsockMuxer emulates Firecracker's vsock Unix-socket muxer: it listens on
// the uds_path itself, answers the CONNECT/OK handshake, and then bridges to
// agent.ServeConn so checks execute in the test process. closedCh fires once a
// served connection ends, which lets a test prove the client closed it.
type fakeVsockMuxer struct {
	ln       net.Listener
	closedCh chan struct{}
}

func startFakeVsockMuxer(t *testing.T) (*fakeVsockMuxer, string) {
	t.Helper()
	udsPath := filepath.Join(shortHealthcheckDir(t), "task.vsock")
	ln, err := net.Listen("unix", udsPath)
	require.NoError(t, err)

	m := &fakeVsockMuxer{ln: ln, closedCh: make(chan struct{}, 8)}
	go m.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return m, udsPath
}

func (m *fakeVsockMuxer) serve() {
	for {
		conn, err := m.ln.Accept()
		if err != nil {
			return
		}
		go m.handle(conn)
	}
}

func (m *fakeVsockMuxer) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	line, err := readMuxHandshakeLine(conn)
	if err != nil {
		return
	}
	if line != fmt.Sprintf("CONNECT %d", agent.DefaultPort) {
		_, _ = io.WriteString(conn, "ERR wrong port\n")
		return
	}
	if _, err := fmt.Fprintf(conn, "OK %d\n", 1073741824); err != nil {
		return
	}
	_ = agent.ServeConn(conn)
	m.closedCh <- struct{}{}
}

// waitClosed asserts the client closed its connection.
func (m *fakeVsockMuxer) waitClosed(t *testing.T) {
	t.Helper()
	select {
	case <-m.closedCh:
	case <-time.After(3 * time.Second):
		t.Fatal("client connection was not closed")
	}
}

func TestHealthcheckCommand(t *testing.T) {
	tests := []struct {
		name    string
		test    []string
		want    []string
		enabled bool
		wantErr bool
	}{
		{
			name:    "CMD-SHELL runs through the shell",
			test:    []string{"CMD-SHELL", "curl -f localhost"},
			want:    []string{"/bin/sh", "-c", "curl -f localhost"},
			enabled: true,
		},
		{
			name:    "CMD runs the arguments directly",
			test:    []string{"CMD", "curl", "-f", "localhost"},
			want:    []string{"curl", "-f", "localhost"},
			enabled: true,
		},
		{
			name:    "NONE disables the check",
			test:    []string{"NONE"},
			want:    nil,
			enabled: false,
		},
		{
			name:    "empty disables the check",
			test:    []string{},
			want:    nil,
			enabled: false,
		},
		{
			name:    "nil disables the check",
			test:    nil,
			want:    nil,
			enabled: false,
		},
		{
			name:    "CMD-SHELL without a command is an error",
			test:    []string{"CMD-SHELL"},
			wantErr: true,
		},
		{
			name:    "CMD without arguments is an error",
			test:    []string{"CMD"},
			wantErr: true,
		},
		{
			name:    "unknown directive is an error",
			test:    []string{"BOGUS", "x"},
			wantErr: true,
		},
		{
			name:    "directives are case-sensitive",
			test:    []string{"cmd-shell", "x"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, enabled, err := healthcheckCommand(tt.test)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.enabled, enabled)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRunHealthcheckHealthy(t *testing.T) {
	m, uds := startFakeVsockMuxer(t)
	h := &types.Healthcheck{Timeout: 5 * time.Second}

	healthy, out, err := runHealthcheck(context.Background(), uds, h,
		[]string{"/bin/sh", "-c", "echo hello"})
	require.NoError(t, err)
	assert.True(t, healthy)
	assert.Equal(t, "hello\n", out)
	m.waitClosed(t)
}

func TestRunHealthcheckNonZeroExitIsUnhealthy(t *testing.T) {
	m, uds := startFakeVsockMuxer(t)
	h := &types.Healthcheck{Timeout: 5 * time.Second}

	healthy, out, err := runHealthcheck(context.Background(), uds, h,
		[]string{"/bin/sh", "-c", "echo boom 1>&2; exit 3"})
	require.NoError(t, err, "a non-zero exit is an unhealthy verdict, not a transport error")
	assert.False(t, healthy)
	assert.Contains(t, out, "boom")
	m.waitClosed(t)
}

// TestRunHealthcheckTruncatesOutput asserts the combined output is bounded so a
// chatty guest cannot make the executor allocate unbounded memory.
func TestRunHealthcheckTruncatesOutput(t *testing.T) {
	m, uds := startFakeVsockMuxer(t)
	h := &types.Healthcheck{Timeout: 5 * time.Second}

	healthy, out, err := runHealthcheck(context.Background(), uds, h,
		[]string{"/bin/sh", "-c", "head -c 5000 /dev/zero | tr '\\0' x"})
	require.NoError(t, err)
	assert.True(t, healthy)
	assert.Len(t, out, maxHealthcheckOutput)
	m.waitClosed(t)
}

// TestRunHealthcheckTimeout asserts the probe is bounded by h.Timeout even when
// the command never returns.
func TestRunHealthcheckTimeout(t *testing.T) {
	m, uds := startFakeVsockMuxer(t)
	h := &types.Healthcheck{Timeout: 200 * time.Millisecond}

	start := time.Now()
	healthy, _, err := runHealthcheck(context.Background(), uds, h,
		[]string{"/bin/sh", "-c", "while true; do echo tick; sleep 0.05; done"})
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.False(t, healthy)
	assert.Less(t, elapsed, 2*time.Second, "the probe must be bounded by the configured timeout")
	m.waitClosed(t)
}

func TestRunHealthcheckDialError(t *testing.T) {
	missing := filepath.Join(shortHealthcheckDir(t), "missing.vsock")
	h := &types.Healthcheck{Timeout: 200 * time.Millisecond}

	healthy, _, err := runHealthcheck(context.Background(), missing, h, []string{"/bin/sh", "-c", "true"})
	require.Error(t, err)
	assert.False(t, healthy)
	assert.Contains(t, err.Error(), "dial")
}

func TestRunHealthcheckExecError(t *testing.T) {
	m, uds := startFakeVsockMuxer(t)
	h := &types.Healthcheck{Timeout: 5 * time.Second}

	healthy, _, err := runHealthcheck(context.Background(), uds, h,
		[]string{"definitely-not-a-real-binary-xyz"})
	require.Error(t, err, "a guest-side exec failure is reported as an error")
	assert.False(t, healthy)
	m.waitClosed(t)
}

func TestRunHealthcheckNilConfig(t *testing.T) {
	_, uds := startFakeVsockMuxer(t)

	healthy, _, err := runHealthcheck(context.Background(), uds, nil, []string{"/bin/sh", "-c", "true"})
	require.Error(t, err)
	assert.False(t, healthy)
}

func TestHealthcheckFromAPI(t *testing.T) {
	t.Run("full config", func(t *testing.T) {
		got := healthcheckFromAPI(&api.HealthConfig{
			Test:        []string{"CMD-SHELL", "curl -f localhost"},
			Interval:    gogotypes.DurationProto(5 * time.Second),
			Timeout:     gogotypes.DurationProto(2 * time.Second),
			Retries:     4,
			StartPeriod: gogotypes.DurationProto(10 * time.Second),
		})
		require.NotNil(t, got)
		assert.Equal(t, []string{"CMD-SHELL", "curl -f localhost"}, got.Test)
		assert.Equal(t, 5*time.Second, got.Interval)
		assert.Equal(t, 2*time.Second, got.Timeout)
		assert.Equal(t, 4, got.Retries)
		assert.Equal(t, 10*time.Second, got.StartPeriod)
	})

	t.Run("nil stays nil", func(t *testing.T) {
		assert.Nil(t, healthcheckFromAPI(nil))
	})

	t.Run("nil durations become zero", func(t *testing.T) {
		got := healthcheckFromAPI(&api.HealthConfig{Test: []string{"CMD", "true"}})
		require.NotNil(t, got)
		assert.Zero(t, got.Interval)
		assert.Zero(t, got.Timeout)
		assert.Zero(t, got.StartPeriod)
	})

	t.Run("invalid duration is treated as zero", func(t *testing.T) {
		got := healthcheckFromAPI(&api.HealthConfig{
			Test:     []string{"CMD", "true"},
			Interval: &gogotypes.Duration{Nanos: 2e9}, // nanos out of range
		})
		require.NotNil(t, got)
		assert.Zero(t, got.Interval)
	})
}

func TestConvertTask_Healthcheck(t *testing.T) {
	t.Run("converts the healthcheck onto the internal container", func(t *testing.T) {
		ctrl := &Controller{task: &api.Task{
			ID: "task-hc",
			Spec: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{
					Container: &api.ContainerSpec{
						Image: "nginx",
						Healthcheck: &api.HealthConfig{
							Test:        []string{"CMD-SHELL", "test -f /tmp/ready"},
							Interval:    gogotypes.DurationProto(30 * time.Second),
							Timeout:     gogotypes.DurationProto(3 * time.Second),
							Retries:     5,
							StartPeriod: gogotypes.DurationProto(15 * time.Second),
						},
					},
				},
			},
		}}

		container, err := ctrl.convertTask().Spec.GetContainer()
		require.NoError(t, err)
		require.NotNil(t, container.Healthcheck)
		assert.Equal(t, []string{"CMD-SHELL", "test -f /tmp/ready"}, container.Healthcheck.Test)
		assert.Equal(t, 30*time.Second, container.Healthcheck.Interval)
		assert.Equal(t, 3*time.Second, container.Healthcheck.Timeout)
		assert.Equal(t, 5, container.Healthcheck.Retries)
		assert.Equal(t, 15*time.Second, container.Healthcheck.StartPeriod)
	})

	t.Run("no healthcheck stays nil", func(t *testing.T) {
		ctrl := &Controller{task: &api.Task{
			ID: "task-no-hc",
			Spec: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{
					Container: &api.ContainerSpec{Image: "nginx"},
				},
			},
		}}

		container, err := ctrl.convertTask().Spec.GetContainer()
		require.NoError(t, err)
		assert.Nil(t, container.Healthcheck)
	})
}
