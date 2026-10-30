// Package migration describes the artifact a live migration moves between nodes:
// the files, their checksums, and the host-side identities the target must
// recreate before Firecracker can load the guest.
//
// It is a leaf: it reads a completed pkg/snapshot snapshot and it does not talk
// to Firecracker, the network or SwarmKit, so the same rules apply on the source
// node, on the target node and in the manager. Nothing here touches a VM; the
// transfer and the restore live in pkg/broker and pkg/snapshot.
package migration

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
)

const (
	// SchemaVersion is the artifact schema major. It only moves on a breaking
	// change to the manifest; adding a field does not change it.
	SchemaVersion = 1

	// ArtifactKind is the document kind of a migration artifact.
	ArtifactKind = "MigrationArtifact"
)

const (
	// artifactIDPrefix and artifactIDBytes mirror the shape pkg/snapshot writes
	// for snapshot IDs, so an artifact ID is one safe path segment and looks
	// like the thing it was made from.
	artifactIDPrefix = "mig-"
	artifactIDBytes  = 8

	// sha256HexLength is the length of a hex-encoded SHA-256 digest.
	sha256HexLength = 64

	// interfaceNameLimit is the kernel's IFNAMSIZ minus the terminating NUL.
	interfaceNameLimit = 15
)

// ErrInvalidArtifact classifies every rejection from this package, so a caller
// can answer one stable code without inspecting a message. The wrapped message
// is the leading phrase, so %w keeps the text readable and errors.Is works.
var ErrInvalidArtifact = errors.New("invalid migration artifact")

