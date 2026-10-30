package migration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validArtifact() Artifact {
	return Artifact{
		SchemaVersion: SchemaVersion,
		Kind:          ArtifactKind,
		ID:            "mig-0123456789abcdef",
		SourceTaskID:  "task-1",
		SourceNodeID:  "node-1",
		ServiceID:     "service-1",
		CreatedAt:     time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
		TapName:       "tap-7afaa346-0",
		VsockPath:     "/var/run/firecracker/task-1.vsock",
		DiskPath:      "/var/lib/firecracker/rootfs/tasks/task-1.ext4",
		StateFile:     File{Name: "vm.state", Size: 15360, SHA256: strings.Repeat("a", 64)},
		MemoryFile:    File{Name: "vm.mem", Size: 536870912, SHA256: strings.Repeat("b", 64)},
		DiskFile:      File{Name: "rootfs.ext4", Size: 1073741824, SHA256: strings.Repeat("c", 64)},
	}
}

func TestArtifactValidateAcceptsValidArtifact(t *testing.T) {
	artifact := validArtifact()

	require.NoError(t, artifact.Validate())
	assert.Equal(t, validArtifact(), artifact, "Validate must not normalise the manifest in place")
}

func TestArtifactValidateRejectsBadFields(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Artifact)
		// wantPhrase must appear in the message: it is the sentence that names
		// the field and what was wrong with it.
		wantPhrase string
	}{
		{
			name:       "schema version missing",
			mutate:     func(a *Artifact) { a.SchemaVersion = 0 },
			wantPhrase: "schemaVersion is 0",
		},
		{
			name:       "schema version unknown",
			mutate:     func(a *Artifact) { a.SchemaVersion = 2 },
			wantPhrase: "schemaVersion is 2",
		},
		{
			name:       "kind empty",
			mutate:     func(a *Artifact) { a.Kind = "" },
			wantPhrase: `kind is empty`,
		},
		{
			name:       "kind is another document",
			mutate:     func(a *Artifact) { a.Kind = "Snapshot" },
			wantPhrase: `kind "Snapshot" is not "MigrationArtifact"`,
		},
		{
			name:       "id empty",
			mutate:     func(a *Artifact) { a.ID = "" },
			wantPhrase: "id is empty",
		},
		{
			name:       "id has the wrong prefix",
			mutate:     func(a *Artifact) { a.ID = "snap-0123456789abcdef" },
			wantPhrase: `id "snap-0123456789abcdef" is not a migration artifact ID`,
		},
		{
			name:       "id is too short",
			mutate:     func(a *Artifact) { a.ID = "mig-0123456789abcde" },
			wantPhrase: "is not a migration artifact ID",
		},
		{
			name:       "id is not hex",
			mutate:     func(a *Artifact) { a.ID = "mig-0123456789abcdeZ" },
			wantPhrase: "is not a migration artifact ID",
		},
		{
			name:       "source task ID missing",
			mutate:     func(a *Artifact) { a.SourceTaskID = "" },
			wantPhrase: "sourceTaskId is empty",
		},
		{
			name:       "source node ID missing",
			mutate:     func(a *Artifact) { a.SourceNodeID = "" },
			wantPhrase: "sourceNodeId is empty",
		},
		{
			name:       "service ID missing",
			mutate:     func(a *Artifact) { a.ServiceID = "" },
			wantPhrase: "serviceId is empty",
		},
		{
			name:       "creation time missing",
			mutate:     func(a *Artifact) { a.CreatedAt = time.Time{} },
			wantPhrase: "createdAt is zero",
		},
		{
			name:       "tap name missing",
			mutate:     func(a *Artifact) { a.TapName = "" },
			wantPhrase: "tapName is empty",
		},
		{
			name:       "tap name too long for the kernel",
			mutate:     func(a *Artifact) { a.TapName = "tap-0123456789abcdef-0" },
			wantPhrase: `tapName "tap-0123456789abcdef-0" is not a valid interface name`,
		},
		{
			name:       "tap name is a path",
			mutate:     func(a *Artifact) { a.TapName = "tap-7afaa346-0/x" },
			wantPhrase: "is not a valid interface name",
		},
		{
			name:       "vsock path missing",
			mutate:     func(a *Artifact) { a.VsockPath = "" },
			wantPhrase: "vsockPath is empty",
		},
		{
			name:       "vsock path relative",
			mutate:     func(a *Artifact) { a.VsockPath = "task-1.vsock" },
			wantPhrase: `vsockPath "task-1.vsock" is relative`,
		},
		{
			name:       "disk path missing",
			mutate:     func(a *Artifact) { a.DiskPath = "" },
			wantPhrase: "diskPath is empty",
		},
		{
			name:       "disk path relative",
			mutate:     func(a *Artifact) { a.DiskPath = "tasks/task-1.ext4" },
			wantPhrase: `diskPath "tasks/task-1.ext4" is relative`,
		},
		{
			name:       "state file name missing",
			mutate:     func(a *Artifact) { a.StateFile.Name = "" },
			wantPhrase: "stateFile.name is empty",
		},
		{
			name:       "state file name is a path",
			mutate:     func(a *Artifact) { a.StateFile.Name = "snap-1/vm.state" },
			wantPhrase: `stateFile.name "snap-1/vm.state" is a path`,
		},
		{
			name:       "state file is empty",
			mutate:     func(a *Artifact) { a.StateFile.Size = 0 },
			wantPhrase: "stateFile.size is 0",
		},
		{
			name:       "memory file checksum is short",
			mutate:     func(a *Artifact) { a.MemoryFile.SHA256 = strings.Repeat("b", 63) },
			wantPhrase: "memoryFile.sha256 is 63 characters",
		},
		{
			name:       "memory file checksum is long",
			mutate:     func(a *Artifact) { a.MemoryFile.SHA256 = strings.Repeat("b", 65) },
			wantPhrase: "memoryFile.sha256 is 65 characters",
		},
		{
			name:       "memory file checksum is not hex",
			mutate:     func(a *Artifact) { a.MemoryFile.SHA256 = strings.Repeat("z", 64) },
			wantPhrase: "memoryFile.sha256 is not 64 lowercase hex characters",
		},
		{
			name:       "disk file checksum missing",
			mutate:     func(a *Artifact) { a.DiskFile.SHA256 = "" },
			wantPhrase: "diskFile.sha256 is empty",
		},
		{
			name:       "disk file size negative",
			mutate:     func(a *Artifact) { a.DiskFile.Size = -1 },
			wantPhrase: "diskFile.size is -1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			artifact := validArtifact()
			tc.mutate(&artifact)

			err := artifact.Validate()
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidArtifact)
			assert.Contains(t, err.Error(), tc.wantPhrase)
			assert.Contains(t, err.Error(), "invalid migration artifact",
				"every rejection carries the sentinel's phrase")
		})
	}
}

