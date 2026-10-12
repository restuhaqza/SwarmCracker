package swarmkit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
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

// TestHealthcheckProbeTimeout asserts a healthcheck with no positive timeout
// falls back to the default. SwarmKit's "inherit" is a nil duration, which
// converts to zero, and a zero timeout would cancel every probe instantly.
func TestHealthcheckProbeTimeout(t *testing.T) {
	assert.Equal(t, defaultHealthcheckTimeout, healthcheckProbeTimeout(nil))
	assert.Equal(t, defaultHealthcheckTimeout, healthcheckProbeTimeout(&types.Healthcheck{}))
	assert.Equal(t, defaultHealthcheckTimeout, healthcheckProbeTimeout(&types.Healthcheck{Timeout: -time.Second}))
	assert.Equal(t, 3*time.Second, healthcheckProbeTimeout(&types.Healthcheck{Timeout: 3 * time.Second}))
}

// TestRunHealthcheckZeroTimeoutStillProbes proves the fallback end to end: a
// spec that declares a healthcheck without a timeout still runs its probe.
func TestRunHealthcheckZeroTimeoutStillProbes(t *testing.T) {
	m, uds := startFakeVsockMuxer(t)

	healthy, _, err := runHealthcheck(context.Background(), uds,
		&types.Healthcheck{Timeout: 0}, []string{"/bin/sh", "-c", "echo ok"})
	require.NoError(t, err)
	assert.True(t, healthy)
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

// TestHealthcheckProbeInterval asserts a healthcheck with no positive interval
// falls back to the default. SwarmKit's "inherit" is a nil duration, which
// converts to zero, and a zero interval would spin the CPU between probes.
func TestHealthcheckProbeInterval(t *testing.T) {
	assert.Equal(t, defaultHealthcheckInterval, healthcheckProbeInterval(nil))
	assert.Equal(t, defaultHealthcheckInterval, healthcheckProbeInterval(&types.Healthcheck{}))
	assert.Equal(t, defaultHealthcheckInterval, healthcheckProbeInterval(&types.Healthcheck{Interval: -time.Second}))
	assert.Equal(t, 5*time.Second, healthcheckProbeInterval(&types.Healthcheck{Interval: 5 * time.Second}))
}

// newStartController builds a Controller whose Start path boots against mocks
// and whose awaitHealthy probes the fake vsock muxer at vsockUDS. socketPath is
// derived so that vsockUDSPath(socketPath) resolves back to vsockUDS.
func newStartController(t *testing.T, vsockUDS string, hc *api.HealthConfig) *Controller {
	t.Helper()
	return &Controller{
		task: &api.Task{
			ID: "task-hc-start",
			Spec: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{
					Container: &api.ContainerSpec{Image: "nginx", Healthcheck: hc},
				},
			},
		},
		internalTask: &types.Task{
			ID:   "task-hc-start",
			Spec: types.TaskSpec{Runtime: &types.Container{Image: "nginx"}},
		},
		config:     &Config{SocketDir: t.TempDir(), Hostname: "test-host"},
		trans:      &MockTaskTranslator{},
		vmmMgr:     &MockVMMManager{},
		networkMgr: &MockNetworkManager{},
		socketPath: strings.TrimSuffix(vsockUDS, ".vsock") + ".sock",
		prepared:   true,
	}
}

// TestController_Start_NoHealthcheckDoesNotProbe asserts a task with no
// healthcheck is completely unchanged: Start returns without waiting even when
// the vsock path is missing, so it can never have dialled it.
func TestController_Start_NoHealthcheckDoesNotProbe(t *testing.T) {
	missing := filepath.Join(shortHealthcheckDir(t), "missing.vsock")
	ctrl := newStartController(t, missing, nil)

	start := time.Now()
	require.NoError(t, ctrl.Start(context.Background()))
	assert.Less(t, time.Since(start), time.Second, "Start must not wait when no healthcheck is declared")
}

// TestController_Start_DisabledHealthcheckDoesNotProbe asserts a ["NONE"] test
// disables the gate entirely.
func TestController_Start_DisabledHealthcheckDoesNotProbe(t *testing.T) {
	missing := filepath.Join(shortHealthcheckDir(t), "missing.vsock")
	ctrl := newStartController(t, missing, &api.HealthConfig{Test: []string{"NONE"}})

	start := time.Now()
	require.NoError(t, ctrl.Start(context.Background()))
	assert.Less(t, time.Since(start), time.Second)
}

// TestController_Start_HealthyFirstProbe asserts Start returns nil once the
// first probe passes.
func TestController_Start_HealthyFirstProbe(t *testing.T) {
	_, uds := startFakeVsockMuxer(t)
	hc := &api.HealthConfig{
		Test:     []string{"CMD-SHELL", "true"},
		Interval: gogotypes.DurationProto(10 * time.Millisecond),
		Timeout:  gogotypes.DurationProto(2 * time.Second),
		Retries:  3,
	}
	ctrl := newStartController(t, uds, hc)

	require.NoError(t, ctrl.Start(context.Background()))
}

