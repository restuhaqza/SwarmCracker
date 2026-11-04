package golden

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeVM is a started VM whose exit the test controls. kill makes it exit, so
// the prober's cleanup never waits for a process that does not exist.
type fakeVM struct {
	mu     sync.Mutex
	exits  chan error
	killed bool
}

func newFakeVM() *fakeVM { return &fakeVM{exits: make(chan error, 1)} }

func (v *fakeVM) done() <-chan error { return v.exits }

func (v *fakeVM) kill() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.killed {
		return
	}
	v.killed = true
	select {
	case v.exits <- nil:
	default:
	}
}

// exitNow makes the VM look like it died before the agent answered.
func (v *fakeVM) exitNow(err error) { v.exits <- err }

func (v *fakeVM) wasKilled() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.killed
}

// fakeClient is the guest side of a hello.
type fakeClient struct {
	hello  agent.Hello
	closed bool
}

func (c *fakeClient) Hello() agent.Hello { return c.hello }
func (c *fakeClient) Close() error {
	c.closed = true
	return nil
}

// probeRecorder records what the prober handed to the launcher and dialer, so a
// test can assert on the boot configuration without booting anything.
type probeRecorder struct {
	argv   []string
	config firecrackerConfig
	rootfs []byte
	uds    string
	dialed []string
}

func testTarget(t *testing.T) BootTarget {
	t.Helper()
	dir := t.TempDir()
	artifact := filepath.Join(dir, "artifact.ext4")
	kernel := filepath.Join(dir, "vmlinux")
	require.NoError(t, os.WriteFile(artifact, []byte("template-bytes"), 0o644))
	require.NoError(t, os.WriteFile(kernel, []byte("kernel-bytes"), 0o644))
	return BootTarget{
		ArtifactPath: artifact,
		KernelPath:   kernel,
		InitSystem:   InitSystemd,
		BootArgs:     []string{"systemd.unified_cgroup_hierarchy=1"},
	}
}

// recordingProber wires a prober to fakes and returns the recorder.
func recordingProber(t *testing.T, vm *fakeVM, hello agent.Hello) (*FirecrackerProber, *probeRecorder) {
	t.Helper()
	rec := &probeRecorder{}
	p := &FirecrackerProber{Binary: "/usr/bin/fake-firecracker"}
	p.launch = func(_ context.Context, argv []string, output io.Writer) (vmProcess, error) {
		rec.argv = append([]string(nil), argv...)
		_, _ = output.Write([]byte("serial output"))
		// Read the config the prober wrote, while its work dir still exists.
		cfgPath := argv[len(argv)-1]
		data, err := os.ReadFile(cfgPath)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &rec.config); err != nil {
			return nil, err
		}
		if len(rec.config.Drives) == 1 {
			rec.rootfs, err = os.ReadFile(rec.config.Drives[0].PathOnHost)
			if err != nil {
				return nil, err
			}
		}
		return vm, nil
	}
	p.dial = func(_ context.Context, uds string, _ uint32) (helloClient, error) {
		rec.uds = uds
		rec.dialed = append(rec.dialed, uds)
		return &fakeClient{hello: hello}, nil
	}
	return p, rec
}

func TestFirecrackerProber_BootsTheArtifactCopy(t *testing.T) {
	target := testTarget(t)
	vm := newFakeVM()
	p, rec := recordingProber(t, vm, healthyHello())

	hello, err := p.Probe(context.Background(), target)
	require.NoError(t, err)
	assert.Equal(t, healthyHello(), hello)

	// The VM was started with the configured binary and a Firecracker argv.
	require.Len(t, rec.argv, 5)
	assert.Equal(t, "/usr/bin/fake-firecracker", rec.argv[0])
	assert.Equal(t, "--api-sock", rec.argv[1])
	assert.Equal(t, "--config-file", rec.argv[3])

	// The boot configuration comes from the recipe and the target.
	assert.Equal(t, target.KernelPath, rec.config.BootSource.KernelImagePath)
	assert.Equal(t,
		"console=ttyS0 reboot=k panic=1 pci=off nomodules init=/sbin/init systemd.unified_cgroup_hierarchy=1",
		rec.config.BootSource.BootArgs)
	assert.Equal(t, int64(2), rec.config.MachineConfig.VCPUCount)
	assert.Equal(t, int64(2048), rec.config.MachineConfig.MemSizeMiB)
	assert.Equal(t, uint32(agent.GuestCID), rec.config.Vsock.GuestCID)

	// The root drive is a copy, not the sealed artifact, so a boot cannot
	// change the checksum the sidecar records.
	require.Len(t, rec.config.Drives, 1)
	assert.NotEqual(t, target.ArtifactPath, rec.config.Drives[0].PathOnHost)
	assert.True(t, rec.config.Drives[0].IsRootDevice)
	assert.Equal(t, "template-bytes", string(rec.rootfs))

	// The agent is dialed at the vsock muxer the config declared.
	assert.Equal(t, rec.config.Vsock.UDSPath, rec.uds)
	assert.True(t, vm.wasKilled(), "the probe VM must be terminated")
}

