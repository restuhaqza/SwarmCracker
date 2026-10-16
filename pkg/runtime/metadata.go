package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// VMMetadataSuffix is the suffix of the per-task network metadata file the
// daemon writes next to a VM's Firecracker API socket. The CLI reads it to
// recover runtime details — most importantly the guest IP, and the live
// healthcheck state — that the daemon assigns at runtime but never writes back
// into the SwarmKit task store.
const VMMetadataSuffix = ".net.json"

// Health states published in VMHealth.State. They mirror the healthcheck
// package's verdicts, but are kept as plain strings so an old reader (or an old
// writer) degrades to "no health" instead of failing to decode.
const (
	// HealthStarting means the task is not healthy yet: it is still inside its
	// start period, or has not passed a first check. A task in this state has
	// not been reported RUNNING by SwarmKit yet.
	HealthStarting = "starting"
	// HealthHealthy means the most recent probe passed.
	HealthHealthy = "healthy"
	// HealthFailing means the most recent probe failed but the retries
	// threshold has not been reached, so the task is still RUNNING and no
	// restart has been triggered. This is the window Docker's own status hides:
	// it keeps reporting "healthy" until the verdict flips. It is the whole
	// point of publishing health from the daemon.
	HealthFailing = "failing"
	// HealthUnhealthy means the task has failed its check enough times in a row
	// to be considered unhealthy; the daemon is stopping the VM so SwarmKit
	// reschedules the task.
	HealthUnhealthy = "unhealthy"
)

// VMHealth is the daemon's live view of a task's healthcheck. It is written
// next to the VM socket and read by the node-local commands (`vm status`, `vm
// list`) and by the daemon's per-node health endpoint, which `service ps`/`task
// ls` fan out to. SwarmKit itself has no health field and never polls a running
// task's status, so the value travels through that endpoint rather than through
// the task store.
type VMHealth struct {
	// State is HealthStarting, HealthHealthy, HealthFailing or HealthUnhealthy.
	State string `json:"state"`
	// FailingStreak is the number of consecutive failed probes that count
	// toward the retries threshold. Per Docker semantics a start-period failure
	// does not count, so it stays 0 while inside the start period.
	FailingStreak int `json:"failing_streak,omitempty"`
	// Output is the last probe's combined stdout+stderr, bounded by the daemon.
	Output string `json:"output,omitempty"`
	// Command is the probe command, for context on what is being checked.
	Command string `json:"command,omitempty"`
	// UpdatedAt is when this observation was recorded.
	UpdatedAt time.Time `json:"updated_at"`
}

// VMMetadata is the runtime metadata persisted by the daemon for a running VM.
// It is best-effort enrichment for the CLI: missing or unreadable files are
// treated as "no metadata", never as an error.
type VMMetadata struct {
	// ID is the task/VM ID the metadata belongs to.
	ID string `json:"id"`
	// NetworkID is the network the VM's first interface is attached to.
	NetworkID string `json:"network_id,omitempty"`
	// IPAddresses are the guest IPs in CIDR form (e.g. "192.168.127.42/24").
	IPAddresses []string `json:"ip_addresses,omitempty"`
	// Health is the live healthcheck observation. It is nil when the task
	// declares no healthcheck, or when the daemon has not probed yet.
	Health *VMHealth `json:"health,omitempty"`
}

// metadataPath returns the metadata file path for a task under dir.
func metadataPath(dir, taskID string) string {
	return filepath.Join(dir, taskID+VMMetadataSuffix)
}

// WriteVMMetadata atomically writes the runtime metadata for a task under dir.
// The directory must exist; the write is temp-file-then-rename so a reader
// never observes a partially written file.
//
// Callers must serialize writes per task: the temporary path is derived from
// the task ID, so two concurrent writers would collide on it. The daemon's
// controller is the only writer for a task (start, then health updates).
func WriteVMMetadata(dir string, md *VMMetadata) error {
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("metadata directory is required")
	}
	if md == nil || strings.TrimSpace(md.ID) == "" {
		return fmt.Errorf("metadata ID is required")
	}

	data, err := json.MarshalIndent(md, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal VM metadata: %w", err)
	}

	path := metadataPath(dir, md.ID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("failed to write VM metadata: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("failed to rename VM metadata: %w", err)
	}

	return nil
}

// ReadVMMetadata reads the runtime metadata for a task from dir. It returns
// (nil, false) when the file is missing or malformed so callers can fall back
// to other sources.
func ReadVMMetadata(dir, taskID string) (*VMMetadata, bool) {
	if strings.TrimSpace(dir) == "" || strings.TrimSpace(taskID) == "" {
		return nil, false
	}

	data, err := os.ReadFile(metadataPath(dir, taskID))
	if err != nil {
		return nil, false
	}

	var md VMMetadata
	if err := json.Unmarshal(data, &md); err != nil {
		return nil, false
	}
	return &md, true
}

// RemoveVMMetadata removes the runtime metadata file for a task under dir. It
// is a no-op when the directory/task is empty or the file is already gone.
func RemoveVMMetadata(dir, taskID string) error {
	if strings.TrimSpace(dir) == "" || strings.TrimSpace(taskID) == "" {
		return nil
	}
	if err := os.Remove(metadataPath(dir, taskID)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ListVMMetadata reads the metadata of every task under dir. Malformed files are
// skipped rather than failing the whole listing, so one corrupt sidecar cannot
// hide the node's other VMs.
func ListVMMetadata(dir string) []*VMMetadata {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	matches, err := filepath.Glob(filepath.Join(dir, "*"+VMMetadataSuffix))
	if err != nil {
		return nil
	}
	out := make([]*VMMetadata, 0, len(matches))
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var md VMMetadata
		if err := json.Unmarshal(data, &md); err != nil || md.ID == "" {
			continue
		}
		out = append(out, &md)
	}
	return out
}
