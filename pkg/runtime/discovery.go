package runtime

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// VMSocketSuffix is the suffix of a Firecracker API socket.
	VMSocketSuffix = ".sock"
	// ConsoleSocketSuffix is the suffix of a VM's console socket, which is not
	// a Firecracker API socket and must not be listed as a VM.
	ConsoleSocketSuffix = ".console.sock"

	defaultProbeTimeout = 500 * time.Millisecond
)

// RunningVM is a Firecracker VM discovered from its API socket.
type RunningVM struct {
	ID         string
	SocketPath string
	Started    time.Time
}

// IsVMSocketAlive reports whether a Firecracker API socket answers a
// machine-config request. It is used to filter out stale socket files left
// behind by VMs that are no longer running.
func IsVMSocketAlive(socketPath string, timeout time.Duration) bool {
	if timeout <= 0 {
		timeout = defaultProbeTimeout
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}
	defer transport.CloseIdleConnections()

	client := &http.Client{Transport: transport, Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/machine-config", nil)
	if err != nil {
		return false
	}

	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	return resp.StatusCode == http.StatusOK
}

// DiscoverRunningVMs returns the VMs whose API sockets under socketDir answer a
// liveness probe. A missing directory yields an empty list, not an error.
func DiscoverRunningVMs(socketDir string) ([]RunningVM, error) {
	entries, err := os.ReadDir(socketDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read VM socket directory %s: %w", socketDir, err)
	}

	vms := make([]RunningVM, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, VMSocketSuffix) || strings.HasSuffix(name, ConsoleSocketSuffix) {
			continue
		}

		socketPath := filepath.Join(socketDir, name)
		if !IsVMSocketAlive(socketPath, defaultProbeTimeout) {
			continue
		}

		vm := RunningVM{
			ID:         strings.TrimSuffix(name, VMSocketSuffix),
			SocketPath: socketPath,
		}
		if info, statErr := entry.Info(); statErr == nil {
			vm.Started = info.ModTime()
		}
		vms = append(vms, vm)
	}

	sort.Slice(vms, func(i, j int) bool { return vms[i].ID < vms[j].ID })
	return vms, nil
}

// MergeVMs merges persisted CLI state entries with VMs discovered on the host.
// State entries win on an ID conflict (they carry richer metadata). The result
// is sorted by ID and shares no backing storage with its inputs.
func MergeVMs(states []*VMState, running []RunningVM) []*VMState {
	merged := make(map[string]*VMState, len(states)+len(running))

	for _, state := range states {
		if state == nil || state.ID == "" {
			continue
		}
		cp := *state
		cp.IPAddresses = append([]string(nil), state.IPAddresses...)
		cp.Command = append([]string(nil), state.Command...)
		merged[cp.ID] = &cp
	}

	for _, vm := range running {
		if _, exists := merged[vm.ID]; exists {
			continue
		}
		merged[vm.ID] = &VMState{
			ID:         vm.ID,
			Status:     "running",
			SocketPath: vm.SocketPath,
			StartTime:  vm.Started,
		}
	}

	result := make([]*VMState, 0, len(merged))
	for _, vm := range merged {
		result = append(result, vm)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

// FindFirecrackerPIDs returns a map of task ID to host PID for the Firecracker
// processes on this host. On systems without /proc (e.g. macOS) it returns an
// empty map.
func FindFirecrackerPIDs() map[string]int {
	result := make(map[string]int)

	const procDir = "/proc"
	entries, err := os.ReadDir(procDir)
	if err != nil {
		return result
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(procDir, entry.Name(), "cmdline"))
		if err != nil {
			continue
		}
		if id, ok := firecrackerTaskID(string(data)); ok {
			if _, dup := result[id]; !dup {
				result[id] = pid
			}
		}
	}

	return result
}

// FindFirecrackerPID returns the host PID of the Firecracker process for a task
// ID, or 0 if it cannot be determined.
func FindFirecrackerPID(taskID string) int {
	if taskID == "" {
		return 0
	}
	return FindFirecrackerPIDs()[taskID]
}

// firecrackerTaskID extracts the --id value from a Firecracker process's
// /proc/<pid>/cmdline, reporting false for non-Firecracker processes.
func firecrackerTaskID(cmdline string) (string, bool) {
	args := strings.Split(cmdline, "\x00")

	isFirecracker := false
	id := ""
	for i, arg := range args {
		if arg == "--id" && i+1 < len(args) {
			id = args[i+1]
		}
		if strings.Contains(filepath.Base(arg), "firecracker") {
			isFirecracker = true
		}
	}

	if isFirecracker && id != "" {
		return id, true
	}
	return "", false
}