func TestArtifactValidateAcceptsPathAndNameEdgeCases(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Artifact)
	}{
		{
			name:   "interface name at the kernel limit",
			mutate: func(a *Artifact) { a.TapName = "tap-7afaa346-99" }, // 15 characters
		},
		{
			name:   "single-file name with a dot",
			mutate: func(a *Artifact) { a.StateFile.Name = "vm.state.v1" },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			artifact := validArtifact()
			tc.mutate(&artifact)
			require.NoError(t, artifact.Validate())
		})
	}
}

func TestValidArtifactID(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{id: "mig-0123456789abcdef", want: true},
		{id: "mig-ffffffffffffffff", want: true},
		{id: "", want: false},
		{id: "mig-", want: false},
		{id: "mig-0123456789abcde", want: false},
		{id: "mig-0123456789abcdef0", want: false},
		{id: "mig-0123456789ABCDEF", want: false},
		{id: "snap-0123456789abcdef", want: false},
		{id: "mig-0123456789abcde/", want: false},
		{id: "mig-0123456789abcde.", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			assert.Equal(t, tc.want, ValidArtifactID(tc.id))
		})
	}
}

// TestArtifactJSONRoundTrip pins the wire shape: the artifact crosses the node
// boundary as JSON, so the field names created here are what the target reads.
func TestArtifactJSONRoundTrip(t *testing.T) {
	original := validArtifact()

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var keys map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &keys))
	for _, key := range []string{
		"schemaVersion", "kind", "id", "sourceTaskId", "sourceNodeId", "serviceId",
		"createdAt", "tapName", "vsockPath", "diskPath",
		"stateFile", "memoryFile", "diskFile",
	} {
		assert.Contains(t, keys, key)
	}

	var decoded Artifact
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, original, decoded)
	require.NoError(t, decoded.Validate())
}