// TestController_Start_UnhealthyAfterRetries asserts that after Retries
// consecutive failures outside the start period, Start returns an error naming
// the command and the last output, and that it is not a context error.
func TestController_Start_UnhealthyAfterRetries(t *testing.T) {
	_, uds := startFakeVsockMuxer(t)
	hc := &api.HealthConfig{
		Test:     []string{"CMD-SHELL", "echo boom 1>&2; exit 3"},
		Interval: gogotypes.DurationProto(10 * time.Millisecond),
		Timeout:  gogotypes.DurationProto(2 * time.Second),
		Retries:  3,
	}
	ctrl := newStartController(t, uds, hc)

	err := ctrl.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "healthcheck failed after 3 attempts")
	assert.Contains(t, err.Error(), "boom")
	assert.False(t, errors.Is(err, context.Canceled), "an unhealthy verdict must not masquerade as cancellation")
	assert.False(t, errors.Is(err, context.DeadlineExceeded))
}

// TestController_Start_StartPeriodFailuresDoNotExhaustRetries asserts a failure
// inside the start period does not count toward Retries: the check fails once
// then passes, with Retries=1, and Start still succeeds.
func TestController_Start_StartPeriodFailuresDoNotExhaustRetries(t *testing.T) {
	_, uds := startFakeVsockMuxer(t)
	countFile := filepath.Join(shortHealthcheckDir(t), "count")
	cmd := fmt.Sprintf(`n=$(cat %s 2>/dev/null || echo 0); n=$((n+1)); echo $n > %s; [ "$n" -ge 2 ]`,
		countFile, countFile)
	hc := &api.HealthConfig{
		Test:        []string{"CMD-SHELL", cmd},
		Interval:    gogotypes.DurationProto(10 * time.Millisecond),
		Timeout:     gogotypes.DurationProto(2 * time.Second),
		Retries:     1,
		StartPeriod: gogotypes.DurationProto(5 * time.Second),
	}
	ctrl := newStartController(t, uds, hc)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, ctrl.Start(ctx))
}

// TestController_Start_TransportErrorIsUnhealthy asserts a dial/exec failure is
// counted as an unhealthy probe, not surfaced as a transport error.
func TestController_Start_TransportErrorIsUnhealthy(t *testing.T) {
	missing := filepath.Join(shortHealthcheckDir(t), "missing.vsock")
	hc := &api.HealthConfig{
		Test:     []string{"CMD-SHELL", "true"},
		Interval: gogotypes.DurationProto(10 * time.Millisecond),
		Timeout:  gogotypes.DurationProto(200 * time.Millisecond),
		Retries:  1,
	}
	ctrl := newStartController(t, missing, hc)

	err := ctrl.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "healthcheck failed")
	assert.False(t, errors.Is(err, context.Canceled))
}

// TestController_Start_ContextCanceled asserts a shutdown surfaces as a context
// error (which SwarmKit retries) rather than hanging or reporting unhealthy.
func TestController_Start_ContextCanceled(t *testing.T) {
	_, uds := startFakeVsockMuxer(t)
	hc := &api.HealthConfig{
		Test:     []string{"CMD-SHELL", "exit 3"},
		Interval: gogotypes.DurationProto(50 * time.Millisecond),
		Timeout:  gogotypes.DurationProto(2 * time.Second),
		Retries:  1000,
	}
	ctrl := newStartController(t, uds, hc)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ctrl.Start(ctx) }()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.Error(t, err)
		assert.True(t, errors.Is(err, context.Canceled), "shutdown must surface as a context error: %v", err)
		// SwarmKit classifies retryability with pkg/errors.Cause, which unwraps
		// only errors implementing `Cause() error` — NOT the Unwrap() that
		// fmt.Errorf("%w") provides. A plainly wrapped context error would make
		// a shutdown during the health gate a task FAILURE instead of a retry,
		// so assert the contract that library actually relies on.
		var causer interface{ Cause() error }
		require.True(t, errors.As(err, &causer),
			"error must implement Cause() for SwarmKit's retry classification: %v", err)
		assert.Equal(t, context.Canceled, causer.Cause())
	case <-time.After(3 * time.Second):
		t.Fatal("Start hung after cancellation")
	}
}

// TestController_Start_ReleasesMutexDuringHealthWait proves awaitHealthy does
// not hold c.mu: while Start is blocked waiting on health, Terminate (which
// takes c.mu) must still complete promptly.
func TestController_Start_ReleasesMutexDuringHealthWait(t *testing.T) {
	_, uds := startFakeVsockMuxer(t)
	hc := &api.HealthConfig{
		Test:     []string{"CMD-SHELL", "exit 3"},
		Interval: gogotypes.DurationProto(200 * time.Millisecond),
		Timeout:  gogotypes.DurationProto(2 * time.Second),
		Retries:  100,
	}
	ctrl := newStartController(t, uds, hc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startErr := make(chan error, 1)
	go func() { startErr <- ctrl.Start(ctx) }()

	// Let startVM finish and the first probe fail; the loop then sleeps for the
	// interval, so Start is now waiting on health.
	time.Sleep(100 * time.Millisecond)

	termDone := make(chan error, 1)
	go func() { termDone <- ctrl.Terminate(context.Background()) }()
	select {
	case err := <-termDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Terminate blocked while Start waited on health: c.mu was held during the wait")
	}

	cancel()
	select {
	case err := <-startErr:
		require.Error(t, err)
		assert.True(t, errors.Is(err, context.Canceled))
	case <-time.After(3 * time.Second):
		t.Fatal("Start did not return after ctx cancellation")
	}
}
