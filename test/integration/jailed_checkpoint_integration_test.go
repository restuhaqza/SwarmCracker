//go:build integration

package integration

// SBX-070 host evidence, step one: does snapshot.CreateSnapshot's JailRoot path —
// the one no production caller has used yet — really work when it is reached the
// way the control plane reaches it, through pkg/sandbox.Manager.Checkpoint?
//
// What this file proves, and only this: a sandbox built by the node's own wiring
// (pkg/sandboxd, the assembly cmd/sandboxd maps its flags onto) reaches ready, and
// Manager.Checkpoint publishes a checkpoint of it whose state document, memory
// image in the D11 format and disk copy all exist on the host, taken from a guest
// that was frozen first and released after.
//
// It deliberately does not fork, restore, or fsck anything: those are the next
// step of the slice, and this run asks one question about one number.
//
// The template it checkpoints is an *unmodified* build of
// recipes/sandbox-base.yaml:
//
//	swarmcracker template build recipes/sandbox-base.yaml --output-dir <dir>
//
// Nothing in the artifact or its sidecar is edited here. The sidecar is the
// builder's own claim about the image — the recipe digest, the pin, and the
// capabilities the agent inside it advertised on its first boot — and a run that
// corrected a field first would be proving something about this file instead of
// about the recipe. The copy this run stages is checked against the sidecar's
// recorded SHA-256, so the bytes under test are the bytes the builder packed.
//
// What it is built to tell apart is a chroot-relative failure. A jailed checkpoint
// asks Firecracker to write /snapshot/vm.state and /snapshot/vm.mem *inside its
// chroot* and reads the disk back from /drives/rootfs.ext4, then copies both
// images out of the jail into the snapshot's own directory. A wrong jail root, a
// jail that never created /snapshot, or a copy that resolves the host's names
// instead of the jail's shows up here rather than in production.
//
// Prerequisites: Linux, root, /dev/kvm, firecracker and jailer in PATH, iproute2,
// nftables, and the sealed template described above. Name the artifact's path in
// the environment; the checkpoint requires a guest whose agent advertises
// `quiesce`, which this file refuses rather than works around, because a template
// built before that operation existed is not a template this node can checkpoint.
//
//	SWARMCRACKER_TEST_TEMPLATE=/var/tmp/sbx-fork-golden/golden-sandbox-base-1.0.0.ext4 \
//	go test -tags=integration -run JailedCheckpoint -v ./test/integration/
//
// SWARMCRACKER_TEST_SCRATCH moves the working directory (default /var/tmp), and
// SWARMCRACKER_TEST_KEEP=1 keeps it after a run, where the jail's logs, the
// VMM's own log and the guest's console are worth reading.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/restuhaqza/swarmcracker/pkg/checkpoint"
	"github.com/restuhaqza/swarmcracker/pkg/config"
	"github.com/restuhaqza/swarmcracker/pkg/golden"
	// This package already declares a "sandbox" type (the netns fixture), so the
	// package is aliased rather than shadowed.
	sandboxpkg "github.com/restuhaqza/swarmcracker/pkg/sandbox"
	"github.com/restuhaqza/swarmcracker/pkg/sandboxd"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
	"github.com/restuhaqza/swarmcracker/pkg/vminstance"
)

const (
	// checkpointSandboxID is the sandbox this run checkpoints. It is also the
	// network namespace name (netpolicy names one sbx-<id>), the jail directory
	// and the instance record key, so the namespace this run creates is
	// sbx-fork-ev1 and nothing else on the node answers to it.
	checkpointSandboxID = "fork-ev1"

	// checkpointName is the checkpoint's name: one safe path element, and the
	// directory it owns under the checkpoint store.
	checkpointName = "sbx-fork-c1"

	// checkpointCgroup is the parent cgroup the jailer creates this sandbox's
	// cgroup under. It is named after this test so a left-over cgroup is
	// recognisable, and so it cannot collide with a node's own.
	checkpointCgroup = "sbx-fork"

	// checkpointVCPUs and checkpointMemoryMB are the guest's size. Memory is what
	// the memory image must be, so the run asserts the whole of it was chunked.
	checkpointVCPUs    = 2
	checkpointMemoryMB = 512
)

