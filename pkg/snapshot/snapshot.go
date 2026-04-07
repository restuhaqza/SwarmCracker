// Package snapshot provides VM snapshot and restore functionality for SwarmCracker.
//
// It manages the lifecycle of Firecracker microVM snapshots, including creation,
// restoration, listing, deletion, and cleanup of old snapshots.
//
// The Firecracker snapshot API works as follows:
//  1. PUT /snapshot/create — snapshot a running VM (pauses internally)
//  2. Start firecracker --snapshot <state-file> — boot from snapshot
//  3. PUT /snapshot/load — load memory backend into restored VM
package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// SnapshotInfo holds metadata about a VM snapshot.
type SnapshotInfo struct {
	// ID is the unique identifier for this snapshot.
	ID string `json:"id"`

	// TaskID is the SwarmKit task ID this snapshot was created from.
	TaskID string `json:"task_id"`

	// ServiceID is the SwarmKit service ID.
	ServiceID string `json:"service_id"`

	// NodeID is the node where the VM was running.
	NodeID string `json:"node_id"`

	// CreatedAt is when the snapshot was taken.
	CreatedAt time.Time `json:"created_at"`

	// MemoryPath is the absolute path to the memory snapshot file.
	MemoryPath string `json:"memory_path"`

	// StatePath is the absolute path to the VM state snapshot file.
	StatePath string `json:"state_path"`

	// SizeBytes is the total size of the snapshot (memory + state).
	SizeBytes int64 `json:"size_bytes"`

	// VCPUCount is the number of vCPUs the VM had at snapshot time.
	VCPUCount int `json:"vcpu_count"`

	// MemoryMB is the memory size in MiB at snapshot time.
	MemoryMB int `json:"memory_mb"`

	// RootfsPath is the path to the rootfs image the VM was using.
	RootfsPath string `json:"rootfs_path"`

	// Checksum is the SHA-256 checksum of the state file for integrity verification.
	Checksum string `json:"checksum"`

	// Metadata holds arbitrary key-value metadata.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// SnapshotFilter filters snapshot listings.
type SnapshotFilter struct {
	TaskID    string
	ServiceID string
	NodeID    string
	Since     time.Time // Only snapshots created after this time
	Before    time.Time // Only snapshots created before this time
}

// SnapshotConfig holds snapshot configuration.
type SnapshotConfig struct {
	Enabled      bool          `yaml:"enabled"`
	SnapshotDir  string        `yaml:"snapshot_dir"`
	MaxSnapshots int           `yaml:"max_snapshots"` // Per service, 0 = unlimited
	MaxAge       time.Duration `yaml:"max_age"`       // 0 = unlimited
	AutoSnapshot bool          `yaml:"auto_snapshot"` // Auto-snapshot on successful start
	Compress     bool          `yaml:"compress"`      // Gzip compress memory snapshots
}

// DefaultSnapshotConfig returns sensible defaults.
func DefaultSnapshotConfig() SnapshotConfig {
	return SnapshotConfig{
		Enabled:      false,
		SnapshotDir:  "/var/lib/firecracker/snapshots",
		MaxSnapshots: 3,
		MaxAge:       168 * time.Hour, // 7 days
		AutoSnapshot: false,
		Compress:     false,
	}
}

// SetDefaults fills in zero-value fields with sensible defaults.
func (c *SnapshotConfig) SetDefaults() {
	if c.SnapshotDir == "" {
		c.SnapshotDir = "/var/lib/firecracker/snapshots"
	}
	if c.MaxSnapshots == 0 {
		c.MaxSnapshots = 3
	}
	if c.MaxAge == 0 {
		c.MaxAge = 168 * time.Hour
	}
}

// CreateOptions holds optional parameters for snapshot creation.
type CreateOptions struct {
	ServiceID  string
	NodeID     string
	VCPUCount  int
	MemoryMB   int
	RootfsPath string
	Metadata   map[string]string
}

// Manager manages VM snapshot lifecycle.
type Manager struct {
	config SnapshotConfig
	mu     sync.Mutex
}

// NewManager creates a new snapshot Manager.
func NewManager(config SnapshotConfig) (*Manager, error) {
	if config.SnapshotDir == "" {
		config.SnapshotDir = DefaultSnapshotConfig().SnapshotDir
	}

	if err := os.MkdirAll(config.SnapshotDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create snapshot directory %s: %w", config.SnapshotDir, err)
	}

	return &Manager{config: config}, nil
}

// CreateSnapshot snapshots a running VM via the Firecracker API.
//
// It calls PUT /snapshot/create on the Firecracker Unix socket, which
// internally pauses the VM, writes state and memory files, then exits.
// The snapshot files are then moved to the managed snapshot directory.
func (m *Manager) CreateSnapshot(
	ctx context.Context,
	taskID, socketPath string,
	opts CreateOptions,
) (*SnapshotInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if socketPath == "" {
		return nil, fmt.Errorf("socket path is required")
	}

	// Verify socket exists
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("firecracker socket not found: %s", socketPath)
	}

	// Create a staging directory for the snapshot
	stagingDir, err := os.MkdirTemp(m.config.SnapshotDir, "snapshot-staging-")
	if err != nil {
		return nil, fmt.Errorf("failed to create staging directory: %w", err)
	}
	defer os.RemoveAll(stagingDir)

	statePath := filepath.Join(stagingDir, "vm.state")
	memoryPath := filepath.Join(stagingDir, "vm.mem")

	log.Info().
		Str("task_id", taskID).
		Str("socket", socketPath).
		Msg("Creating VM snapshot")

	// Call Firecracker snapshot API
	if err := callSnapshotCreate(ctx, socketPath, statePath, memoryPath); err != nil {
		return nil, fmt.Errorf("failed to create snapshot via Firecracker API: %w", err)
	}

	// Generate snapshot ID and create final directory
	snapshotID := generateSnapshotID(taskID)
	snapshotDir := filepath.Join(m.config.SnapshotDir, snapshotID)
	if err := os.MkdirAll(snapshotDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create snapshot directory: %w", err)
	}

	// Move files to final location
	finalStatePath := filepath.Join(snapshotDir, "vm.state")
	finalMemoryPath := filepath.Join(snapshotDir, "vm.mem")

	if err := os.Rename(statePath, finalStatePath); err != nil {
		os.RemoveAll(snapshotDir)
		return nil, fmt.Errorf("failed to move state file: %w", err)
	}
	if err := os.Rename(memoryPath, finalMemoryPath); err != nil {
		os.RemoveAll(snapshotDir)
		return nil, fmt.Errorf("failed to move memory file: %w", err)
	}

	// Calculate sizes and checksum
	stateSize, err := fileSize(finalStatePath)
	if err != nil {
		os.RemoveAll(snapshotDir)
		return nil, fmt.Errorf("failed to stat state file: %w", err)
	}

	memorySize, err := fileSize(finalMemoryPath)
	if err != nil {
		os.RemoveAll(snapshotDir)
		return nil, fmt.Errorf("failed to stat memory file: %w", err)
	}

	checksum, err := sha256File(finalStatePath)
	if err != nil {
		os.RemoveAll(snapshotDir)
		return nil, fmt.Errorf("failed to checksum state file: %w", err)
	}

	info := &SnapshotInfo{
		ID:         snapshotID,
		TaskID:     taskID,
		ServiceID:  opts.ServiceID,
		NodeID:     opts.NodeID,
		CreatedAt:  time.Now().UTC(),
		MemoryPath: finalMemoryPath,
		StatePath:  finalStatePath,
		SizeBytes:  stateSize + memorySize,
		VCPUCount:  opts.VCPUCount,
		MemoryMB:   opts.MemoryMB,
		RootfsPath: opts.RootfsPath,
		Checksum:   checksum,
		Metadata:   opts.Metadata,
	}

	// Save metadata
	if err := saveMetadata(snapshotDir, info); err != nil {
		os.RemoveAll(snapshotDir)
		return nil, fmt.Errorf("failed to save snapshot metadata: %w", err)
	}

	// Enforce max snapshots per service
	if m.config.MaxSnapshots > 0 && info.ServiceID != "" {
		m.enforceMaxSnapshots(info.ServiceID)
	}

	log.Info().
		Str("snapshot_id", snapshotID).
		Str("task_id", taskID).
		Int64("size_bytes", info.SizeBytes).
		Msg("Snapshot created successfully")

	return info, nil
}

