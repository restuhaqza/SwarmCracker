package migration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// artifactCreatedAt is the export instant of the fixture manifest. It is fixed
// so the JSON pins below are stable; the builder's own clock is checked
// separately, against the wall clock.
var artifactCreatedAt = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

func validArtifact() Artifact {
	return Artifact{
		SchemaVersion: v1.SchemaVersion,
		Kind:          ArtifactKind,
		ID:            "mig-0123456789abcdef",
		SnapshotID:    "snap-0123456789abcdef",
		SourceTaskID:  "task-1",
		SourceNodeID:  "node-1",
		ServiceID:     "service-1",
		CreatedAt:     artifactCreatedAt,
		SnapshotAt:    artifactCreatedAt.Add(-time.Minute),
		TapName:       "tap-7afaa346-0",
		VsockPath:     "/var/run/firecracker/task-1.vsock",
		DiskPath:      "/var/lib/firecracker/rootfs/tasks/task-1.ext4",
		StateFile:     File{Name: "vm.state", Size: 15360, SHA256: strings.Repeat("a", 64)},
		MemoryFile:    File{Name: "vm.mem", Size: 536870912, SHA256: strings.Repeat("b", 64)},
		DiskFile:      File{Name: "rootfs.ext4", Size: 1073741824, SHA256: strings.Repeat("c", 64)},
	}
}

func TestArtifactValidateAcceptsValidArtifact(t *testing.T) {
	require.NoError(t, validArtifact().Validate())
}