// TestIntegration_JailedCheckpoint is the run: build the node's manager from
// values, create a sandbox from the sealed template, checkpoint it, and check
// what the host now holds.
func TestIntegration_JailedCheckpoint(t *testing.T) {
	requireSandboxHost(t)

	ctx := context.Background()
	node := newSliceNode(t)

	// The create path the control plane serves, with the template this run staged
	// The create path the control plane serves, with the template this run staged
	// and an ID nothing else on the node uses.
	rec, err := node.manager.Create(ctx, sandboxpkg.CreateRequest{
		Template: node.templateRef,
		ID:       checkpointSandboxID,
		VCPUs:    checkpointVCPUs,
		MemoryMB: checkpointMemoryMB,
		// Never reaped: no reaper is running here, and a TTL would be a number
		// nothing reads.
		TTL: -1,
	})
	require.NoError(t, err, "create the sandbox the checkpoint is taken of")
	t.Cleanup(func() {
		if _, err := node.manager.Remove(context.Background(), rec.ID); err != nil {
			t.Logf("removing sandbox %s after the run: %v", rec.ID, err)
		}
	})
	t.Logf("sandbox ready: id=%s state=%s netns=%s disk=%s jail_root=%s agent=%s",
		rec.ID, rec.State, rec.NetNS, rec.Disk, rec.JailRoot, rec.AgentVersion)

	// The number this run exists for.
	chk, err := node.manager.Checkpoint(ctx, rec.ID, checkpointName)
	require.NoError(t, err, "Manager.Checkpoint: the jail-aware snapshot create, reached the way the control plane reaches it")
	t.Cleanup(func() {
		if err := node.checkpoints.Delete(checkpointName); err != nil {
			t.Logf("deleting checkpoint %s after the run: %v", checkpointName, err)
		}
	})

	evidence(t, chk, node.snapshotsDir)
}

// evidence reports what a published checkpoint is made of, and refuses a
// checkpoint that is not what the operation promises: images taken with the
// guest's writers stopped, a memory image the size of the guest's memory, a state
// document and a disk copy that are really there, and a snapshot working copy the
// publish moved rather than left behind.
func evidence(t *testing.T, chk sandboxpkg.CheckpointRecord, snapshotDir string) {
	t.Helper()

	t.Logf("checkpoint published: name=%s sandbox=%s template=%s kernel=%s",
		chk.Name, chk.Sandbox, chk.Template, chk.Kernel)
	t.Logf("quiesce: mode=%s mounts=%v", chk.Quiesce.Mode, chk.Quiesce.Mounts)
	t.Logf("state=%s disk=%s size_bytes=%d", chk.State, chk.Disk, chk.SizeBytes)
	t.Logf("memory: format=%d chunk_size=%d chunks=%d total_bytes=%d",
		chk.Mem.FormatVersion, chk.Mem.ChunkSize, len(chk.Mem.Chunks), chk.Mem.TotalSize)
	// The token is the guest's credential, not a path: it is reported by its
	// presence and its length, and its value is never printed.
	t.Logf("agent token carried: %d characters (value not printed)", len(chk.Token))

	for _, file := range []string{chk.State, chk.Disk} {
		info, err := os.Stat(file)
		require.NoError(t, err, "a checkpoint names a file that exists: %s", file)
		require.Positive(t, info.Size(), "%s is not empty", file)
		t.Logf("%s is %d bytes", file, info.Size())
	}

	// The checkpoint's own directory carries a guest credential, so it is closed to
	// everyone but the daemon.
	dir, err := os.Stat(filepath.Dir(chk.State))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), dir.Mode().Perm(), "the checkpoint directory is 0700")
	require.NotEmpty(t, chk.Token, "the checkpoint carries the credential a resumed guest still requires")

	require.Equal(t, agent.QuiesceModeFreeze, chk.Quiesce.Mode,
		"the images were taken with the guest's writers stopped")
	require.NotEmpty(t, chk.Quiesce.Mounts, "the guest named the filesystems it froze")
	require.Equal(t, int64(checkpointMemoryMB)<<20, chk.Mem.TotalSize,
		"the memory image is the whole memory the guest was given")
	require.NotEmpty(t, chk.Mem.Chunks, "the memory image is chunked, not one blob")
	require.NoError(t, chk.Mem.Validate(), "the manifest describes an image a fork could load")

	// The record a fork resolves the checkpoint through. Its shape is the
	// CheckpointRecord type's — this run edits no field of it and does not touch
	// pkg/sandbox — so it is read back from disk and held to exactly that type:
	// every key present is a key the record declares, and every key a fork needs
	// is present.
	record := filepath.Join(filepath.Dir(chk.State), "checkpoint.json")
	body, err := os.ReadFile(record)
	require.NoError(t, err, "the checkpoint's record is beside the images it names")
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &fields))

	declared := map[string]bool{}
	recordType := reflect.TypeOf(chk)
	for i := 0; i < recordType.NumField(); i++ {
		tag := strings.Split(recordType.Field(i).Tag.Get("json"), ",")[0]
		declared[tag] = tag != "" && tag != "-"
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
		require.True(t, declared[key], "record field %q is one CheckpointRecord declares", key)
	}
	sort.Strings(keys)
	for _, key := range []string{"name", "sandbox", "template", "kernel", "vcpus", "memoryMB",
		"created", "quiesce", "mem", "state", "disk", "token", "sizeBytes"} {
		require.Contains(t, keys, key, "the record carries %q for a fork", key)
	}
	t.Logf("checkpoint record %s: %d fields %v", record, len(keys), keys)

	// The working copy the snapshot manager made is what the checkpoint was
	// published from, and publishCheckpoint moves the images out of it. What it
	// must not leave is a snapshot directory that also names those images: one
	// checkpoint in two indexes is not one checkpoint.
	entries, err := os.ReadDir(snapshotDir)
	require.NoError(t, err)
	for _, entry := range entries {
		require.NotRegexp(t, `^snap-[0-9a-f]{16}$`, entry.Name(),
			"the working copy the checkpoint was published from is gone")
	}
	t.Logf("snapshot store %s holds %d entries after the publish", snapshotDir, len(entries))
}

