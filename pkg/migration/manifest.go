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

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
)

const (
	// ArtifactKind is the document kind of a migration artifact. It is
	// deliberately not a pkg/api/v1 kind: the artifact is an internal
	// manager↔node document, so its kind lives with the document, not with the
	// public contract.
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

	// maxFileNameBytes is the kernel's NAME_MAX. A file name is one directory
	// entry, and a longer one cannot be created on the node that must stage it,
	// so the rule refuses what the transfer could not have produced.
	maxFileNameBytes = 255

	// interfaceNameLimit is the kernel's IFNAMSIZ minus the terminating NUL.
	interfaceNameLimit = 15

	// tapNamePrefixLength is the part of a TAP name that identifies its task:
	// "tap-", eight hex characters of the task-ID digest, and the "-" that
	// introduces the interface index. The index itself is not checked, because
	// the manifest does not carry it.
	tapNamePrefixLength = len("tap-") + 8 + 1

	// MaxFileSize is the largest file one artifact may describe: 64 GiB. It is
	// a ceiling on what the transfer will accept, not a guess at the largest
	// guest: it caps how much a single node can be made to read, hash, ship and
	// write for one migration, so a nonsense size in a manifest is refused
	// before it becomes an allocation.
	//
	// It is exported because the transport needs the same number twice: this
	// package refuses a manifest that describes a larger file, and the broker's
	// stage operation must bound a body before any manifest exists on the target.
	// One constant, two enforcement points, so the two can never drift.
	MaxFileSize int64 = 64 << 30
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

	// tapNamePattern is the shape runtime.TapName produces:
	// tap-<8 lowercase hex characters>-<index>.
	tapNamePattern = regexp.MustCompile(`^tap-[0-9a-f]{8}-[0-9]+$`)
)

// ValidArtifactID reports whether id is a well-formed artifact ID: one safe
// path segment in the shape this package generates.
func ValidArtifactID(id string) bool {
	return artifactIDPattern.MatchString(id)
}

