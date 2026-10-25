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
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/config"
	"github.com/rs/zerolog/log"
)

// Sentinel errors classify a store failure for callers that must answer a
// stable code rather than inspect an error string. The messages are the leading
// phrase of the wrapped failure, so wrapping with %w both keeps the text
// readable and makes errors.Is work.
var (
	// ErrNotFound means the store holds no snapshot with that ID.
	ErrNotFound = errors.New("snapshot not found")
	// ErrSocketMissing means the task's Firecracker API socket is gone, so the
	// VM a snapshot would need is no longer running.
	ErrSocketMissing = errors.New("firecracker socket not found")
	// ErrSnapshotIncomplete means a snapshot's files (state, memory or rootfs)
	// are missing or unusable.
	ErrSnapshotIncomplete = errors.New("snapshot is incomplete")
	// ErrChecksumMismatch means the state file no longer matches the checksum
	// recorded at snapshot time.
	ErrChecksumMismatch = errors.New("state file checksum mismatch")
	// ErrInvalidSnapshotID means the ID is not one safe path segment in the
	// shape this package generates. It is checked before any filesystem join, so
	// ".", "..", a separator or a Windows-style separator can never address a
	// directory outside the snapshot root.
	ErrInvalidSnapshotID = errors.New("invalid snapshot ID")
)

// snapshotIDPattern is the exact shape generateSnapshotID produces. It is
// deliberately narrower than the wire rule in pkg/api/v1: the node is the store,
// so only an ID this package could have written is accepted here.
var snapshotIDPattern = regexp.MustCompile(`^snap-[0-9a-f]{16}$`)

// ValidSnapshotID reports whether id is a well-formed snapshot ID: exactly the
// shape generateSnapshotID writes, one safe path segment. It is the single
// validator for the package and for callers that reject an ID before any
// filesystem join (the CLI does, so a usage error never loads configuration).
func ValidSnapshotID(id string) bool {
	return snapshotIDPattern.MatchString(id)
}

// SnapshotInfo holds metadata about a VM snapshot.
//
//nolint:revive // Stuttering name is acceptable here for package clarity
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

	// SizeBytes is the total size of the snapshot (memory + state + disk copy).
	SizeBytes int64 `json:"size_bytes"`

	// VCPUCount is the number of vCPUs the VM had at snapshot time.
	VCPUCount int `json:"vcpu_count"`

	// MemoryMB is the memory size in MiB at snapshot time.
	MemoryMB int `json:"memory_mb"`

	// RootfsPath is the path to the rootfs image the VM was using.
	RootfsPath string `json:"rootfs_path"`

	// DiskPath is the snapshot-owned copy of the task disk, normally
	// <SnapshotDir>/<id>/rootfs.ext4. A Firecracker snapshot captures memory and
	// device state only, so without this copy a restore depends on the executor
	// keeping the original disk. Empty for snapshots taken before disk copies
	// existed; those restore exactly as they did.
	DiskPath string `json:"disk_path,omitempty"`

	// Checksum is the SHA-256 checksum of the state file for integrity verification.
	Checksum string `json:"checksum"`

	// Metadata holds arbitrary key-value metadata.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// SnapshotFilter filters snapshot listings.
//
//nolint:revive // Stuttering name is acceptable here for package clarity
type SnapshotFilter struct {
	TaskID    string
	ServiceID string
	NodeID    string
	Since     time.Time // Only snapshots created after this time
	Before    time.Time // Only snapshots created before this time
}

// SnapshotConfig holds snapshot configuration.
//
//nolint:revive // Stuttering name is acceptable here for package clarity
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
		SnapshotDir:  config.DefaultSnapshotDir,
		MaxSnapshots: 3,
		MaxAge:       168 * time.Hour, // 7 days
		AutoSnapshot: false,
		Compress:     false,
	}
}

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
	config            SnapshotConfig
	mu                sync.Mutex
	restoredProcesses map[string]*os.Process // taskID -> process for restored VMs
}