// sliceNode is the node one host-evidence run builds for itself: the manager the
// daemon's own assembly (pkg/sandboxd) produces from values, the stores it wrote
// to, and a reader for the instance registry a clone's VMM pid lives in.
//
// It exists once so the checkpoint run and the fork run cannot drift apart: a
// fork proven on a differently wired manager would be evidence about the fixture.
type sliceNode struct {
	manager *sandboxpkg.Manager
	// checkpoints and snapshotsDir are where a checkpoint lands, and the working
	// copies the snapshot manager makes on the way. blobs is the chunk store the
	// memory image lives in, which is what a run reads an image back out of.
	checkpoints  *sandboxpkg.CheckpointStore
	snapshotsDir string
	blobs        checkpoint.Store
	// stateDir holds the sandbox records, the checkpoint store and the registry.
	stateDir string
	// instances reads the registry the launcher publishes into: the host's own
	// answer about which process is a sandbox's VMM.
	instances *vminstance.Store
	// templateRef is the template staged from the built artifact, as the sidecar
	// names it.
	templateRef string

	// The paths this node's parts live under. They are kept because one run — the
	// negative control — has to reach them itself: it resumes a checkpoint through
	// the launcher rather than the manager, which is the only way to leave the
	// release out, and a launcher is configured with paths, not with a manager.
	goldenDir string
	rootfsDir string
	logDir    string
	jailBase  string
	kernel    string
}