// RestoreFromSnapshot restores a VM from a snapshot.
//
// This starts a new Firecracker process with --snapshot pointing to the
// state file, then loads the memory backend via PUT /snapshot/load.
func (m *Manager) RestoreFromSnapshot(
	ctx context.Context,
	info *SnapshotInfo,
	socketPath string,
) error {
	if info == nil {
		return fmt.Errorf("snapshot info is required")
	}

	// Verify snapshot files exist
	if _, err := os.Stat(info.StatePath); os.IsNotExist(err) {
		return fmt.Errorf("state file not found: %s", info.StatePath)
	}
	if _, err := os.Stat(info.MemoryPath); os.IsNotExist(err) {
		return fmt.Errorf("memory file not found: %s", info.MemoryPath)
	}

	// Verify checksum
	if info.Checksum != "" {
		checksum, err := sha256File(info.StatePath)
		if err != nil {
			return fmt.Errorf("failed to verify state checksum: %w", err)
		}
		if checksum != info.Checksum {
			return fmt.Errorf("state file checksum mismatch: expected %s, got %s", info.Checksum, checksum)
		}
	}

	log.Info().
		Str("snapshot_id", info.ID).
		Str("task_id", info.TaskID).
		Str("socket", socketPath).
		Msg("Restoring VM from snapshot")

	// Clean up existing socket
	os.Remove(socketPath)

	// Start Firecracker with --snapshot
	fcBinary, err := exec.LookPath("firecracker")
	if err != nil {
		return fmt.Errorf("firecracker binary not found in PATH: %w", err)
	}

	cmd := exec.Command(fcBinary, "--api-sock", socketPath, "--snapshot", info.StatePath)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start firecracker for restore: %w", err)
	}

	// Ensure cleanup on failure
	startErr := false
	defer func() {
		if startErr {
			cmd.Process.Kill()
			os.Remove(socketPath)
		}
	}()

	// Wait for API server
	if err := waitForSocket(socketPath, 10*time.Second); err != nil {
		startErr = true
		return fmt.Errorf("firecracker API not ready after restore: %w", err)
	}

	// Load memory via snapshot/load API
	if err := callSnapshotLoad(ctx, socketPath, info.StatePath, info.MemoryPath); err != nil {
		startErr = true
		return fmt.Errorf("failed to load snapshot memory: %w", err)
	}

	// Resume the instance
	if err := callInstanceStart(ctx, socketPath); err != nil {
		startErr = true
		return fmt.Errorf("failed to resume restored instance: %w", err)
	}

	log.Info().
		Str("snapshot_id", info.ID).
		Msg("VM restored from snapshot successfully")

	return nil
}