// TestMaxFileSizeIsTheCeiling pins the one number two packages enforce: a manifest
// may not describe a file larger than this, and the transport may not accept a
// body larger than this. Changing it is a migration-format decision, not a tweak.
func TestMaxFileSizeIsTheCeiling(t *testing.T) {
	assert.Equal(t, int64(64<<30), MaxFileSize)
	assert.Equal(t, "68719476736", strconv.FormatInt(MaxFileSize, 10))
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
			name:       "snapshot ID missing",
			mutate:     func(a *Artifact) { a.SnapshotID = "" },
			wantPhrase: "snapshotId is empty",
		},
		{
			name:       "snapshot ID is another document's ID",
			mutate:     func(a *Artifact) { a.SnapshotID = "mig-0123456789abcdef" },
			wantPhrase: `snapshotId "mig-0123456789abcdef" is not a snapshot ID`,
		},
		{
			name:       "snapshot ID is not hex",
			mutate:     func(a *Artifact) { a.SnapshotID = "snap-0123456789abcdeZ" },
			wantPhrase: "is not a snapshot ID",
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
			name:       "creation time missing",
			mutate:     func(a *Artifact) { a.CreatedAt = time.Time{} },
			wantPhrase: "createdAt is zero",
		},
		{
			name:       "snapshot time missing",
			mutate:     func(a *Artifact) { a.SnapshotAt = time.Time{} },
			wantPhrase: "snapshotAt is zero",
		},
		{
			name:       "snapshot taken after the export that describes it",
			mutate:     func(a *Artifact) { a.SnapshotAt = artifactCreatedAt.Add(time.Second) },
			wantPhrase: "is after createdAt",
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
			name:       "tap name is not the shared shape",
			mutate:     func(a *Artifact) { a.TapName = "tap7afaa3460" },
			wantPhrase: `tapName "tap7afaa3460" is not a TAP device name`,
		},
		{
			name:       "tap name has an uppercase digest",
			mutate:     func(a *Artifact) { a.TapName = "tap-7AFAA346-0" },
			wantPhrase: "is not a TAP device name",
		},
		{
			name:       "tap name has no interface index",
			mutate:     func(a *Artifact) { a.TapName = "tap-7afaa346" },
			wantPhrase: "is not a TAP device name",
		},
		{
			name:       "tap name belongs to another task",
			mutate:     func(a *Artifact) { a.TapName = "tap-deadbeef-0" },
			wantPhrase: `tapName "tap-deadbeef-0" does not belong to task "task-1"`,
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
			wantPhrase: `stateFile.name "snap-1/vm.state" is not a file name`,
		},
		{
			name:       "state file name carries a control character",
			mutate:     func(a *Artifact) { a.StateFile.Name = "vm.state\n" },
			wantPhrase: "control character",
		},
		{
			name:       "state file name is the parent directory",
			mutate:     func(a *Artifact) { a.StateFile.Name = ".." },
			wantPhrase: "is not a file name",
		},
		{
			name:       "state file name is longer than the kernel allows",
			mutate:     func(a *Artifact) { a.StateFile.Name = strings.Repeat("n", 256) },
			wantPhrase: "at most 255",
		},
		{
			name:       "state file is empty",
			mutate:     func(a *Artifact) { a.StateFile.Size = 0 },
			wantPhrase: "stateFile.size is 0",
		},
		{
			name:       "state file above the size ceiling",
			mutate:     func(a *Artifact) { a.StateFile.Size = MaxFileSize + 1 },
			wantPhrase: "stateFile.size is 68719476737: want at most 68719476736 bytes",
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
			name:       "memory file above the size ceiling",
			mutate:     func(a *Artifact) { a.MemoryFile.Size = MaxFileSize + 1 },
			wantPhrase: "memoryFile.size is 68719476737",
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
		{
			name:       "disk file present but undescribed",
			mutate:     func(a *Artifact) { a.DiskFile = File{Name: "rootfs.ext4"} },
			wantPhrase: "diskFile.size is 0",
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
			name:   "any interface index of the source task",
			mutate: func(a *Artifact) { a.TapName = "tap-7afaa346-2" },
		},
		{
			name:   "single-file name with a dot",
			mutate: func(a *Artifact) { a.StateFile.Name = "vm.state.v1" },
		},
		{
			name:   "snapshot and export at the same instant",
			mutate: func(a *Artifact) { a.SnapshotAt = a.CreatedAt },
		},
		{
			name:   "a file exactly at the size ceiling",
			mutate: func(a *Artifact) { a.MemoryFile.Size = MaxFileSize },
		},
		{
			name:   "no service ID, because the snapshot recorded none",
			mutate: func(a *Artifact) { a.ServiceID = "" },
		},
		{
			name:   "no disk copy, because the snapshot shipped none",
			mutate: func(a *Artifact) { a.DiskFile = File{} },
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

// TestValidFileName pins the one bare-file-name rule. The broker stages and serves
// a file by this name, and the manifest describes one; the two used to hold their
// own copies of the rule, which had already drifted (the manifest accepted a name
// with a newline that the broker refused). The table is the rule: both call sites
// defer to ValidFileName, so there is one place to change and one place to check.
func TestValidFileName(t *testing.T) {
	cases := []struct {
		name string
		// wantProblem is a phrase of the rejection; empty means the name is valid.
		wantProblem string
	}{
		{name: "vm.state"},
		{name: "rootfs.ext4"},
		{name: "a"},
		{name: "vm.state.v1"},
		{name: "-leading-dash"},
		{name: "a name with a space"},
		{name: strings.Repeat("n", 255)},
		{name: "", wantProblem: "empty"},
		{name: ".", wantProblem: "is not a file name"},
		{name: "..", wantProblem: "is not a file name"},
		{name: "snap-1/vm.state", wantProblem: "separator"},
		{name: "/vm.state", wantProblem: "separator"},
		{name: "vm.state/", wantProblem: "separator"},
		{name: `snap-1\vm.state`, wantProblem: "separator"},
		{name: "vm.state\n", wantProblem: "control character"},
		{name: "vm.state\r", wantProblem: "control character"},
		{name: "vm\x00.state", wantProblem: "control character"},
		{name: "vm\tstate", wantProblem: "control character"},
		{name: "vm\x7f.state", wantProblem: "control character"},
		{name: strings.Repeat("n", 256), wantProblem: "at most 255"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidFileName(tc.name)
			if tc.wantProblem == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantProblem)
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
		"createdAt", "snapshotAt", "snapshotId", "tapName", "vsockPath", "diskPath",
		"stateFile", "memoryFile", "diskFile",
	} {
		assert.Contains(t, keys, key)
	}

	var decoded Artifact
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, original, decoded)
	require.NoError(t, decoded.Validate())
}

// TestArtifactJSONOmitsAbsentOptionalFields is the DTO constraint made
// measurable: a field that can legitimately be absent must not be forced onto
// the wire as a zero value, so an older or simpler producer stays compatible.
func TestArtifactJSONOmitsAbsentOptionalFields(t *testing.T) {
	artifact := validArtifact()
	artifact.ServiceID = ""
	artifact.DiskFile = File{}
	require.NoError(t, artifact.Validate())

	data, err := json.Marshal(artifact)
	require.NoError(t, err)

	var keys map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &keys))
	assert.NotContains(t, keys, "serviceId")
	assert.NotContains(t, keys, "diskFile")

	for _, key := range []string{"schemaVersion", "kind", "id", "sourceTaskId", "snapshotAt", "stateFile"} {
		assert.Contains(t, keys, key, "%s is not optional and must always be on the wire", key)
	}
}