// newSliceNode builds the node with everything under one scratch directory of its
// own, whose every runtime name starts with sbx-fork-.
func newSliceNode(t *testing.T) *sliceNode {
	t.Helper()

	scratch := checkpointScratch(t)
	stateDir := filepath.Join(scratch, "state")
	goldenDir := filepath.Join(scratch, "golden")
	rootfsDir := filepath.Join(scratch, "rootfs")
	logDir := filepath.Join(scratch, "logs")
	jailBase := filepath.Join(scratch, "jails")
	snapshotsDir := filepath.Join(scratch, "snapshots")

	checkpoints, err := sandboxpkg.NewCheckpointStore(filepath.Join(stateDir, "checkpoints"))
	require.NoError(t, err)
	blobs, err := checkpoint.NewLocal(checkpoint.LocalConfig{Root: filepath.Join(stateDir, "blobs")})
	require.NoError(t, err)
	snapshots, err := snapshot.NewManager(snapshot.SnapshotConfig{SnapshotDir: snapshotsDir})
	require.NoError(t, err)

	// The registry the manager publishes into, opened a second time so a run can
	// read what the launcher wrote: a sandbox's VMM pid, its socket and the
	// namespace its devices are in.
	instances, err := vminstance.NewStore(vminstance.Config{Dir: vminstance.Dir(stateDir)})
	require.NoError(t, err)

	// The node's own assembly, from values: the same function cmd/sandboxd calls
	// with the values its flags produced. A manager this file assembled itself
	// would be evidence about this file.
	kernelPath := testAsset(t, "SWARMCRACKER_TEST_KERNEL", "/usr/share/firecracker/vmlinux")
	manager, tokens, err := sandboxd.New(sandboxd.Config{
		StateDir:        stateDir,
		GoldenDir:       goldenDir,
		KernelPath:      kernelPath,
		KernelProfiles:  config.DefaultKernelProfiles(),
		RootfsDir:       rootfsDir,
		LogDir:          logDir,
		JailerPath:      lookPath(t, "jailer"),
		JailerChrootDir: jailBase,
		JailerUIDFirst:  jailedTestUIDBase,
		JailerUIDSize:   8,
		ParentCgroup:    checkpointCgroup,
		VMMPidsMax:      64,
		VCPUs:           checkpointVCPUs,
		MemoryMB:        checkpointMemoryMB,
		DefaultTTL:      0,
		ReadyTimeout:    90 * time.Second,
		Checkpoints:     checkpoints,
		CheckpointBlobs: blobs,
		Snapshots:       snapshots,
	})
	require.NoError(t, err, "build the node's sandbox manager the way sandboxd builds it")
	require.NotNil(t, tokens)

	return &sliceNode{
		manager:      manager,
		checkpoints:  checkpoints,
		snapshotsDir: snapshotsDir,
		blobs:        blobs,
		stateDir:     stateDir,
		instances:    instances,
		templateRef:  stageCheckpointTemplate(t, goldenDir),
		goldenDir:    goldenDir,
		rootfsDir:    rootfsDir,
		logDir:       logDir,
		jailBase:     jailBase,
		kernel:       kernelPath,
	}
}

// materializeCheckpointImages writes a checkpoint's two images out as files, which
// is the form a launcher takes: the chunk store is the durable form, and a VMM reads
// files. It is what a fork does internally (sandbox.Manager.stageCheckpointImages),
// done here because the negative control hands the images to the launcher itself.
func materializeCheckpointImages(t *testing.T, node *sliceNode, chk sandboxpkg.CheckpointRecord) (state, mem string) {
	t.Helper()

	dir := t.TempDir()
	state, mem = filepath.Join(dir, "vm.state"), filepath.Join(dir, "vm.mem")

	stateFile, err := os.Open(chk.State)
	require.NoError(t, err, "open the checkpoint's state document")
	defer func() { _ = stateFile.Close() }()

	// Read verifies the state document against the manifest by consuming it, so the
	// copy below is written from a rewound handle.
	image, err := checkpoint.Read(context.Background(), node.blobs, chk.Mem, stateFile)
	require.NoError(t, err, "read the checkpoint's memory image out of the chunk store")
	defer func() { _ = image.Close() }()
	_, err = stateFile.Seek(0, io.SeekStart)
	require.NoError(t, err)

	copyTo := func(path string, src io.Reader) {
		dst, err := os.Create(path)
		require.NoError(t, err)
		_, err = io.Copy(dst, src)
		require.NoError(t, err)
		require.NoError(t, dst.Close())
	}
	copyTo(state, stateFile)
	copyTo(mem, image)
	return state, mem
}

