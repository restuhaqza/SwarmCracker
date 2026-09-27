package runtime

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shortSocketDir returns a short temporary directory. Unix socket paths are
// limited (~104 bytes on macOS), which t.TempDir() can exceed.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sc-rt-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// serveFakeVM starts an HTTP server on a Unix socket that answers with the
// given status for any request, emulating the Firecracker API.
func serveFakeVM(t *testing.T, socketPath string, status int) {
	t.Helper()
	ln, err := net.Listen("unix", socketPath)
	require.NoError(t, err)

	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"vcpu_count":1,"mem_size_mib":512}`))
	})}
	go func() { _ = srv.Serve(ln) }()

	t.Cleanup(func() {
		_ = srv.Close()
		_ = ln.Close()
	})
}

func TestIsVMSocketAlive(t *testing.T) {
	dir := shortSocketDir(t)

	live := filepath.Join(dir, "live.sock")
	serveFakeVM(t, live, http.StatusOK)
	assert.True(t, IsVMSocketAlive(live, time.Second), "live socket should probe as alive")

	// A socket that only answers 500 is not healthy.
	unhealthy := filepath.Join(dir, "unhealthy.sock")
	serveFakeVM(t, unhealthy, http.StatusInternalServerError)
	assert.False(t, IsVMSocketAlive(unhealthy, time.Second), "non-200 socket should probe as dead")

	// A plain file (stale socket path) is not a live VM.
	stale := filepath.Join(dir, "stale.sock")
	require.NoError(t, os.WriteFile(stale, nil, 0o600))
	assert.False(t, IsVMSocketAlive(stale, 200*time.Millisecond))

	assert.False(t, IsVMSocketAlive(filepath.Join(dir, "missing.sock"), 200*time.Millisecond))
}

func TestDiscoverRunningVMs(t *testing.T) {
	dir := shortSocketDir(t)

	serveFakeVM(t, filepath.Join(dir, "task-live.sock"), http.StatusOK)
	// A live console socket must not be reported as a VM.
	serveFakeVM(t, filepath.Join(dir, "task-live.console.sock"), http.StatusOK)
	// A stale socket file with no listener must not be reported.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task-stale.sock"), nil, 0o600))
	// Non-socket files are ignored.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "random.txt"), nil, 0o600))

	vms, err := DiscoverRunningVMs(dir)
	require.NoError(t, err)
	require.Len(t, vms, 1)
	assert.Equal(t, "task-live", vms[0].ID)
	assert.Equal(t, filepath.Join(dir, "task-live.sock"), vms[0].SocketPath)
	assert.False(t, vms[0].Started.IsZero(), "start time should come from the socket mtime")
}

func TestDiscoverRunningVMsMissingDir(t *testing.T) {
	vms, err := DiscoverRunningVMs(filepath.Join(shortSocketDir(t), "nope"))
	require.NoError(t, err)
	assert.Empty(t, vms)
}

func TestMergeVMs(t *testing.T) {
	started := time.Now().Add(-time.Minute).Truncate(time.Second)

	states := []*VMState{
		{ID: "b", Status: "stopped", Image: "img-b"},
		{ID: "a", Status: "running", Image: "img-a", IPAddresses: []string{"10.0.0.1"}, Command: []string{"sleep"}},
		nil,
		{ID: ""},
		{ID: "   "},
	}
	running := []RunningVM{
		{ID: "c", SocketPath: "/s/c.sock", Started: started},
		{ID: "a", SocketPath: "/s/a.sock"}, // conflict: state entry wins
	}

	merged := MergeVMs(states, running)
	require.Len(t, merged, 3)
	assert.Equal(t, []string{"a", "b", "c"}, []string{merged[0].ID, merged[1].ID, merged[2].ID})

	// State entry for "a" is preserved (not replaced by the discovered VM).
	assert.Equal(t, "running", merged[0].Status)
	assert.Equal(t, "img-a", merged[0].Image)
	assert.Empty(t, merged[0].SocketPath)

	// Discovered VM "c" is synthesized as running.
	assert.Equal(t, "running", merged[2].Status)
	assert.Equal(t, "/s/c.sock", merged[2].SocketPath)
	assert.Equal(t, started, merged[2].StartTime)

	// The result must not alias the input state.
	merged[0].IPAddresses[0] = "changed"
	merged[0].Command[0] = "changed"
	assert.Equal(t, "10.0.0.1", states[1].IPAddresses[0])
	assert.Equal(t, "sleep", states[1].Command[0])
}

func TestMergeVMsEmpty(t *testing.T) {
	assert.Empty(t, MergeVMs(nil, nil))
}

func TestFirecrackerTaskID(t *testing.T) {
	cmdline := "/usr/local/bin/firecracker\x00--api-sock\x00/var/run/firecracker/t.sock\x00--id\x00task-1\x00"
	id, ok := firecrackerTaskID(cmdline)
	assert.True(t, ok)
	assert.Equal(t, "task-1", id)

	_, ok = firecrackerTaskID("/bin/sleep\x0010000\x00")
	assert.False(t, ok)

	_, ok = firecrackerTaskID("/usr/local/bin/firecracker\x00--api-sock\x00/x.sock\x00")
	assert.False(t, ok, "firecracker without --id is not matched")
}

func TestFindFirecrackerPIDMissing(t *testing.T) {
	assert.Zero(t, FindFirecrackerPID(""))
	assert.Zero(t, FindFirecrackerPID("no-such-task-id-xyz"))
}