// TrackRestoredProcess registers a restored VM process for lifecycle tracking.
func (m *Manager) TrackRestoredProcess(taskID string, proc *os.Process) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.restoredProcesses == nil {
		m.restoredProcesses = make(map[string]*os.Process)
	}
	m.restoredProcesses[taskID] = proc
}
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
		return nil, fmt.Errorf("%w: %s", ErrSocketMissing, socketPath)
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

	// Reserve a final directory with a fresh ID. The Firecracker snapshot already
	// exists in the staging directory, so this is the point of no return: a
	// directory that is already there belongs to another snapshot and is never
	// reused.
	snapshotID, snapshotDir, err := m.reserveSnapshotDir()
	if err != nil {
		return nil, err
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

	// A Firecracker snapshot is memory and device state: the block device still
	// points at the task disk, which the executor deletes with the task. Copy it
	// so the snapshot is a restore point on its own. Failure means the snapshot
	// would be unusable after cleanup, so no snapshot is kept at all.
	var diskPath string
	var diskSize int64
	if opts.RootfsPath != "" {
		diskPath = filepath.Join(snapshotDir, diskFileName)
		if err := copyFile(opts.RootfsPath, diskPath); err != nil {
			os.RemoveAll(snapshotDir)
			return nil, fmt.Errorf("failed to copy task disk %s into the snapshot: %w", opts.RootfsPath, err)
		}
		diskSize, err = fileSize(diskPath)
		if err != nil {
			os.RemoveAll(snapshotDir)
			return nil, fmt.Errorf("failed to stat snapshot disk copy: %w", err)
		}
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
		SizeBytes:  stateSize + memorySize + diskSize,
		VCPUCount:  opts.VCPUCount,
		MemoryMB:   opts.MemoryMB,
		RootfsPath: opts.RootfsPath,
		DiskPath:   diskPath,
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

// reserveSnapshotDir creates and returns a snapshot ID and its directory. The
// directory is created with mkdir, not mkdirall: an ID that already has a
// directory is retried with a new ID rather than reused, because overwriting it
// would silently destroy a valid restore point. Caller must hold m.mu.
func (m *Manager) reserveSnapshotDir() (id string, dir string, err error) {
	for attempt := 0; attempt < snapshotIDAttempts; attempt++ {
		id = generateSnapshotID()
		dir = filepath.Join(m.config.SnapshotDir, id)
		if mkErr := os.Mkdir(dir, 0o755); mkErr != nil {
			if errors.Is(mkErr, os.ErrExist) {
				continue
			}
			return "", "", fmt.Errorf("failed to create snapshot directory: %w", mkErr)
		}
		return id, dir, nil
	}
	return "", "", fmt.Errorf("failed to allocate a unique snapshot directory after %d attempts", snapshotIDAttempts)
}

// RestoreFromSnapshot restores a VM from a snapshot.
//
// It starts a fresh Firecracker process (no devices configured) and asks it to
// load the snapshot's state and memory via the /snapshot/load API, which also
// restores device config and resumes the VM. Modern Firecracker has no
// `--snapshot` CLI flag, so the state is applied purely through the API.
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
		return fmt.Errorf("%w: state file not found: %s", ErrSnapshotIncomplete, info.StatePath)
	}
	if _, err := os.Stat(info.MemoryPath); os.IsNotExist(err) {
		return fmt.Errorf("%w: memory file not found: %s", ErrSnapshotIncomplete, info.MemoryPath)
	}

	// Verify checksum
	if info.Checksum != "" {
		checksum, err := sha256File(info.StatePath)
		if err != nil {
			return fmt.Errorf("failed to verify state checksum: %w", err)
		}
		if checksum != info.Checksum {
			return fmt.Errorf("%w: expected %s, got %s", ErrChecksumMismatch, info.Checksum, checksum)
		}
	}

	// When the snapshot recorded a rootfs path, ensure the restored VM's block
	// device has a disk to point at. A snapshot that owns a copy puts it back
	// after the executor removed the original; one without a copy needs the
	// original in place, exactly as before disk copies existed.
	if info.RootfsPath != "" {
		if _, err := os.Stat(info.RootfsPath); err != nil {
			if info.DiskPath == "" {
				return fmt.Errorf("%w: snapshot rootfs not found at %s: %v", ErrSnapshotIncomplete, info.RootfsPath, err)
			}
			if _, copyErr := os.Stat(info.DiskPath); copyErr != nil {
				return fmt.Errorf("%w: snapshot disk copy %s not found and the task disk %s is gone: %v",
					ErrSnapshotIncomplete, info.DiskPath, info.RootfsPath, copyErr)
			}
			if copyErr := copyFile(info.DiskPath, info.RootfsPath); copyErr != nil {
				return fmt.Errorf("failed to restore task disk from snapshot: %w", copyErr)
			}
			log.Info().
				Str("snapshot_id", info.ID).
				Str("rootfs_path", info.RootfsPath).
				Msg("Restored the task disk from the snapshot copy")
		}
	}

	log.Info().
		Str("snapshot_id", info.ID).
		Str("task_id", info.TaskID).
		Str("socket", socketPath).
		Msg("Restoring VM from snapshot")

	// Clean up existing socket
	os.Remove(socketPath)

	// Start a fresh Firecracker process. The snapshot state (including block
	// devices and kernel) is applied through the API below; do NOT pass a
	// config or the removed --snapshot flag.
	fcBinary, err := exec.LookPath("firecracker")
	if err != nil {
		return fmt.Errorf("firecracker binary not found in PATH: %w", err)
	}

	cmd := exec.Command(fcBinary, "--api-sock", socketPath)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start firecracker for restore: %w", err)
	}

	// Track the restored VM process so it can be stopped later
	m.TrackRestoredProcess(info.TaskID, cmd.Process)

	// Ensure cleanup on failure
	startErr := false
	defer func() {
		if startErr {
			if killErr := cmd.Process.Kill(); killErr != nil {
				log.Warn().Err(killErr).Msg("Failed to kill firecracker in snapshot cleanup")
			}
			if remErr := os.Remove(socketPath); remErr != nil {
				log.Warn().Err(remErr).Msg("Failed to remove socket in snapshot cleanup")
			}
		}
	}()

	// Wait for API server
	if err := waitForSocket(socketPath, 10*time.Second); err != nil {
		startErr = true
		return fmt.Errorf("firecracker API not ready after restore: %w", err)
	}

	// Load the snapshot state and memory via /snapshot/load. The payload sets
	// resume_vm=true, so the VM is running once this returns.
	if err := callSnapshotLoad(ctx, socketPath, info.StatePath, info.MemoryPath); err != nil {
		startErr = true
		return fmt.Errorf("failed to load snapshot memory: %w", err)
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
//
// The ID is validated before the filesystem join: an ID is one directory name,
// and "..", "/" or a Windows-style separator would address something outside
// the snapshot root.
func (m *Manager) DeleteSnapshot(_ context.Context, snapshotID string) error {
	if !ValidSnapshotID(snapshotID) {
		return fmt.Errorf("%w: %q", ErrInvalidSnapshotID, snapshotID)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	snapshotDir := filepath.Join(m.config.SnapshotDir, snapshotID)

	if _, err := os.Stat(snapshotDir); os.IsNotExist(err) {
		return fmt.Errorf("%w: %s", ErrNotFound, snapshotID)
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

	maxSnapshots := m.config.MaxSnapshots
	if maxSnapshots <= 0 || len(snapshots) <= maxSnapshots {
		return
	}

	// Remove oldest snapshots (list is sorted newest first)
	toRemove := len(snapshots) - maxSnapshots
	var freed int64
	for i := len(snapshots) - 1; i >= len(snapshots)-toRemove; i-- {
		snap := snapshots[i]
		// A metadata file is node-local data, but it is still the input to a
		// RemoveAll below: an ID that is not one safe directory name must never
		// reach the join, even when the file was written by other software.
		if !ValidSnapshotID(snap.ID) {
			log.Warn().Str("snapshot_id", snap.ID).Msg("Skipping snapshot with an invalid ID")
			continue
		}
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
func callSnapshotCreate(ctx context.Context, socketPath, statePath, memoryPath string) (err error) {
	// Pause VM first (required in v1.14.0+)
	if err := pauseVM(ctx, socketPath); err != nil {
		return fmt.Errorf("failed to pause VM: %w", err)
	}

	// Ensure VM is resumed on any exit path (success or failure). The caller's
	// context may already be done by now — the manager's 30 s deadline fires
	// while a full memory dump is still in flight — and resuming on that context
	// would fail immediately, leaving the VM paused forever. A fresh bounded
	// context keeps the operation's values but drops its cancellation, so the
	// resume always gets a chance to run.
	defer func() {
		resumeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), resumeTimeout)
		defer cancel()
		if resumeErr := resumeVM(resumeCtx, socketPath); resumeErr != nil {
			log.Warn().Err(resumeErr).
				Msg("failed to resume VM after snapshot; the VM is still paused and may need a manual resume")
		}
	}()

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

// resumeTimeout bounds the deferred resume after a snapshot create. It is
// independent of the caller's deadline: a request that timed out still has to
// wake the VM it paused.
const resumeTimeout = 10 * time.Second

const metadataFile = "metadata.json"

func saveMetadata(dir string, info *SnapshotInfo) error {
	path := filepath.Join(dir, metadataFile)
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
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
	if info.DiskPath != "" && !filepath.IsAbs(info.DiskPath) {
		info.DiskPath = filepath.Join(dir, diskFileName)
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

// snapshotIDBytes is the random payload of an ID: 8 bytes, 16 hex characters,
// which is exactly the shape snapshotIDPattern accepts.
const snapshotIDBytes = 8

// snapshotIDAttempts bounds the retry when a generated ID already has a
// directory. A collision past this many tries is a broken source, not bad luck.
const snapshotIDAttempts = 5

// snapshotIDSource is the randomness behind generateSnapshotID. It is a variable
// only so tests can force the fallback or a collision; production never sets it.
var snapshotIDSource = rand.Read

// snapshotIDFallback keeps IDs unique inside one process when the random source
// fails.
var snapshotIDFallback atomic.Uint32

// generateSnapshotID returns a random ID in the shape snapshotIDPattern accepts.
// Time is deliberately not part of the normal path: two snapshots created inside
// the same clock tick used to get the same ID, and the second create silently
// reused the first snapshot's directory.
func generateSnapshotID() string {
	var buf [snapshotIDBytes]byte
	if _, err := snapshotIDSource(buf[:]); err != nil {
		// A broken build or an exhausted sandbox must not stop snapshots. The
		// counter guarantees in-process uniqueness; the time half only makes a
		// collision with another process less likely.
		binary.BigEndian.PutUint32(buf[:4], uint32(time.Now().UnixNano()>>8))
		binary.BigEndian.PutUint32(buf[4:], snapshotIDFallback.Add(1))
	}
	return "snap-" + hex.EncodeToString(buf[:])
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
