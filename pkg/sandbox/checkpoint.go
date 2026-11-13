package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/checkpoint"
	"github.com/restuhaqza/swarmcracker/pkg/runtime"
)

// Checkpoints: what a sandbox can be forked from.
//
// A checkpoint is the one object in this package that outlives the sandbox it was
// taken from (RFC §5.4: a Checkpoint is immutable, shared by "forks of one sandbox").
// It is four files and a manifest:
//
//   - the Firecracker state document (vm.state), kilobytes, which names the devices
//     and the kernel the guest was running;
//   - the guest's memory image, stored in the D11 format — fixed 4 MiB
//     content-addressed chunks in pkg/checkpoint's local store, described by a
//     manifest that carries the Firecracker and kernel pins;
//   - the disk the guest had been writing, copied while the guest was frozen, which
//     is what makes a fork a fork rather than a second name for one disk;
//   - the guest's per-boot agent token, which a resumed guest still requires: the
//     fork's memory is the parent's memory, so the credential is the parent's too.
//
// The first three are images; the fourth is a secret, which is why the checkpoint
// directory is 0700 and each file in it is 0600. The RFC's B11 says checkpoints carry
// secrets and must be encrypted before they leave a node; this build keeps them on the
// node (the local D11 driver) and says so rather than pretending otherwise.
//
// # Names, and why a checkpoint is node-global
//
// A checkpoint's name is its identity, one safe path element, and it is unique on the
// node rather than inside one sandbox: `fork --checkpoint c1` is a command an operator
// types without first remembering which sandbox `c1` belongs to, and a checkpoint whose
// sandbox was removed still has to be reachable by the name it was given. The record
// keeps its parent's ID so the sandbox-scoped routes can resolve it, and so a fork can
// report what it came from.

// DirNameCheckpoints is the subdirectory of a node's state directory that holds
// sandbox checkpoints. It is beside DirName (the sandbox records) and separate from
// pkg/checkpoint's own root, so the two layouts cannot be confused: records here, the
// D11 chunk store there.
const DirNameCheckpoints = "sandbox-checkpoints"

// CheckpointRecord is one sandbox checkpoint on this node.
//
// Everything in it is what a fork needs and nothing it could look up elsewhere: the
// images' paths, the identity the guest had (so a fork starts from a machine that was
// really running), and the network policy the parent was created with (so a fork
// reaches exactly what its parent could).
type CheckpointRecord struct {
	// Name is the checkpoint's identity and its directory's name.
	Name string `json:"name"`
	// Sandbox is the sandbox this was taken from. It may no longer exist.
	Sandbox string `json:"sandbox"`
	// Template is the sealed template the sandbox was created from, by ref.
	Template string `json:"template"`
	// Kernel is the kernel image the sandbox was booting, as this node's paths name
	// it. A fork boots it again.
	Kernel string `json:"kernel"`
	// VCPUs and MemoryMB are the size the sandbox had. A fork keeps them: the memory
	// image is exactly this much memory.
	VCPUs    int `json:"vcpus"`
	MemoryMB int `json:"memoryMB"`
	// Egress is the parent's egress allowlist, as it was requested. A fork installs
	// the same policy, so a clone reaches what its parent could and nothing more.
	Egress []string `json:"egress,omitempty"`
	// Owner is who asked for the sandbox, kept so a fork can inherit it.
	Owner string `json:"owner,omitempty"`

	// Created is when the checkpoint was taken.
	Created time.Time `json:"created"`
	// AgentVersion and BootID are what the parent guest answered when it was
	// checkpointed: a fork resumes that agent, and the pair says which one.
	AgentVersion string `json:"agentVersion,omitempty"`
	BootID       string `json:"bootId,omitempty"`

	// Quiesce is the guest's own report of what it did to its filesystems before the
	// images were taken. A checkpoint is only as consistent as this says, so it is
	// recorded rather than assumed.
	Quiesce QuiesceOutcome `json:"quiesce"`

	// Mem is the memory image's manifest: size, chunk digests and the pins it is
	// valid against (D11). The chunks live in the node's checkpoint store.
	Mem checkpoint.Manifest `json:"mem"`
	// State and Disk are this checkpoint's own copies, as host paths.
	State string `json:"state"`
	Disk  string `json:"disk"`
	// DiskCopy is how the disk was copied: reflink, sparse or full.
	DiskCopy string `json:"diskCopy,omitempty"`
	// Token is the guest's per-boot agent credential, as the resumed guest still
	// requires it. The file is 0600 in a 0700 directory.
	Token string `json:"token"`
	// SizeBytes is what the checkpoint occupies: the state document, the memory
	// image's chunks and the disk copy.
	SizeBytes int64 `json:"sizeBytes"`
}

// QuiesceOutcome is the guest's report of the freeze that preceded a checkpoint.
type QuiesceOutcome struct {
	// Mode names the mechanism the guest used, or is empty for a checkpoint taken
	// without one — which is a checkpoint whose consistency is unknown.
	Mode string `json:"mode,omitempty"`
	// Mounts are the filesystems it froze, in its own order.
	Mounts []string `json:"mounts,omitempty"`
}

// validate refuses a checkpoint record a fork could not use. It is the rule the store
// applies on write and again on read, for the same reason a sandbox record has one: a
// file a human edited, or one an older build wrote, must not be handed to a fork.
func (c CheckpointRecord) validate() error {
	if err := runtime.CheckTaskID(c.Name); err != nil {
		return fmt.Errorf("checkpoint name %q cannot name one: %w", c.Name, err)
	}
	if strings.TrimSpace(c.Sandbox) == "" {
		return errors.New("a checkpoint names the sandbox it was taken from")
	}
	if strings.TrimSpace(c.Template) == "" || strings.TrimSpace(c.Kernel) == "" {
		return errors.New("a checkpoint names its template and the kernel its guest was running")
	}
	if c.State == "" || c.Disk == "" {
		return errors.New("a checkpoint names its state document and its disk copy")
	}
	if c.Token == "" {
		return errors.New("a checkpoint carries the guest's agent token: a resumed guest still requires it")
	}
	if err := c.Mem.Validate(); err != nil {
		return fmt.Errorf("memory image: %w", err)
	}
	return nil
}

