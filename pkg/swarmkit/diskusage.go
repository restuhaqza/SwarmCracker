package swarmkit

import (
	"syscall"

	swarmcrackermetrics "github.com/restuhaqza/swarmcracker/pkg/metrics"
	"github.com/rs/zerolog/log"
)

// diskPath is one configured directory the daemon reports disk pressure for.
// label is the value published as the metric's path label; path is the
// directory on disk.
type diskPath struct {
	label string
	path  string
}

// diskPaths returns the executor's configured disk paths. rootfs and state come
// from the executor config; snapshots from SnapshotDir, which NewExecutor
// defaults to pkg/config's DefaultSnapshotDir, the same directory the snapshot
// manager uses. An empty path is skipped by the sampler.
func (e *Executor) diskPaths() []diskPath {
	if e == nil || e.config == nil {
		return nil
	}
	return []diskPath{
		{label: "rootfs", path: e.config.RootfsDir},
		{label: "snapshots", path: e.config.SnapshotDir},
		{label: "state", path: e.config.StateDir},
	}
}

// sampleDiskUsage publishes used and total bytes for every configured path that
// exists. A path that is unset, missing, or unreadable produces no series and a
// debug log, so the daemon never reports a directory it does not own as zero.
func (e *Executor) sampleDiskUsage() {
	if e == nil || e.config == nil {
		return
	}
	sampleDiskUsage(e.config.Hostname, e.diskPaths())
}

// sampleDiskUsage is the package-level sampler so it can be tested without a
// live executor. It uses syscall.Statfs, the same call the image preparer uses
// for its free-space check, and reports used bytes as df does (capacity minus
// free blocks).
func sampleDiskUsage(instance string, paths []diskPath) {
	for _, p := range paths {
		if p.path == "" {
			log.Debug().Str("path_label", p.label).Msg("Disk usage: path not configured, skipping")
			continue
		}

		var stat syscall.Statfs_t
		if err := syscall.Statfs(p.path, &stat); err != nil {
			log.Debug().Err(err).Str("path_label", p.label).Str("path", p.path).
				Msg("Disk usage: path unavailable, skipping")
			continue
		}

		blockSize := int64(stat.Bsize)
		if blockSize <= 0 {
			continue
		}
		total := stat.Blocks * uint64(blockSize)
		var used uint64
		if stat.Blocks > stat.Bfree {
			used = (stat.Blocks - stat.Bfree) * uint64(blockSize)
		}
		swarmcrackermetrics.RecordDiskUsage(instance, p.label, used, total)
	}
}