// TestArtifactJSONOmitsAnUnsetSnapshotTime proves the omission reaches a
// struct-typed field. encoding/json never omits a struct with omitempty, so the
// time fields carry omitzero; this test is what would catch a regression to a
// tag that only looks like it omits.
func TestArtifactJSONOmitsAnUnsetSnapshotTime(t *testing.T) {
	artifact := validArtifact()
	artifact.SnapshotAt = time.Time{}

	data, err := json.Marshal(artifact)
	require.NoError(t, err)

	var keys map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &keys))
	assert.NotContains(t, keys, "snapshotAt", "an unset instant must not appear as a zero timestamp")

	var decoded Artifact
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.True(t, decoded.SnapshotAt.IsZero())

	err = decoded.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "snapshotAt is zero",
		"an absent instant is refused on the way in, not quietly read as a real time")
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
	snapshotAt time.Time
	stateBytes string
	memBytes   string
	diskBytes  string
}

func newSnapshotFixture(t *testing.T) snapshotFixture {
	t.Helper()

	dir := t.TempDir()
	f := snapshotFixture{
		// The snapshot was taken before the export that describes it, which is
		// the only order a real export can see.
		snapshotAt: time.Now().UTC().Add(-time.Minute),
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
		CreatedAt:  f.snapshotAt,
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

	beforeExport := time.Now().UTC()
	artifact, err := ArtifactFromSnapshot(fixture.info, "/var/run/firecracker", 0)
	require.NoError(t, err)

	assert.True(t, ValidArtifactID(artifact.ID), "id %q is not mig-<16 hex>", artifact.ID)
	assert.Equal(t, v1.SchemaVersion, artifact.SchemaVersion,
		"the artifact follows the v1 envelope's schema major")
	assert.Equal(t, ArtifactKind, artifact.Kind)
	assert.Equal(t, "snap-0123456789abcdef", artifact.SnapshotID,
		"snapshotId must name the dump the artifact was made from, so discard deletes exactly that one")
	assert.Equal(t, "task-1", artifact.SourceTaskID)
	assert.Equal(t, "node-1", artifact.SourceNodeID)
	assert.Equal(t, "service-1", artifact.ServiceID)

	// Two instants, not one: the guest resumes from the dump, while the
	// artifact's own age (and any TTL over it) starts at the export.
	assert.WithinDuration(t, fixture.snapshotAt, artifact.SnapshotAt, time.Millisecond,
		"snapshotAt must be the instant the dump was taken")
	assert.False(t, artifact.CreatedAt.Before(beforeExport),
		"createdAt must be the export time, not the dump time")
	assert.False(t, artifact.CreatedAt.After(time.Now().UTC()),
		"createdAt must not be in the future")

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

// TestArtifactFromSnapshotWritesLiteralHostIdentity pins the two host-side
// names to literal strings. They are the names Firecracker recorded inside the
// state file, so a manifest that disagreed with them restores a VM whose
// devices do not exist. The literals are the rule; recomputing the rule here
// would only prove the expression equals itself.
func TestArtifactFromSnapshotWritesLiteralHostIdentity(t *testing.T) {
	cases := []struct {
		name      string
		socketDir string
		tapIndex  int
		wantTap   string
		wantVsock string
	}{
		{
			name:      "first interface in the default socket directory",
			socketDir: "/var/run/firecracker",
			tapIndex:  0,
			wantTap:   "tap-7afaa346-0",
			wantVsock: "/var/run/firecracker/task-1.vsock",
		},
		{
			name:      "second interface of the same task",
			socketDir: "/var/run/firecracker",
			tapIndex:  1,
			wantTap:   "tap-7afaa346-1",
			wantVsock: "/var/run/firecracker/task-1.vsock",
		},
		{
			name:      "a two-digit interface index",
			socketDir: "/var/run/firecracker",
			tapIndex:  12,
			wantTap:   "tap-7afaa346-12",
			wantVsock: "/var/run/firecracker/task-1.vsock",
		},
		{
			name:      "a jailer-style nested socket directory",
			socketDir: "/srv/jail/task-1/root/run",
			tapIndex:  0,
			wantTap:   "tap-7afaa346-0",
			wantVsock: "/srv/jail/task-1/root/run/task-1.vsock",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newSnapshotFixture(t)

			artifact, err := ArtifactFromSnapshot(fixture.info, tc.socketDir, tc.tapIndex)
			require.NoError(t, err)

			assert.Equal(t, tc.wantTap, artifact.TapName,
				"tap-<first 8 hex of sha256(sourceTaskId)>-<index>")
			assert.Equal(t, tc.wantVsock, artifact.VsockPath,
				"<socketDir>/<sourceTaskId>.vsock")
		})
	}
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

	t.Run("no snapshot ID", func(t *testing.T) {
		fixture := newSnapshotFixture(t)
		fixture.info.ID = ""

		_, err := ArtifactFromSnapshot(fixture.info, "/var/run/firecracker", 0)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidArtifact)
		assert.Contains(t, err.Error(), "is not a snapshot ID",
			"an artifact must name the dump it was made from, so discard can delete exactly that one")
	})

	t.Run("no socket directory", func(t *testing.T) {
		fixture := newSnapshotFixture(t)

		_, err := ArtifactFromSnapshot(fixture.info, "", 0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "socketDir is empty")
	})

	t.Run("relative socket directory", func(t *testing.T) {
		fixture := newSnapshotFixture(t)

		_, err := ArtifactFromSnapshot(fixture.info, "var/run/firecracker", 0)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidArtifact)
		assert.Contains(t, err.Error(), `socketDir "var/run/firecracker" is relative`,
			"the argument that was wrong is socketDir, not the path derived from it")
	})

	t.Run("negative tap index", func(t *testing.T) {
		fixture := newSnapshotFixture(t)

		_, err := ArtifactFromSnapshot(fixture.info, "/var/run/firecracker", -1)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidArtifact)
		assert.Contains(t, err.Error(), "tapIndex is -1")
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

	t.Run("memory file above the size ceiling", func(t *testing.T) {
		fixture := newSnapshotFixture(t)

		// A sparse file: the size is a stat, not bytes on disk, so this costs
		// nothing and proves the ceiling is checked before the file is hashed.
		oversized := filepath.Join(t.TempDir(), "vm.mem")
		require.NoError(t, os.WriteFile(oversized, nil, 0o600))
		require.NoError(t, os.Truncate(oversized, MaxFileSize+1))
		fixture.info.MemoryPath = oversized

		_, err := ArtifactFromSnapshot(fixture.info, "/var/run/firecracker", 0)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidArtifact)
		assert.Contains(t, err.Error(), "68719476737 bytes: want at most 68719476736 bytes")
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

	t.Run("snapshot timestamped in the future", func(t *testing.T) {
		fixture := newSnapshotFixture(t)
		fixture.info.CreatedAt = time.Now().UTC().Add(time.Hour)

		_, err := ArtifactFromSnapshot(fixture.info, "/var/run/firecracker", 0)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidArtifact)
		assert.Contains(t, err.Error(), "is after createdAt")
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