// checkpointScratch makes the directory this run owns. It is not t.TempDir(): the
// images are hundreds of megabytes and /tmp is usually a tmpfs. The name is short
// because the instance's API socket lives inside its chroot, and that path has to
// fit the 108 bytes a unix socket address holds.
func checkpointScratch(t *testing.T) string {
	t.Helper()

	base := os.Getenv("SWARMCRACKER_TEST_SCRATCH")
	if base == "" {
		base = "/var/tmp"
	}
	dir, err := os.MkdirTemp(base, "sbx-fork-")
	require.NoError(t, err, "make this run's scratch directory under %s", base)
	if os.Getenv("SWARMCRACKER_TEST_KEEP") == "" {
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
	} else {
		t.Logf("scratch kept at %s", dir)
	}
	return dir
}

// stageCheckpointTemplate copies the template this run checkpoints into its own
// golden directory, verbatim: the artifact the builder packed and the sidecar the
// builder wrote, unedited (see the file header for why). The copy is held to the
// sidecar's own SHA-256, so the bytes under test are the bytes the build produced.
//
// It returns the reference the create asks for — "name@version" as the sidecar
// names them, so the run uses the template that was built rather than one this
// file expected.
//
// It also refuses a template whose recorded agent does not advertise `quiesce`.
// A checkpoint freezes its guest's filesystems before it takes its images, so a
// template built before that operation existed is not one this node can
// checkpoint; finding that out here is the answer, and working around it in the
// artifact would hide the very thing the recipe is supposed to prove.
func stageCheckpointTemplate(t *testing.T, goldenDir string) string {
	t.Helper()

	source := checkpointTemplateSource(t)
	sidecar := strings.TrimSuffix(source, ".ext4") + ".json"
	built, err := golden.ReadMetadata(sidecar)
	require.NoError(t, err, "read the built template's sidecar")

	require.NoError(t, os.MkdirAll(goldenDir, 0o755))
	artifact := filepath.Join(goldenDir, filepath.Base(source))
	require.NoError(t, copySparse(source, artifact))
	require.Equal(t, built.SHA256, fileSHA256Hex(t, artifact),
		"the staged copy is the artifact the builder packed")

	body, err := os.ReadFile(sidecar)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(goldenDir, filepath.Base(sidecar)), body, 0o600))

	require.NoError(t, golden.CheckAgentPin(built.Ref(), built.Agent),
		"this host can drive the agent the template carries")
	require.True(t, built.Agent.Supports(agent.CapQuiesce),
		"template %s carries a guest agent that advertises %s, which a checkpoint requires",
		built.Ref(), agent.CapQuiesce)

	t.Logf("staged template %s from %s: sha256=%s agent=%s caps=%v pin=firecracker %s kernel %s",
		built.Ref(), source, built.SHA256, built.Agent.Version, built.Agent.Caps,
		built.Pin.Firecracker, built.Pin.Kernel)
	return built.Ref()
}

// checkpointTemplateSource is the artifact this run stages: the path the
// environment names. There is no default, because the only default this host has
// is the sealed template in the node's golden directory, which may predate the
// agent this build needs — a run that quietly fell back to it would fail at the
// freeze with a message about the guest rather than about the template the
// operator meant to test.
func checkpointTemplateSource(t *testing.T) string {
	t.Helper()

	const env = "SWARMCRACKER_TEST_TEMPLATE"
	path := os.Getenv(env)
	if path == "" {
		t.Skipf("%s must name a template built from recipes/sandbox-base.yaml by this build", env)
	}
	require.FileExists(t, path, "%s names a missing artifact", env)
	require.FileExists(t, strings.TrimSuffix(path, ".ext4")+".json", "the built artifact's sidecar")
	return path
}

// fileSHA256Hex is a file's SHA-256, in the form the sidecar records.
func fileSHA256Hex(t *testing.T, path string) string {
	t.Helper()

	file, err := os.Open(path)
	require.NoError(t, err)
	defer func() { _ = file.Close() }()

	hash := sha256.New()
	_, err = io.Copy(hash, file)
	require.NoError(t, err)
	return hex.EncodeToString(hash.Sum(nil))
}

// A compile-time check that the values this run passes to sandboxd.Config are the
// ones the manager asks for, so a change to either is a build failure here rather
// than a surprise in a host run.
var (
	_ sandboxpkg.Snapshotter = (*snapshot.Manager)(nil)
	_ checkpoint.Store       = (*checkpoint.Local)(nil)
)
