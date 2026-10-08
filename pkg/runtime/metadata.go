package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// VMMetadataSuffix is the suffix of the per-task network metadata file the
// daemon writes next to a VM's Firecracker API socket. The CLI reads it to
// recover network details — most importantly the guest IP — that the daemon
// assigns at runtime but never writes back into the SwarmKit task store.
const VMMetadataSuffix = ".net.json"

// VMMetadata is the network metadata persisted by the daemon for a running VM.
// It is best-effort enrichment for the CLI: missing or unreadable files are
// treated as "no metadata", never as an error.
type VMMetadata struct {
	// ID is the task/VM ID the metadata belongs to.
	ID string `json:"id"`
	// NetworkID is the network the VM's first interface is attached to.
	NetworkID string `json:"network_id,omitempty"`
	// IPAddresses are the guest IPs in CIDR form (e.g. "192.168.127.42/24").
	IPAddresses []string `json:"ip_addresses,omitempty"`
}

// metadataPath returns the metadata file path for a task under dir.
func metadataPath(dir, taskID string) string {
	return filepath.Join(dir, taskID+VMMetadataSuffix)
}

// WriteVMMetadata atomically writes the network metadata for a task under dir.
// The directory must exist; the write is temp-file-then-rename so a reader
// never observes a partially written file.
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

// ReadVMMetadata reads the network metadata for a task from dir. It returns
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

// RemoveVMMetadata removes the network metadata file for a task under dir. It
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