// writeTempFile writes content under dir and returns its absolute path.
func writeTempFile(t *testing.T, dir, name, content string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func sha256Hex(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// snapshotFixture is one snapshot directory shaped like the one
// pkg/snapshot publishes: vm.state, vm.mem and the disk copy.
type snapshotFixture struct {
	info       *snapshot.SnapshotInfo
	stateBytes string
	memBytes   string
	diskBytes  string
}

func newSnapshotFixture(t *testing.T) snapshotFixture {
	t.Helper()

	dir := t.TempDir()
	f := snapshotFixture{
		stateBytes: "{}",
		memBytes:   strings.Repeat("memory", 128),
		diskBytes:  strings.Repeat("disk", 256),
	}

	statePath := writeTempFile(t, dir, "vm.state", f.stateBytes)
	memoryPath := writeTempFile(t, dir, "vm.mem", f.memBytes)
	diskPath := writeTempFile(t, dir, "rootfs.ext4", f.diskBytes)

	f.info = &snapshot.SnapshotInfo{
		ID:         "snap-0123456789abcdef",
		TaskID:     "task-1",
		ServiceID:  "service-1",
		NodeID:     "node-1",
		CreatedAt:  time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
		MemoryPath: memoryPath,
		StatePath:  statePath,
		SizeBytes:  int64(len(f.stateBytes) + len(f.memBytes) + len(f.diskBytes)),
		RootfsPath: "/var/lib/firecracker/rootfs/tasks/task-1.ext4",
		DiskPath:   diskPath,
		Checksum:   sha256Hex(f.stateBytes),
	}
	return f
}

func TestArtifactFromSnapshot(t *testing.T) {
	fixture := newSnapshotFixture(t)

	artifact, err := ArtifactFromSnapshot(fixture.info, "/var/run/firecracker", 0)
	require.NoError(t, err)

	assert.True(t, ValidArtifactID(artifact.ID), "id %q is not mig-<16 hex>", artifact.ID)
	assert.Equal(t, SchemaVersion, artifact.SchemaVersion)
	assert.Equal(t, ArtifactKind, artifact.Kind)
	assert.Equal(t, "task-1", artifact.SourceTaskID)
	assert.Equal(t, "node-1", artifact.SourceNodeID)
	assert.Equal(t, "service-1", artifact.ServiceID)
	assert.Equal(t, fixture.info.CreatedAt, artifact.CreatedAt)

	// The source VM's host-side identity, not the new task's.
	assert.Equal(t, "/var/lib/firecracker/rootfs/tasks/task-1.ext4", artifact.DiskPath,
		"diskPath must be the path recorded inside the snapshot state file")

	stateFile := File{Name: "vm.state", Size: int64(len(fixture.stateBytes)), SHA256: sha256Hex(fixture.stateBytes)}
	memoryFile := File{Name: "vm.mem", Size: int64(len(fixture.memBytes)), SHA256: sha256Hex(fixture.memBytes)}
	diskFile := File{Name: "rootfs.ext4", Size: int64(len(fixture.diskBytes)), SHA256: sha256Hex(fixture.diskBytes)}
	assert.Equal(t, stateFile, artifact.StateFile)
	assert.Equal(t, memoryFile, artifact.MemoryFile)
	assert.Equal(t, diskFile, artifact.DiskFile)

	require.NoError(t, artifact.Validate(), "a manifest the builder produces must validate")
}

// TestArtifactFromSnapshotUsesProductionNamingRules pins the TAP name and vsock
// path against the helpers the running node uses: a manifest that disagreed
// with them would restore a VM whose recorded devices do not exist.
func TestArtifactFromSnapshotUsesProductionNamingRules(t *testing.T) {
	fixture := newSnapshotFixture(t)

	artifact, err := ArtifactFromSnapshot(fixture.info, "/var/run/firecracker", 1)
	require.NoError(t, err)

	assert.Equal(t, scruntime.TapName("task-1", 1), artifact.TapName)
	assert.Equal(t, "tap-7afaa346-1", artifact.TapName,
		"tap-<sha256(taskId)[:8]>-<index>, the rule pkg/network and pkg/translator use")
	assert.Equal(t, scruntime.VsockPath("/var/run/firecracker", "task-1"), artifact.VsockPath)
	assert.Equal(t, "/var/run/firecracker/task-1.vsock", artifact.VsockPath,
		"<socketDir>/<taskId>.vsock, the rule pkg/broker.Task.VsockPath uses")
}

func TestArtifactFromSnapshotRejects(t *testing.T) {
	t.Run("no snapshot info", func(t *testing.T) {
		_, err := ArtifactFromSnapshot(nil, "/var/run/firecracker", 0)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidArtifact)
		assert.Contains(t, err.Error(), "source snapshot is missing")
	})

	t.Run("no task ID", func(t *testing.T) {
		fixture := newSnapshotFixture(t)
		fixture.info.TaskID = ""

		_, err := ArtifactFromSnapshot(fixture.info, "/var/run/firecracker", 0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "source snapshot has no task ID")
	})

	t.Run("no socket directory", func(t *testing.T) {
		fixture := newSnapshotFixture(t)

		_, err := ArtifactFromSnapshot(fixture.info, "", 0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "socket directory is empty")
	})

	t.Run("no disk copy", func(t *testing.T) {
		fixture := newSnapshotFixture(t)
		fixture.info.DiskPath = ""

		_, err := ArtifactFromSnapshot(fixture.info, "/var/run/firecracker", 0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "has no disk copy")
	})

	t.Run("no state file", func(t *testing.T) {
		fixture := newSnapshotFixture(t)
		fixture.info.StatePath = ""

		_, err := ArtifactFromSnapshot(fixture.info, "/var/run/firecracker", 0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "has no state file")
	})

	t.Run("missing memory file on disk", func(t *testing.T) {
		fixture := newSnapshotFixture(t)
		fixture.info.MemoryPath = filepath.Join(t.TempDir(), "vm.mem")

		_, err := ArtifactFromSnapshot(fixture.info, "/var/run/firecracker", 0)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidArtifact)
		assert.Contains(t, err.Error(), "memory file")
	})

	t.Run("state path is a directory", func(t *testing.T) {
		fixture := newSnapshotFixture(t)
		fixture.info.StatePath = t.TempDir()

		_, err := ArtifactFromSnapshot(fixture.info, "/var/run/firecracker", 0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is not a regular file")
	})

	t.Run("snapshot without a recorded disk path", func(t *testing.T) {
		fixture := newSnapshotFixture(t)
		fixture.info.RootfsPath = ""

		_, err := ArtifactFromSnapshot(fixture.info, "/var/run/firecracker", 0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "has no recorded disk path")
	})

	t.Run("state checksum disagrees with the snapshot metadata", func(t *testing.T) {
		fixture := newSnapshotFixture(t)
		fixture.info.Checksum = strings.Repeat("d", 64)

		_, err := ArtifactFromSnapshot(fixture.info, "/var/run/firecracker", 0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "does not match the snapshot metadata")
	})

	t.Run("snapshot without a service ID", func(t *testing.T) {
		fixture := newSnapshotFixture(t)
		fixture.info.ServiceID = ""

		_, err := ArtifactFromSnapshot(fixture.info, "/var/run/firecracker", 0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "serviceId is empty")
	})
}

func TestArtifactFromSnapshotGivesEachCallANewID(t *testing.T) {
	fixture := newSnapshotFixture(t)

	first, err := ArtifactFromSnapshot(fixture.info, "/var/run/firecracker", 0)
	require.NoError(t, err)
	second, err := ArtifactFromSnapshot(fixture.info, "/var/run/firecracker", 0)
	require.NoError(t, err)

	assert.NotEqual(t, first.ID, second.ID, "two exports are two artifacts")
}