// ValidFileName reports whether name is one bare file name: the rule for an
// artifact file wherever it is named, in the manifest that describes it and in the
// routes that serve and stage it.
//
// It is the single copy of the rule on purpose. The manifest and the node's
// transport each used to hold one, and the two had already disagreed: the broker
// refused a newline and an over-long name that the manifest accepted, so a
// manifest could describe a file the transport would never move. The union is
// what applies, because both failures are real: a separator (or "." / "..") would
// address something other than this directory's entry, a control character would
// end up in a header or a log line, and a name past NAME_MAX cannot exist on the
// node that stages it.
//
// The rejection is a sentence that says what is wrong with this name, so both
// call sites can put it in front of their own caller.
func ValidFileName(name string) error {
	switch {
	case name == "":
		return errors.New("it is empty")
	case name == "." || name == "..":
		return fmt.Errorf("%q is not a file name", name)
	case len(name) > maxFileNameBytes:
		return fmt.Errorf("it is %d bytes and a file name may be at most %d", len(name), maxFileNameBytes)
	case strings.ContainsAny(name, `/\`):
		return errors.New("a file name has no path separator")
	case containsControlByte(name):
		return errors.New("a file name has no control character")
	case name != filepath.Base(name):
		return errors.New("a file name is one directory entry")
	}
	return nil
}

// containsControlByte reports whether s holds a C0 or DEL byte: a name that does
// would be truncated, escaped or hidden wherever it is printed.
func containsControlByte(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
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
// It is an internal DTO between the manager and a node, not a public v1
// document. It travels only over the mTLS manager↔node relay, never on a
// public route, which is why it may carry node-local paths: the target needs
// them to recreate the TAP device, the vsock muxer and the disk the snapshot
// state file recorded. A public route that ever needs to describe an artifact
// starts with a pkg/api/v1 wrapper built from this type rather than serving it.
//
// It follows the v1 envelope conventions where they exist (schemaVersion from
// v1.SchemaVersion, a kind, and optional fields omitted when absent), so the
// two document families read the same way.
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

	// SnapshotID is the snapshot record the artifact's files were published
	// from, on the source node: snap-<16 lowercase hex characters>. It travels
	// so the node that discards the artifact deletes exactly that dump, rather
	// than guessing which snapshot behind the task the artifact came from (a
	// task may have been dumped more than once, and the second dump is not this
	// artifact's).
	SnapshotID string `json:"snapshotId"`

	// SourceTaskID is the task the VM ran as on the source node. It is the
	// name the snapshot recorded, so it stays the VM's identity after the move
	// even though SwarmKit starts the workload under a new task ID.
	SourceTaskID string `json:"sourceTaskId"`
	// SourceNodeID is the node the snapshot was taken on.
	SourceNodeID string `json:"sourceNodeId"`
	// ServiceID is the service whose single replica moves, when the snapshot
	// recorded one. A placement constraint applies to every task of a service,
	// so only a single-replica service can be moved this way; a snapshot with
	// no service (a VM started outside SwarmKit) leaves the field out.
	ServiceID string `json:"serviceId,omitempty"`
	// CreatedAt is when the export produced this manifest: the artifact's own
	// creation instant, and therefore the instant any TTL over the artifact
	// counts from. It is not the dump instant; see SnapshotAt.
	CreatedAt time.Time `json:"createdAt"`
	// SnapshotAt is when the dump was taken: the instant the guest resumes
	// from. Writes the source accepted after it are not carried over. It is
	// never after CreatedAt, which is what keeps a TTL from being measured
	// from the dump instead of from the artifact.
	//
	// omitzero, not omitempty: encoding/json never omits a struct with
	// omitempty, so the tag would look right and do nothing.
	SnapshotAt time.Time `json:"snapshotAt,omitzero"`

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

	// StateFile is the Firecracker VM state and MemoryFile the guest memory
	// image; both are always described.
	StateFile  File `json:"stateFile"`
	MemoryFile File `json:"memoryFile"`
	// DiskFile is the snapshot-owned disk copy. A snapshot that shipped no disk
	// copy leaves it out entirely; when it is present it must be described in
	// full. A restore cannot use an artifact without it (the guest's
	// filesystem would not move), which the target checks, not this type.
	//
	// omitzero for the reason given on SnapshotAt.
	DiskFile File `json:"diskFile,omitzero"`
}

// Validate reports whether the manifest is complete enough to travel. It
// returns the first problem, in field order, as a sentence that names the field
// and the valid form; every rejection wraps ErrInvalidArtifact.
//
// It checks the description, not the files: the target verifies the bytes
// against the checksums after the transfer, because a manifest validated on the
// source says nothing about what arrived.
func (a Artifact) Validate() error {
	if err := a.validateEnvelope(); err != nil {
		return err
	}
	if err := a.validateHostIdentity(); err != nil {
		return err
	}
	return a.validateFiles()
}

// validateEnvelope checks the document's own fields: what it is, which artifact
// it is, and whether its two instants agree.
func (a Artifact) validateEnvelope() error {
	if a.SchemaVersion != v1.SchemaVersion {
		return invalidf("schemaVersion is %d: want %d, the schema this build reads and writes", a.SchemaVersion, v1.SchemaVersion)
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
	if a.SnapshotID == "" {
		return invalidf("snapshotId is empty: want the snapshot the artifact was published from, so discarding it deletes exactly that dump")
	}
	if !snapshot.ValidSnapshotID(a.SnapshotID) {
		return invalidf("snapshotId %q is not a snapshot ID: want snap- followed by 16 lowercase hex characters", a.SnapshotID)
	}
	if a.SourceTaskID == "" {
		return invalidf("sourceTaskId is empty: want the task the snapshot was taken from, because its ID names the TAP device, the vsock path and the disk")
	}
	if a.SourceNodeID == "" {
		return invalidf("sourceNodeId is empty: want the node the snapshot was taken on")
	}
	if a.CreatedAt.IsZero() {
		return invalidf("createdAt is zero: want when the export produced this manifest, which is what a TTL over the artifact counts from")
	}
	if a.SnapshotAt.IsZero() {
		return invalidf("snapshotAt is zero: want the instant the dump was taken, which is when the guest resumes from")
	}
	if a.SnapshotAt.After(a.CreatedAt) {
		return invalidf("snapshotAt %s is after createdAt %s: want the dump instant, which cannot follow the export that describes it",
			a.SnapshotAt.Format(time.RFC3339Nano), a.CreatedAt.Format(time.RFC3339Nano))
	}
	return nil
}

// validateHostIdentity checks the three names the target must recreate. The TAP
// name is checked against the one naming rule in pkg/runtime rather than
// against its own copy of it.
func (a Artifact) validateHostIdentity() error {
	if a.TapName == "" {
		return invalidf("tapName is empty: want the TAP device name the snapshot recorded and the target recreates")
	}
	if problem := interfaceNameProblem(a.TapName); problem != "" {
		return invalidf("tapName %q is not a valid interface name: %s", a.TapName, problem)
	}
	if !tapNamePattern.MatchString(a.TapName) {
		return invalidf("tapName %q is not a TAP device name: want tap-<8 lowercase hex characters of sha256(sourceTaskId)>-<interface index>, the name the node's network manager creates", a.TapName)
	}
	if want := tapNameTaskPrefix(a.SourceTaskID); a.TapName[:tapNamePrefixLength] != want {
		return invalidf("tapName %q does not belong to task %q: want the prefix %q, the first 8 hex characters of the task ID's SHA-256, because the snapshot recorded this task's device",
			a.TapName, a.SourceTaskID, want)
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
	return nil
}

// validateFiles checks the descriptions of the bytes that move.
func (a Artifact) validateFiles() error {
	files := []struct {
		field string
		file  File
		// optional files may be absent altogether; a partially described
		// optional file is still rejected, because half a description cannot
		// be verified.
		optional bool
	}{
		{"stateFile", a.StateFile, false},
		{"memoryFile", a.MemoryFile, false},
		{"diskFile", a.DiskFile, true},
	}
	for _, f := range files {
		if f.optional && f.file == (File{}) {
			continue
		}
		if err := validateFile(f.field, f.file); err != nil {
			return err
		}
	}

	return nil
}

// tapNameTaskPrefix returns the part of a TAP device name that identifies its
// task: "tap-<8 hex characters>-". It is derived from the one naming rule in
// pkg/runtime, so a change there cannot leave this check behind.
func tapNameTaskPrefix(taskID string) string {
	return scruntime.TapName(taskID, 0)[:tapNamePrefixLength]
}

// Validate reports whether the description is complete enough to move: a bare
// file name, a positive size within MaxFileSize, and a hex-encoded SHA-256. It is
// the one rule for one file's description, used by the manifest's own field
// checks and by the transport that has to verify those bytes on the way in and on
// the way out; field names in the manifest's rejections come from
// validateFiles, which labels the field it is checking.
func (f File) Validate() error {
	return validateFile("file", f)
}

// validateFile checks one moving file's description.
func validateFile(field string, f File) error {
	if f.Name == "" {
		return invalidf("%s.name is empty: want the bare name of the file that moves", field)
	}
	if err := ValidFileName(f.Name); err != nil {
		return invalidf("%s.name %q is not a file name: %v", field, f.Name, err)
	}
	if f.Size <= 0 {
		return invalidf("%s.size is %d: want the positive byte count of the file that moves", field, f.Size)
	}
	if f.Size > MaxFileSize {
		return invalidf("%s.size is %d: want at most %d bytes (64 GiB), the largest file a migration carries", field, f.Size, MaxFileSize)
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
// "" when it is. Only the kernel's limits are checked here; the shape of a TAP
// name is checked against the naming rule separately.
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
// this is where their sizes and digests come from. CreatedAt is the export
// instant (now) and SnapshotAt is the snapshot's own instant, so a TTL over the
// artifact is not measured from the dump. It returns a manifest that passes
// Validate, or an error.
func ArtifactFromSnapshot(info *snapshot.SnapshotInfo, socketDir string, tapIndex int) (Artifact, error) {
	if info == nil {
		return Artifact{}, invalidf("source snapshot is missing: want a completed snapshot to describe")
	}
	if info.TaskID == "" {
		return Artifact{}, invalidf("source snapshot has no task ID: want the task the VM ran as, because its ID names the TAP device, the vsock path and the disk")
	}
	if !snapshot.ValidSnapshotID(info.ID) {
		return Artifact{}, invalidf("source snapshot ID %q is not a snapshot ID: want snap- followed by 16 lowercase hex characters, because the artifact names the dump it was published from", info.ID)
	}
	if socketDir == "" {
		return Artifact{}, invalidf("socketDir is empty: want the absolute directory holding the source task's Firecracker socket, so the vsock path can be derived as <socketDir>/<sourceTaskId>.vsock")
	}
	if !filepath.IsAbs(socketDir) {
		return Artifact{}, invalidf("socketDir %q is relative: want the absolute directory holding the source task's Firecracker socket", socketDir)
	}
	if tapIndex < 0 {
		return Artifact{}, invalidf("tapIndex is %d: want the zero-based index of a network interface of the source VM", tapIndex)
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
		SchemaVersion: v1.SchemaVersion,
		Kind:          ArtifactKind,
		ID:            id,
		SnapshotID:    info.ID,
		SourceTaskID:  info.TaskID,
		SourceNodeID:  info.NodeID,
		ServiceID:     info.ServiceID,
		CreatedAt:     time.Now().UTC(),
		SnapshotAt:    info.CreatedAt,
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
	// Checked before the file is read: the ceiling exists so a nonsense size
	// is refused instead of hashed, shipped and written.
	if stat.Size() > MaxFileSize {
		return File{}, invalidf("snapshot %s %s is %d bytes: want at most %d bytes (64 GiB), the largest file a migration carries", label, path, stat.Size(), MaxFileSize)
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