// ListSnapshots lists snapshots matching the given filter.
func (m *Manager) ListSnapshots(filter SnapshotFilter) ([]*SnapshotInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.listSnapshotsUnlocked(filter)
}

// listSnapshotsUnlocked lists snapshots without acquiring the lock.
// Caller must hold m.mu.
func (m *Manager) listSnapshotsUnlocked(filter SnapshotFilter) ([]*SnapshotInfo, error) {
	entries, err := os.ReadDir(m.config.SnapshotDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read snapshot directory: %w", err)
	}

	var snapshots []*SnapshotInfo

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		snapshotDir := filepath.Join(m.config.SnapshotDir, entry.Name())
		info, err := loadMetadata(snapshotDir)
		if err != nil {
			log.Debug().Err(err).Str("dir", snapshotDir).Msg("Skipping invalid snapshot directory")
			continue
		}

		if matchesFilter(info, filter) {
			snapshots = append(snapshots, info)
		}
	}

	// Sort by creation time, newest first
	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].CreatedAt.After(snapshots[j].CreatedAt)
	})

	return snapshots, nil
}

// DeleteSnapshot removes a snapshot by ID.
func (m *Manager) DeleteSnapshot(ctx context.Context, snapshotID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	snapshotDir := filepath.Join(m.config.SnapshotDir, snapshotID)

	if _, err := os.Stat(snapshotDir); os.IsNotExist(err) {
		return fmt.Errorf("snapshot not found: %s", snapshotID)
	}

	log.Info().Str("snapshot_id", snapshotID).Msg("Deleting snapshot")

	if err := os.RemoveAll(snapshotDir); err != nil {
		return fmt.Errorf("failed to delete snapshot: %w", err)
	}

	return nil
}