func TestFirecrackerProber_RefusesIncompleteTargets(t *testing.T) {
	target := testTarget(t)
	p := newFakeVM()
	prober, rec := recordingProber(t, p, healthyHello())

	_, err := prober.Probe(context.Background(), BootTarget{KernelPath: target.KernelPath, InitSystem: InitSystemd})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "artifact path")

	_, err = prober.Probe(context.Background(), BootTarget{ArtifactPath: target.ArtifactPath, InitSystem: InitSystemd})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kernel image")

	missing := filepath.Join(t.TempDir(), "nope")
	_, err = prober.Probe(context.Background(), BootTarget{
		ArtifactPath: target.ArtifactPath, KernelPath: missing, InitSystem: InitSystemd,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kernel image")

	assert.Empty(t, rec.argv, "an invalid target must not start a VM")
}

func TestFirecrackerProber_RefusesArtifactThatCannotBeCopied(t *testing.T) {
	target := testTarget(t)
	target.ArtifactPath = filepath.Join(t.TempDir(), "missing.ext4")
	prober, rec := recordingProber(t, newFakeVM(), healthyHello())

	_, err := prober.Probe(context.Background(), target)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open")
	assert.Empty(t, rec.argv)
}

func TestFirecrackerProber_RefusesUnbootableInitSystem(t *testing.T) {
	target := testTarget(t)
	target.InitSystem = InitTini
	prober, rec := recordingProber(t, newFakeVM(), healthyHello())

	_, err := prober.Probe(context.Background(), target)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no /sbin/init")
	assert.Empty(t, rec.argv)
}

func TestFirecrackerProber_LaunchFailure(t *testing.T) {
	prober := &FirecrackerProber{Binary: "firecracker"}
	prober.launch = func(context.Context, []string, io.Writer) (vmProcess, error) {
		return nil, errors.New("no kvm")
	}
	prober.dial = func(context.Context, string, uint32) (helloClient, error) {
		t.Fatal("dial must not run when the VM did not start")
		return nil, nil
	}

	_, err := prober.Probe(context.Background(), testTarget(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "start firecracker")
	assert.Contains(t, err.Error(), "no kvm")
}

func TestFirecrackerProber_EarlyExit(t *testing.T) {
	vm := newFakeVM()
	vm.exitNow(errors.New("boot died"))
	prober, _ := recordingProber(t, vm, healthyHello())
	prober.dial = func(context.Context, string, uint32) (helloClient, error) {
		return nil, errors.New("connection refused")
	}

	_, err := prober.Probe(context.Background(), testTarget(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exited before the guest agent answered")
	assert.Contains(t, err.Error(), "boot died")
}

func TestFirecrackerProber_TimesOut(t *testing.T) {
	prober, rec := recordingProber(t, newFakeVM(), healthyHello())
	prober.BootTimeout = 10 * time.Millisecond
	prober.dial = func(_ context.Context, uds string, _ uint32) (helloClient, error) {
		rec.dialed = append(rec.dialed, uds)
		return nil, errors.New("CONNECT refused")
	}

	_, err := prober.Probe(context.Background(), testTarget(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not answer hello within 10ms")
	assert.Contains(t, err.Error(), "CONNECT refused")
	assert.NotEmpty(t, rec.dialed, "the prober must keep trying until the deadline")
}

func TestFirecrackerProber_StopsWhenTheContextIsCancelled(t *testing.T) {
	prober, _ := recordingProber(t, newFakeVM(), healthyHello())
	prober.dial = func(context.Context, string, uint32) (helloClient, error) {
		return nil, errors.New("refused")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := prober.Probe(ctx, testTarget(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wait for guest agent")
	assert.ErrorIs(t, err, context.Canceled)
}

func TestFirecrackerProber_RetriesUntilTheAgentAnswers(t *testing.T) {
	client := &fakeClient{hello: healthyHello()}
	attempts := 0
	prober, rec := recordingProber(t, newFakeVM(), healthyHello())
	prober.dial = func(_ context.Context, uds string, _ uint32) (helloClient, error) {
		attempts++
		rec.uds = uds
		rec.dialed = append(rec.dialed, uds)
		if attempts < 3 {
			return nil, errors.New("agent still starting")
		}
		return client, nil
	}

	hello, err := prober.Probe(context.Background(), testTarget(t))
	require.NoError(t, err)
	assert.Equal(t, healthyHello().Version, hello.Version)
	assert.Equal(t, 3, attempts)
	assert.NotEmpty(t, rec.uds)
	for _, uds := range rec.dialed {
		assert.Equal(t, rec.uds, uds, "every attempt must dial the same muxer")
	}
	assert.True(t, client.closed, "the probe must close the client it opened")
}

func TestFirecrackerProber_Defaults(t *testing.T) {
	p := NewFirecrackerProber()
	assert.Equal(t, "firecracker", p.binary())
	assert.Equal(t, DefaultFirstBootTimeout, p.timeout())
	assert.Equal(t, int64(2), p.vcpus())
	assert.Equal(t, int64(2048), p.memMiB())
	assert.NotNil(t, p.launcher())
	assert.NotNil(t, p.dialer())

	explicit := &FirecrackerProber{
		Binary:      "/opt/firecracker",
		BootTimeout: time.Minute,
		VCPUs:       4,
		MemoryMiB:   4096,
	}
	assert.Equal(t, "/opt/firecracker", explicit.binary())
	assert.Equal(t, time.Minute, explicit.timeout())
	assert.Equal(t, int64(4), explicit.vcpus())
	assert.Equal(t, int64(4096), explicit.memMiB())
}

// TestStartFirecracker_RunsTheArgv pins the real launcher without a VM: it runs
// the argv it is given, reports its exit on done, and kill terminates a live
// process.
func TestStartFirecracker_RunsTheArgv(t *testing.T) {
	var out strings.Builder
	proc, err := startFirecracker(context.Background(), []string{"/bin/sh", "-c", "echo booted"}, &out)
	require.NoError(t, err)
	select {
	case err := <-proc.done():
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("launcher did not report the exit")
	}
	proc.kill() // safe after the process already exited
	assert.Equal(t, "booted\n", out.String())

	_, err = startFirecracker(context.Background(), []string{"/nonexistent/firecracker"}, io.Discard)
	require.Error(t, err)

	live, err := startFirecracker(context.Background(), []string{"/bin/sleep", "30"}, io.Discard)
	require.NoError(t, err)
	live.kill()
	select {
	case <-live.done():
	case <-time.After(10 * time.Second):
		t.Fatal("kill did not end the process")
	}
}

// TestDialAgent_SpeaksTheMuxerAndNeedsNoToken proves the probe dials the real
// vsock handshake with no credential: the fake muxer accepts CONNECT, then
// serves the agent protocol, and the hello succeeds.
func TestDialAgent_SpeaksTheMuxerAndNeedsNoToken(t *testing.T) {
	dir := t.TempDir()
	uds := filepath.Join(dir, "vsock.sock")
	ln, err := net.Listen("unix", uds)
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		line, readErr := readHandshakeLine(conn)
		if readErr != nil || line != "CONNECT 52" {
			return
		}
		if _, writeErr := conn.Write([]byte("OK 1024\n")); writeErr != nil {
			return
		}
		_ = agent.ServeConn(conn)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := dialAgent(ctx, uds, agent.DefaultPort)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	hello := client.Hello()
	assert.Equal(t, agent.Version, hello.Version)
	assert.True(t, hello.Supports(agent.CapExec))
	assert.NotEmpty(t, hello.BootID, "the guest must report a boot id")

	_, err = dialAgent(ctx, filepath.Join(dir, "missing.sock"), agent.DefaultPort)
	require.Error(t, err)
}

// readHandshakeLine reads the muxer's newline-terminated CONNECT request.
func readHandshakeLine(conn net.Conn) (string, error) {
	var line []byte
	buf := make([]byte, 1)
	for len(line) < 64 {
		if _, err := conn.Read(buf); err != nil {
			return "", err
		}
		if buf[0] == '\n' {
			return string(line), nil
		}
		line = append(line, buf[0])
	}
	return string(line), nil
}

func TestProbeLogTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "serial.log")
	long := strings.Repeat("x", 9000) + "TAIL"
	require.NoError(t, os.WriteFile(path, []byte(long), 0o644))

	assert.Equal(t, long[len(long)-8192:], probeLogTail(path))
	assert.Empty(t, probeLogTail(filepath.Join(dir, "missing.log")))
}
