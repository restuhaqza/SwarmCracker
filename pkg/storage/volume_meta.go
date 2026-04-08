package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

const metadataFileName = "meta.json"

// volumeMeta is the on-disk JSON representation of volume metadata.
type volumeMeta struct {
	Name       string     `json:"name"`
	Type       VolumeType `json:"type"`
	SizeMB     int        `json:"size_mb"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt time.Time  `json:"last_used_at"`
	TaskID     string     `json:"task_id"`
}

// MetaStore persists volume metadata as JSON files on disk.
//
// Layout:
//
//	<volumesDir>/
//	  <volume-name>/
//	    meta.json          ← metadata (always present)
//	    data/              ← volume data (dir driver)
//	    image.ext4         ← loopback image (block driver)
type MetaStore struct {
	baseDir string
	mu      sync.RWMutex
}

// NewMetaStore creates a MetaStore backed by the given directory.
func NewMetaStore(baseDir string) (*MetaStore, error) {
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("create metadata base dir: %w", err)
	}
	return &MetaStore{baseDir: baseDir}, nil
}

// volumeDir returns the directory path for a named volume.
func (ms *MetaStore) volumeDir(name string) string {
	return filepath.Join(ms.baseDir, sanitizeVolumeName(name))
}

// metaPath returns the JSON metadata file path for a named volume.
func (ms *MetaStore) metaPath(name string) string {
	return filepath.Join(ms.volumeDir(name), metadataFileName)
}

// Write persists metadata to disk. The volume directory is created if needed.
func (ms *MetaStore) Write(ctx context.Context, m *volumeMeta) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	dir := ms.volumeDir(m.Name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create volume dir %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	if err := os.WriteFile(ms.metaPath(m.Name), data, 0644); err != nil {
		return fmt.Errorf("write metadata: %w", err)
	}

	log.Debug().Str("volume", m.Name).Str("path", ms.metaPath(m.Name)).Msg("Metadata written")
	return nil
}

// Read loads metadata from disk. Returns os.ErrNotExist if not found.
func (ms *MetaStore) Read(ctx context.Context, name string) (*volumeMeta, error) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	data, err := os.ReadFile(ms.metaPath(name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("read metadata: %w", err)
	}

	var m volumeMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("unmarshal metadata: %w", err)
	}
	return &m, nil
}

// Delete removes the metadata file. It does NOT remove the volume directory itself.
func (ms *MetaStore) Delete(ctx context.Context, name string) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	p := ms.metaPath(name)
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove metadata: %w", err)
	}
	return nil
}

// List returns metadata for all volumes that have a meta.json.
func (ms *MetaStore) List(ctx context.Context) ([]*volumeMeta, error) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	entries, err := os.ReadDir(ms.baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read volumes dir: %w", err)
	}

	var metas []*volumeMeta
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(ms.baseDir, e.Name(), metadataFileName)
		data, err := os.ReadFile(p)
		if err != nil {
			log.Debug().Str("name", e.Name()).Msg("No metadata file, skipping")
			continue
		}
		var m volumeMeta
		if err := json.Unmarshal(data, &m); err != nil {
			log.Warn().Err(err).Str("name", e.Name()).Msg("Corrupt metadata, skipping")
			continue
		}
		metas = append(metas, &m)
	}
	return metas, nil
}

// TouchLastUsed updates the LastUsedAt timestamp for a volume.
func (ms *MetaStore) TouchLastUsed(ctx context.Context, name string) error {
	m, err := ms.Read(ctx, name)
	if err != nil {
		return err
	}
	m.LastUsedAt = time.Now()
	return ms.Write(ctx, m)
}

// UpdateTaskID sets the owning task ID for a volume.
func (ms *MetaStore) UpdateTaskID(ctx context.Context, name, taskID string) error {
	m, err := ms.Read(ctx, name)
	if err != nil {
		return err
	}
	m.TaskID = taskID
	return ms.Write(ctx, m)
}

// RemoveVolumeDir removes the entire volume directory including metadata and data.
func (ms *MetaStore) RemoveVolumeDir(name string) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	dir := ms.volumeDir(name)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove volume dir %s: %w", dir, err)
	}
	log.Debug().Str("volume", name).Msg("Volume directory removed")
	return nil
}