// CleanupOldSnapshots removes snapshots older than maxAge.
// Returns the number of snapshots removed and bytes freed.
func (m *Manager) CleanupOldSnapshots(ctx context.Context, maxAge time.Duration) (int, int64, error) {
	if maxAge <= 0 {
		maxAge = m.config.MaxAge
	}
	if maxAge <= 0 {
		return 0, 0, nil
	}

	snapshots, err := m.ListSnapshots(SnapshotFilter{})
	if err != nil {
		return 0, 0, err
	}

	cutoff := time.Now().UTC().Add(-maxAge)
	var removed int
	var freed int64

	for _, snap := range snapshots {
		if snap.CreatedAt.Before(cutoff) {
			if err := m.DeleteSnapshot(ctx, snap.ID); err != nil {
				log.Warn().Err(err).Str("snapshot_id", snap.ID).Msg("Failed to delete old snapshot")
				continue
			}
			removed++
			freed += snap.SizeBytes
		}
	}

	if removed > 0 {
		log.Info().
			Int("removed", removed).
			Int64("bytes_freed", freed).
			Str("max_age", maxAge.String()).
			Msg("Cleaned up old snapshots")
	}

	return removed, freed, nil
}

// enforceMaxSnapshots removes the oldest snapshots for a service when the limit is exceeded.
// Caller must hold m.mu.
func (m *Manager) enforceMaxSnapshots(serviceID string) {
	snapshots, err := m.listSnapshotsUnlocked(SnapshotFilter{ServiceID: serviceID})
	if err != nil {
		log.Warn().Err(err).Str("service_id", serviceID).Msg("Failed to list snapshots for cleanup")
		return
	}

	max := m.config.MaxSnapshots
	if max <= 0 || len(snapshots) <= max {
		return
	}

	// Remove oldest snapshots (list is sorted newest first)
	toRemove := len(snapshots) - max
	var freed int64
	for i := len(snapshots) - 1; i >= len(snapshots)-toRemove; i-- {
		snap := snapshots[i]
		snapshotDir := filepath.Join(m.config.SnapshotDir, snap.ID)
		if err := os.RemoveAll(snapshotDir); err != nil {
			log.Warn().Err(err).Str("snapshot_id", snap.ID).Msg("Failed to remove old snapshot")
			continue
		}
		freed += snap.SizeBytes
	}

	log.Info().
		Str("service_id", serviceID).
		Int("removed", toRemove).
		Int64("bytes_freed", freed).
		Msg("Enforced max snapshot limit")
}

// --- Firecracker API calls ---

// pauseVM pauses the VM via PATCH /vm endpoint (Firecracker v1.14.0+).
func pauseVM(ctx context.Context, socketPath string) error {
	payload := map[string]interface{}{
		"state": "Paused",
	}
	return patchFirecrackerAPI(ctx, socketPath, "/vm", payload)
}

// resumeVM resumes the VM via PATCH /vm endpoint (Firecracker v1.14.0+).
func resumeVM(ctx context.Context, socketPath string) error {
	payload := map[string]interface{}{
		"state": "Resumed",
	}
	return patchFirecrackerAPI(ctx, socketPath, "/vm", payload)
}