// CheckpointStore is the node's checkpoints, as records on disk: one directory per
// checkpoint, one record file inside it. It is written the way the sandbox store is
// written — a unique temporary, fsync, rename, fsync of the directory — so a reader
// sees a whole record or none, and it assumes one writing process (the node's daemon).
type CheckpointStore struct {
	dir string
}

// NewCheckpointStore opens — creating when needed — the checkpoint directory, and
// proves it can be written to while the daemon can still refuse to start.
func NewCheckpointStore(dir string) (*CheckpointStore, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, errors.New("sandbox checkpoint store: a directory is required")
	}
	dir = filepath.Clean(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("sandbox checkpoint store: create %s: %w", dir, err)
	}
	probe, err := os.CreateTemp(dir, ".checkpoint-probe-*")
	if err != nil {
		return nil, fmt.Errorf("sandbox checkpoint store: %s is not writable: %w", dir, err)
	}
	name := probe.Name()
	_ = probe.Close()
	if err := os.Remove(name); err != nil {
		return nil, fmt.Errorf("sandbox checkpoint store: remove the probe %s: %w", name, err)
	}
	return &CheckpointStore{dir: dir}, nil
}

// Dir is the directory the checkpoints live in.
func (s *CheckpointStore) Dir() string { return s.dir }

// Path is the directory one checkpoint owns: <dir>/<name>.
func (s *CheckpointStore) Path(name string) string { return filepath.Join(s.dir, name) }

// Put writes one checkpoint's record, replacing any record already there. A name that
// is already a checkpoint is refused by the caller that allocates it, not here: Put is
// what a checkpoint's own creation calls last, and a retried create must be able to
// finish a half-written one.
func (s *CheckpointStore) Put(c CheckpointRecord) error {
	if err := c.validate(); err != nil {
		return fmt.Errorf("checkpoint %q: %w", c.Name, err)
	}
	dir := s.Path(c.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("checkpoint %q: create %s: %w", c.Name, dir, err)
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("checkpoint %q: encode: %w", c.Name, err)
	}
	path := filepath.Join(dir, "checkpoint.json")
	tmp, err := os.CreateTemp(dir, ".checkpoint-*.tmp")
	if err != nil {
		return fmt.Errorf("checkpoint %q: create temporary file: %w", c.Name, err)
	}
	tmpName := tmp.Name()
	discard := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	if err := tmp.Chmod(0o600); err != nil {
		discard()
		return fmt.Errorf("checkpoint %q: mode %s: %w", c.Name, tmpName, err)
	}
	if _, err := tmp.Write(data); err != nil {
		discard()
		return fmt.Errorf("checkpoint %q: write %s: %w", c.Name, tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		discard()
		return fmt.Errorf("checkpoint %q: sync %s: %w", c.Name, tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("checkpoint %q: close %s: %w", c.Name, tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("checkpoint %q: rename into place: %w", c.Name, err)
	}
	syncDir(dir)
	return nil
}

// Get returns one checkpoint's record. found is false for a name this node has never
// recorded; a record that exists but cannot be used is an error, and the file is left
// in place so a human can read what the node believes.
func (s *CheckpointStore) Get(name string) (CheckpointRecord, bool, error) {
	if err := runtime.CheckTaskID(name); err != nil {
		return CheckpointRecord{}, false, err
	}
	data, err := os.ReadFile(filepath.Join(s.Path(name), "checkpoint.json"))
	if errors.Is(err, os.ErrNotExist) {
		return CheckpointRecord{}, false, nil
	}
	if err != nil {
		return CheckpointRecord{}, false, fmt.Errorf("checkpoint %q: read: %w", name, err)
	}
	var rec CheckpointRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return CheckpointRecord{}, false, fmt.Errorf("checkpoint %q: decode: %w", name, err)
	}
	if rec.Name != name {
		return CheckpointRecord{}, false, fmt.Errorf("checkpoint %s names %q: one record is one checkpoint",
			s.Path(name), rec.Name)
	}
	if err := rec.validate(); err != nil {
		return CheckpointRecord{}, false, fmt.Errorf("checkpoint %q: %w", name, err)
	}
	return rec, true, nil
}

// List returns every checkpoint on the node, sorted by name, plus the names whose
// record could not be read. One bad record must not hide the node's other checkpoints.
func (s *CheckpointStore) List() ([]CheckpointRecord, []string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, nil, fmt.Errorf("sandbox checkpoint store: read %s: %w", s.dir, err)
	}

	var out []CheckpointRecord
	var skipped []string
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		rec, found, err := s.Get(entry.Name())
		if err != nil {
			skipped = append(skipped, filepath.Join(s.dir, entry.Name()))
			continue
		}
		if !found {
			continue
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	sort.Strings(skipped)
	return out, skipped, nil
}

// Delete removes one checkpoint: its record and every file it owns. The memory image's
// chunks are the checkpoint store's to reclaim, because a chunk is shared between
// checkpoints and has no single owner (pkg/checkpoint.Prune exists for that).
func (s *CheckpointStore) Delete(name string) error {
	if err := runtime.CheckTaskID(name); err != nil {
		return err
	}
	if err := os.RemoveAll(s.Path(name)); err != nil {
		return fmt.Errorf("checkpoint %q: delete: %w", name, err)
	}
	syncDir(s.dir)
	return nil
}
