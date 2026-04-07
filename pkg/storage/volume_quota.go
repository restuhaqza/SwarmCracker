package storage

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/rs/zerolog/log"
)

// QuotaError is returned when a volume operation would exceed its size limit.
type QuotaError struct {
	Volume    string
	LimitMB   int
	CurrentMB int64
	Message   string
}

func (e *QuotaError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("volume %q: quota exceeded (%d MB used / %d MB limit)", e.Volume, e.CurrentMB, e.LimitMB)
}

// QuotaEnforcer checks volume sizes before sync-back and prevents overflows.
//
// It uses directory size measurement (du equivalent) rather than filesystem
// project quotas, making it work on any filesystem without special kernel
// support. The tradeoff is slightly less precise enforcement — a write can
// exceed the limit before the next check — but it's sufficient for the
// copy-in/copy-out model SwarmCracker uses.
type QuotaEnforcer struct{}

// NewQuotaEnforcer creates a new quota enforcer.
func NewQuotaEnforcer() *QuotaEnforcer {
	return &QuotaEnforcer{}
}

// CheckCreate validates that requested size is sensible.
func (q *QuotaEnforcer) CheckCreate(sizeMB int) error {
	if sizeMB < 0 {
		return fmt.Errorf("volume size cannot be negative: %d MB", sizeMB)
	}
	if sizeMB > 1024*1024 { // 1 TB
		return fmt.Errorf("volume size too large: %d MB (max 1048576 MB)", sizeMB)
	}
	return nil
}

// CheckSync verifies that data to be synced back will fit within the volume quota.
//
// sourcePath is the directory to measure (e.g., rootfs mount point with data).
// limitMB is the configured size limit (0 = unlimited).
//
// Returns nil if the data fits or if limit is 0 (unlimited).
func (q *QuotaEnforcer) CheckSync(volumeName, sourcePath string, limitMB int) error {
	if limitMB <= 0 {
		return nil // unlimited
	}

	usedBytes, err := dirSizeBytes(sourcePath)
	if err != nil {
		// If we can't measure, log a warning but don't block the operation.
		// This prevents data loss on permission errors or unusual filesystems.
		log.Warn().Err(err).Str("volume", volumeName).Msg("Cannot measure data size, skipping quota check")
		return nil
	}

	usedMB := usedBytes / (1024 * 1024)
	limit := int64(limitMB)

	if usedMB > limit {
		return &QuotaError{
			Volume:    volumeName,
			LimitMB:   limitMB,
			CurrentMB: usedMB,
			Message:   fmt.Sprintf("volume %q would exceed quota: %d MB > %d MB limit", volumeName, usedMB, limitMB),
		}
	}

	return nil
}

// CheckCapacity validates current usage against the limit.
// Returns an error if the volume is over quota.
func (q *QuotaEnforcer) CheckCapacity(volumeName, dataPath string, limitMB int) error {
	if limitMB <= 0 {
		return nil
	}

	usedBytes, err := dirSizeBytes(dataPath)
	if err != nil {
		return nil
	}

	usedMB := usedBytes / (1024 * 1024)
	if usedMB > int64(limitMB) {
		return &QuotaError{
			Volume:    volumeName,
			LimitMB:   limitMB,
			CurrentMB: usedMB,
		}
	}
	return nil
}

// EnforceDirLimit removes oldest/largest files to fit within quota.
// This is a last-resort enforcement — better to prevent upfront.
//
// dataPath: directory to trim
// limitMB: target limit
// Returns number of files removed.
func (q *QuotaEnforcer) EnforceDirLimit(dataPath string, limitMB int) (int, error) {
	if limitMB <= 0 {
		return 0, nil
	}

	usedBytes, err := dirSizeBytes(dataPath)
	if err != nil {
		return 0, err
	}

	limitBytes := int64(limitMB) * 1024 * 1024
	if usedBytes <= limitBytes {
		return 0, nil
	}

	removed := 0
	// Walk and find files to remove (newest first — keep recent data)
	entries, err := os.ReadDir(dataPath)
	if err != nil {
		return 0, err
	}

	// Sort files by modification time descending (remove oldest first)
	type fileInfo struct {
		name string
		size int64
	}
	var files []fileInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fileInfo{name: e.Name(), size: info.Size()})
	}

	for _, f := range files {
		if usedBytes <= limitBytes {
			break
		}
		p := filepath.Join(dataPath, f.name)
		if err := os.Remove(p); err != nil {
			log.Warn().Err(err).Str("file", p).Msg("Failed to remove file for quota enforcement")
			continue
		}
		usedBytes -= f.size
		removed++
		log.Warn().Str("file", p).Int64("size", f.size).Msg("Removed file to enforce quota")
	}

	return removed, nil
}
