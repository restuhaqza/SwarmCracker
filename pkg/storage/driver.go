// Package storage provides persistent volume management for SwarmCracker.
//
// Volume drivers abstract how data is stored and mounted into Firecracker microVMs.
// Two built-in drivers are provided:
//   - DirectoryDriver: stores data as host directories (simple, compatible)
//   - BlockDriver: stores data as ext4 loopback images (supports native quota, better performance)
//
// A VolumeManager dispatches to the appropriate driver based on volume configuration.
package storage

import (
	"context"
	"io"
	"time"
)

// VolumeType defines how a volume stores its data on the host.
type VolumeType string

const (
	// VolumeTypeDir stores data in a host directory (simple, portable).
	VolumeTypeDir VolumeType = "dir"

	// VolumeTypeBlock stores data in an ext4 loopback image file (supports native quota).
	VolumeTypeBlock VolumeType = "block"
)

// CreateOptions specifies options when creating a new volume.
type CreateOptions struct {
	// Type selects the volume backend. Default: VolumeTypeDir.
	Type VolumeType
	// SizeMB limits volume capacity. 0 means unlimited (dir driver default).
	// For block volumes, this determines the loopback image size.
	SizeMB int
	// Driver-specific options passed as key-value pairs.
	DriverOpts map[string]string
}

// VolumeInfo provides metadata about a volume.
type VolumeInfo struct {
	Name       string     `json:"name"`
	Type       VolumeType `json:"type"`
	Path       string     `json:"path"`
	SizeMB     int        `json:"size_mb"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt time.Time  `json:"last_used_at"`
	TaskID     string     `json:"task_id"`
	UsedMB     int64      `json:"used_mb"` // Current disk usage in MB
}

// Snapshot represents a point-in-time copy of a volume.
type Snapshot struct {
	ID        string    `json:"id"`
	Volume    string    `json:"volume"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"created_at"`
	SizeMB    int64     `json:"size_mb"`
}

// VolumeDriver is the interface that all volume backends must implement.
//
// Each driver handles how data is physically stored and how it is
// projected into the microVM rootfs during Mount/Unmount.
type VolumeDriver interface {
	// Type returns the driver's volume type identifier.
	Type() VolumeType

	// Create creates the backing storage for a new volume.
	// Returns the host-side path where the volume data lives.
	Create(ctx context.Context, name string, opts CreateOptions) (path string, err error)

	// Delete removes the backing storage and all data.
	Delete(ctx context.Context, name string) error

	// Mount copies/mounts volume data into the rootfs at the given target path.
	// This is called before the VM boots.
	Mount(ctx context.Context, name, rootfsPath, target string) error

	// Unmount copies data back from rootfs to the volume store.
	// If readOnly is true, the sync-back is skipped.
	Unmount(ctx context.Context, name, rootfsPath, target string, readOnly bool) error

	// Stat returns current metadata and usage for a volume.
	Stat(ctx context.Context, name string) (*VolumeInfo, error)

	// Capacity reports current usage and the configured limit.
	// Returns (usedBytes, limitBytes, error). limitBytes is 0 if unlimited.
	Capacity(ctx context.Context, name string) (usedBytes int64, limitBytes int64, err error)

	// Snapshot creates a point-in-time copy of the volume.
	Snapshot(ctx context.Context, name string) (*Snapshot, error)

	// Restore replaces volume contents from a snapshot.
	Restore(ctx context.Context, name string, snap *Snapshot) error

	// Export streams volume data for remote backup.
	Export(ctx context.Context, name string, w io.Writer) error

	// Import reads data from a stream and creates/restores a volume.
	Import(ctx context.Context, name string, r io.Reader, sizeMB int) error
}