// callSnapshotCreate calls PUT /snapshot/create on the Firecracker API.
// For Firecracker v1.14.0+, the API expects snapshot_type, snapshot_path, and mem_file_path.
// The VM must be paused before calling this.
func callSnapshotCreate(ctx context.Context, socketPath, statePath, memoryPath string) error {
	// Pause VM first (required in v1.14.0+)
	if err := pauseVM(ctx, socketPath); err != nil {
		return fmt.Errorf("failed to pause VM: %w", err)
	}

	payload := map[string]interface{}{
		"snapshot_type": "Full",
		"snapshot_path": statePath,
		"mem_file_path": memoryPath,
	}

	return putFirecrackerAPI(ctx, socketPath, "/snapshot/create", payload)
}

// callSnapshotLoad calls PUT /snapshot/load on the Firecracker API.
// For Firecracker v1.14.0+, the API expects snapshot_path and mem_file_path.
// The resume_vm flag automatically resumes the VM after loading.
func callSnapshotLoad(ctx context.Context, socketPath, statePath, memoryPath string) error {
	payload := map[string]interface{}{
		"snapshot_path": statePath,
		"mem_file_path": memoryPath,
		"resume_vm":     true,
	}

	return putFirecrackerAPI(ctx, socketPath, "/snapshot/load", payload)
}

// callInstanceStart calls PUT /actions with InstanceStart.
func callInstanceStart(ctx context.Context, socketPath string) error {
	payload := map[string]interface{}{
		"action_type": "InstanceStart",
	}

	return putFirecrackerAPI(ctx, socketPath, "/actions", payload)
}

// putFirecrackerAPI sends a PUT request to the Firecracker API over a Unix socket.
func putFirecrackerAPI(ctx context.Context, socketPath, apiPath string, payload interface{}) error {
	client := newUnixHTTPClient(socketPath, 30*time.Second)

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		"http://localhost"+apiPath,
		strings.NewReader(string(body)),
	)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d for %s: %s", resp.StatusCode, apiPath, string(respBody))
	}

	return nil
}

// patchFirecrackerAPI sends a PATCH request to the Firecracker API over a Unix socket.
func patchFirecrackerAPI(ctx context.Context, socketPath, apiPath string, payload interface{}) error {
	client := newUnixHTTPClient(socketPath, 30*time.Second)

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPatch,
		"http://localhost"+apiPath,
		strings.NewReader(string(body)),
	)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d for %s: %s", resp.StatusCode, apiPath, string(respBody))
	}

	return nil
}

// --- Metadata persistence ---

const metadataFile = "metadata.json"

func saveMetadata(dir string, info *SnapshotInfo) error {
	path := filepath.Join(dir, metadataFile)
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func loadMetadata(dir string) (*SnapshotInfo, error) {
	path := filepath.Join(dir, metadataFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var info SnapshotInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}

	// Resolve paths relative to snapshot directory
	if !filepath.IsAbs(info.MemoryPath) {
		info.MemoryPath = filepath.Join(dir, "vm.mem")
	}
	if !filepath.IsAbs(info.StatePath) {
		info.StatePath = filepath.Join(dir, "vm.state")
	}

	return &info, nil
}

// --- Helpers ---

func matchesFilter(info *SnapshotInfo, filter SnapshotFilter) bool {
	if filter.TaskID != "" && info.TaskID != filter.TaskID {
		return false
	}
	if filter.ServiceID != "" && info.ServiceID != filter.ServiceID {
		return false
	}
	if filter.NodeID != "" && info.NodeID != filter.NodeID {
		return false
	}
	if !filter.Since.IsZero() && info.CreatedAt.Before(filter.Since) {
		return false
	}
	if !filter.Before.IsZero() && info.CreatedAt.After(filter.Before) {
		return false
	}
	return true
}

func generateSnapshotID(taskID string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s-%d", taskID, time.Now().UnixNano())))
	return fmt.Sprintf("snap-%s", hex.EncodeToString(h[:])[:16])
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func newUnixHTTPClient(socketPath string, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", socketPath)
			},
		},
	}
}

func waitForSocket(socketPath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := newUnixHTTPClient(socketPath, 100*time.Millisecond)

	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			resp, err := client.Get("http://localhost/")
			if err == nil {
				resp.Body.Close()
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	return fmt.Errorf("socket not ready within %s", timeout)
}