var (
	// artifactIDPattern is exactly the shape generateArtifactID produces. The
	// artifact ID reaches a URL path and a store key, so it is checked before
	// any use.
	artifactIDPattern = regexp.MustCompile(`^mig-[0-9a-f]{16}$`)

	// sha256HexPattern is the shape this package writes and accepts: lowercase
	// hex, no "sha256:" prefix, matching pkg/snapshot's checksums.
	sha256HexPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ValidArtifactID reports whether id is a well-formed artifact ID: one safe
// path segment in the shape this package generates.
func ValidArtifactID(id string) bool {
	return artifactIDPattern.MatchString(id)
}

// File describes one file that moves: its name on the wire, its size and its
// SHA-256 digest. The name is a bare file name because the transfer route
// addresses a file by name (POST /v1/migrations/{id}/files/{name}).
type File struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Artifact is the manifest of one migration: what moves, and the host-side
// identity of the VM that produced it.
//
// Host-side identity comes from the snapshot, not from the new task:
// Firecracker's state file records the TAP device name, the vsock UDS path and
// the block-device path of the source VM, and a load only succeeds if those
// exist on the target under exactly those names.
type Artifact struct {
	SchemaVersion int    `json:"schemaVersion"`
	Kind          string `json:"kind"`

	// ID is this artifact's identity: mig-<16 lowercase hex characters>.
	ID string `json:"id"`

	// SourceTaskID is the task the VM ran as on the source node. It is the
	// name the snapshot recorded, so it stays the VM's identity after the move
	// even though SwarmKit starts the workload under a new task ID.
	SourceTaskID string `json:"sourceTaskId"`
	// SourceNodeID is the node the snapshot was taken on.
	SourceNodeID string `json:"sourceNodeId"`
	// ServiceID is the service whose single replica moves. A placement
	// constraint applies to every task of a service, so only a single-replica
	// service can be moved this way.
	ServiceID string `json:"serviceId"`
	// CreatedAt is when the dump was taken: the instant the guest resumes
	// from. Writes the source accepted after it are not carried over.
	CreatedAt time.Time `json:"createdAt"`

	// TapName is the TAP device name the snapshot recorded. The target creates
	// the device under this name, keeps it down while Firecracker loads, and
	// brings it up after the guest re-addresses its interface.
	TapName string `json:"tapName"`
	// VsockPath is the absolute vsock muxer path the snapshot recorded, so the
	// target can dial the guest agent of the restored VM.
	VsockPath string `json:"vsockPath"`
	// DiskPath is the absolute path the snapshot state file records for the
	// block device. The target materialises the artifact's disk bytes here
	// before loading; the new task's own disk path is not used, because the
	// state file resolves the drive by the recorded path.
	DiskPath string `json:"diskPath"`

	// StateFile is the Firecracker VM state, MemoryFile the guest memory image
	// and DiskFile the snapshot-owned disk copy.
	StateFile  File `json:"stateFile"`
	MemoryFile File `json:"memoryFile"`
	DiskFile   File `json:"diskFile"`
}

// Validate reports whether the manifest is complete enough to restore from. It
// returns the first problem, in field order, as a sentence that names the field
// and the valid form; every rejection wraps ErrInvalidArtifact.
//
// It checks the description, not the files: the target verifies the bytes
// against the checksums after the transfer, because a manifest validated on the
// source says nothing about what arrived.
func (a Artifact) Validate() error {
	if a.SchemaVersion != SchemaVersion {
		return invalidf("schemaVersion is %d: want %d, the schema this build reads and writes", a.SchemaVersion, SchemaVersion)
	}
	if a.Kind == "" {
		return invalidf("kind is empty: want %q", ArtifactKind)
	}
	if a.Kind != ArtifactKind {
		return invalidf("kind %q is not %q: a restore refuses another document type", a.Kind, ArtifactKind)
	}
	if a.ID == "" {
		return invalidf("id is empty: want %s followed by 16 lowercase hex characters", artifactIDPrefix)
	}
	if !ValidArtifactID(a.ID) {
		return invalidf("id %q is not a migration artifact ID: want %s followed by 16 lowercase hex characters", a.ID, artifactIDPrefix)
	}
	if a.SourceTaskID == "" {
		return invalidf("sourceTaskId is empty: want the task the snapshot was taken from, because its ID names the TAP device, the vsock path and the disk")
	}
	if a.SourceNodeID == "" {
		return invalidf("sourceNodeId is empty: want the node the snapshot was taken on")
	}
	if a.ServiceID == "" {
		return invalidf("serviceId is empty: want the single-replica service being moved")
	}
	if a.CreatedAt.IsZero() {
		return invalidf("createdAt is zero: want the instant the dump was taken, which is when the guest resumes from")
	}
	if a.TapName == "" {
		return invalidf("tapName is empty: want the TAP device name the snapshot recorded and the target recreates")
	}
	if problem := interfaceNameProblem(a.TapName); problem != "" {
		return invalidf("tapName %q is not a valid interface name: %s", a.TapName, problem)
	}
	if a.VsockPath == "" {
		return invalidf("vsockPath is empty: want the absolute vsock muxer path the snapshot recorded, <socketDir>/<sourceTaskId>.vsock")
	}
	if !filepath.IsAbs(a.VsockPath) {
		return invalidf("vsockPath %q is relative: want the absolute path the snapshot recorded", a.VsockPath)
	}
	if a.DiskPath == "" {
		return invalidf("diskPath is empty: want the absolute block-device path the snapshot state file records")
	}
	if !filepath.IsAbs(a.DiskPath) {
		return invalidf("diskPath %q is relative: want the absolute path the snapshot state file records", a.DiskPath)
	}

	files := []struct {
		field string
		file  File
	}{
		{"stateFile", a.StateFile},
		{"memoryFile", a.MemoryFile},
		{"diskFile", a.DiskFile},
	}
	for _, f := range files {
		if err := validateFile(f.field, f.file); err != nil {
			return err
		}
	}

	return nil
}

// validateFile checks one moving file's description.
func validateFile(field string, f File) error {
	if f.Name == "" {
		return invalidf("%s.name is empty: want the bare name of the file that moves", field)
	}
	if f.Name != filepath.Base(f.Name) || f.Name == "." || f.Name == ".." || strings.ContainsAny(f.Name, `/\`) {
		return invalidf("%s.name %q is a path: want a bare file name, because the transfer route addresses a file by name", field, f.Name)
	}
	if f.Size <= 0 {
		return invalidf("%s.size is %d: want the positive byte count of the file that moves", field, f.Size)
	}
	switch {
	case f.SHA256 == "":
		return invalidf("%s.sha256 is empty: want 64 lowercase hex characters", field)
	case len(f.SHA256) != sha256HexLength:
		return invalidf("%s.sha256 is %d characters: want 64 lowercase hex characters", field, len(f.SHA256))
	case !sha256HexPattern.MatchString(f.SHA256):
		return invalidf("%s.sha256 is not 64 lowercase hex characters: want a hex-encoded SHA-256 digest", field)
	}
	return nil
}

// interfaceNameProblem returns why name is not a usable Linux interface name, or
// "" when it is. Only the kernel's limits are checked; the naming rule itself
// belongs to the VM that wrote the snapshot, and the restore recreates whatever
// name it recorded.
func interfaceNameProblem(name string) string {
	if len(name) > interfaceNameLimit {
		return fmt.Sprintf("it is %d characters and Linux allows at most %d", len(name), interfaceNameLimit)
	}
	if i := strings.IndexAny(name, "/:\x00 \t\n"); i >= 0 {
		return fmt.Sprintf("it contains %q, which an interface name cannot", name[i])
	}
	return ""
}

// invalidf builds a rejection that carries ErrInvalidArtifact.
func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidArtifact, fmt.Sprintf(format, args...))
}

// ArtifactFromSnapshot describes a completed snapshot as a migration artifact.
//
// socketDir is the directory holding the source task's Firecracker API socket
// (<socketDir>/<taskID>.sock): the vsock muxer path is derived from it, and the
// target needs it to reach the guest agent after the load. tapIndex is which
// interface of the source VM moves; the TAP name is derived from the source
// task ID with the same rule the node's network manager uses.
//
// Both inputs are explicit because only the caller (the node's export handler)
// knows them, and neither can be recovered from the snapshot metadata.
//
// The three files are read here: the manifest describes the bytes that move, so
// this is where their sizes and digests come from. It returns a manifest that
// passes Validate, or an error.
func ArtifactFromSnapshot(info *snapshot.SnapshotInfo, socketDir string, tapIndex int) (Artifact, error) {
	if info == nil {
		return Artifact{}, invalidf("source snapshot is missing: want a completed snapshot to describe")
	}
	if info.TaskID == "" {
		return Artifact{}, invalidf("source snapshot has no task ID: want the task the VM ran as, because its ID names the TAP device, the vsock path and the disk")
	}
	if socketDir == "" {
		return Artifact{}, invalidf("socket directory is empty: want the directory holding the source task's Firecracker socket, so the vsock path can be derived as <socketDir>/<sourceTaskId>.vsock")
	}
	switch {
	case info.StatePath == "":
		return Artifact{}, invalidf("source snapshot has no state file: want the Firecracker VM state written by snapshot/create")
	case info.MemoryPath == "":
		return Artifact{}, invalidf("source snapshot has no memory file: want the guest memory image written by snapshot/create")
	case info.DiskPath == "":
		return Artifact{}, invalidf("source snapshot %s has no disk copy: want a snapshot that copied the task disk, because a load resolves the drive from the state file and the running task's disk keeps changing", info.ID)
	case info.RootfsPath == "":
		return Artifact{}, invalidf("source snapshot has no recorded disk path: want the block-device path the state file resolves at load")
	}

	stateFile, err := describeFile("state file", info.StatePath)
	if err != nil {
		return Artifact{}, err
	}
	memoryFile, err := describeFile("memory file", info.MemoryPath)
	if err != nil {
		return Artifact{}, err
	}
	diskFile, err := describeFile("disk file", info.DiskPath)
	if err != nil {
		return Artifact{}, err
	}

	if info.Checksum != "" && !strings.EqualFold(info.Checksum, stateFile.SHA256) {
		return Artifact{}, invalidf("snapshot %s state file checksum %s does not match the snapshot metadata %s: the state file changed after the snapshot was published", info.ID, stateFile.SHA256, info.Checksum)
	}

	id, err := generateArtifactID()
	if err != nil {
		return Artifact{}, err
	}

	artifact := Artifact{
		SchemaVersion: SchemaVersion,
		Kind:          ArtifactKind,
		ID:            id,
		SourceTaskID:  info.TaskID,
		SourceNodeID:  info.NodeID,
		ServiceID:     info.ServiceID,
		CreatedAt:     info.CreatedAt,
		TapName:       scruntime.TapName(info.TaskID, tapIndex),
		VsockPath:     scruntime.VsockPath(socketDir, info.TaskID),
		DiskPath:      info.RootfsPath,
		StateFile:     stateFile,
		MemoryFile:    memoryFile,
		DiskFile:      diskFile,
	}
	if err := artifact.Validate(); err != nil {
		return Artifact{}, err
	}
	return artifact, nil
}

// describeFile gives one file's wire description: its base name, its size and
// the SHA-256 of the bytes that will move.
func describeFile(label, path string) (File, error) {
	stat, err := os.Stat(path)
	if err != nil {
		// Both sentinels are kept: the artifact is invalid, and the underlying
		// reason (a missing file, a permission) stays inspectable.
		return File{}, fmt.Errorf("%w: cannot describe the snapshot %s %s: %w", ErrInvalidArtifact, label, path, err)
	}
	if !stat.Mode().IsRegular() {
		return File{}, invalidf("snapshot %s %s is not a regular file: want the file snapshot/create wrote", label, path)
	}

	sum, err := hashFile(path)
	if err != nil {
		return File{}, fmt.Errorf("%w: cannot checksum the snapshot %s %s: %w", ErrInvalidArtifact, label, path, err)
	}

	return File{Name: filepath.Base(path), Size: stat.Size(), SHA256: sum}, nil
}

// hashFile returns the hex-encoded SHA-256 of a file's contents.
func hashFile(path string) (string, error) {
	//nolint:gosec // the path comes from a snapshot this node owns, not from a request
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// generateArtifactID returns a random ID in the shape ValidArtifactID accepts.
// Time is not part of it: two exports in the same clock tick must still differ.
func generateArtifactID() (string, error) {
	var buf [artifactIDBytes]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("migration artifact: cannot generate an ID: %w", err)
	}
	return artifactIDPrefix + hex.EncodeToString(buf[:]), nil
}
