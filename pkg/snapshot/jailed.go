// The jailed launcher.
//
// DirectLauncher starts Firecracker as the daemon's child, which is fine for a
// development box where the guest code is the operator's own. It is not fine for a
// sandbox: a VMM escape would land on host root, and the guest's disk, memory and
// socket would be reachable by every process on the node under the daemon's
// identity. This is the launcher that puts the same instance behind the jailer: a
// chroot holding only the files that instance needs, a uid of its own, an optional
// network namespace, cgroups, and nothing else of the node's inside the fence.
//
// # The paths are the checkpoint contract
//
// The files a jailed instance opens are named the same way in every jail — the
// RFC's D4: /kernel/vmlinux, /drives/rootfs.ext4, /snapshot/vm.state,
// /snapshot/vm.mem, /run/firecracker/<id>.sock. Firecracker records the paths it
// was booted with in the checkpoint's device state, so a checkpoint is only
// portable if those names are stable: a snapshot taken in one jail then loads in
// another jail, under another uid, on another node. The caller therefore hands this
// launcher the host files (the kernel it booted, the disk, the two checkpoint
// images) and gets back the chroot paths, which are what /snapshot/load is told.
//
// # Staging is a copy
//
// Staging copies the four files into the jail, owned by the jail uid, before the
// VMM starts. The copy is deliberate and its consequence belongs to the next slice:
// the guest writes to the copy, so after a jailed VM has run, the disk inside the
// chroot is newer than the host file the caller passed, and a jail-aware
// CreateSnapshot must copy *that* disk out (SBX-022). Making the copy cheap is
// SBX-023's reflink work. What must not happen here is the launcher silently
// running the guest against a disk it did not stage: a shared backing file would
// make two instances one instance.

package snapshot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/restuhaqza/swarmcracker/pkg/console"
	"github.com/restuhaqza/swarmcracker/pkg/jailer"
	"github.com/restuhaqza/swarmcracker/pkg/runtime"
)

// The chroot paths every jailed instance sees, and the host base of the cgroups the
// jailer creates its per-instance cgroup under.
const (
	jailedKernelPath = "/kernel/vmlinux"
	jailedRootfsPath = "/drives/rootfs.ext4"
	jailedStatePath  = "/snapshot/vm.state"
	jailedMemPath    = "/snapshot/vm.mem"
	jailedRunDir     = "/run/firecracker"

	// jailedVMMLogPath is where Firecracker's own log goes, inside the jail. It
	// cannot be stdout: stdout carries the guest's ttyS0, and mixing the two would
	// put VMM diagnostics into the console an operator reads (and, with a console
	// socket attached, into the guest's serial stream).
	jailedVMMLogPath = "/firecracker.log"

	// jailedCgroupBase is the cgroup v2 hierarchy. The jailer creates
	// <base>/<parent>/<id> and applies the --cgroup values to it.
	jailedCgroupBase = "/sys/fs/cgroup"

	// jailedCgroupVersion is passed to the jailer as "2". The node's cgroups are the
	// unified hierarchy (cgroup v2); the jailer defaults to v1, so the flag is not
	// optional.
	jailedCgroupVersion = "2"

	// jailedBootArgs is what a template boot on this node uses: the serial console
	// on ttyS0 (where the launcher and `vm logs` read it), no PCI, and a guest that
	// panics into a reboot rather than a stalled VM. A restore takes its boot
	// arguments from the checkpoint and ignores this.
	jailedBootArgs = "console=ttyS0 reboot=k panic=1 pci=off"

	// jailedDefaultParentCgroup is what the jailer would use if --parent-cgroup were
	// omitted. Naming it explicitly is what makes the cgroup path a function of the
	// launcher's configuration instead of a jailer default, so Stop can remove the
	// exact directory it created.
	jailedDefaultParentCgroup = "firecracker"

	// jailedStaleJailPIDFile is the file the jailer writes the VMM's pid to, inside
	// the jail root, named after the exec file. It is the only evidence a jail
	// directory belongs to a process that may still be running.
	jailedStaleJailPIDFile = ".pid"

	// jailedMaxSocketPath is one less than the 108 bytes a unix socket path may hold,
	// which bounds how long the chroot base directory may be: the API socket's host
	// path is <chroot>/run/firecracker/<id>.sock, and connect() refuses a longer one
	// with EINVAL — a launch that discovers this only when it dials waits out the
	// socket timeout instead of saying what is wrong.
	jailedMaxSocketPath = 107

	// jailedCgroupSettle is how long a teardown waits for a killed VMM's cgroup to
	// become removable. The process is confirmed gone before this runs, but the
	// kernel can still hold references to the cgroup while it reclaims the VMM's
	// memory.
	jailedCgroupSettle = 2 * time.Second
	jailedCgroupPoll   = 20 * time.Millisecond
)

// The seams one jailed launch is tested through, for the same reason DirectLauncher
// has them: a unit test cannot start a real jailed VMM (that needs KVM, /dev/net/tun
// and root), so it replaces the socket wait and the API calls with recorders.
// Everything between them is still the code under test: the chroot layout, the
// staged files and their ownership, the jailer argv, the uid lease, and the teardown.
// Production never replaces them.
var (
	jailedLookPath       = exec.LookPath
	jailedWaitForAPI     = waitForSocket
	jailedLoadCheckpoint = loadJailedCheckpoint
	jailedResumeGuest    = resumeJailedGuest
	jailedBootGuest      = bootJailedGuest
)

// JailedLauncher starts an instance inside a jailer chroot, as a uid of its own.
//
// It is the production launcher (RFC D3). What it will not do is guess: the
// binaries, the chroot base, the cgroup limits and the uid range are configuration,
// and a request that does not fit the configuration is refused before anything is
// created.
type JailedLauncher struct {
	// ChrootBaseDir is the directory the jailer builds its chroots under: this
	// launcher owns the whole tree below it, and an instance's jail is
	// JailRoot(id) with the chroot itself at JailRoot(id)/root. Required.
	ChrootBaseDir string

	// FirecrackerPath is the firecracker binary the jailer execs into the jail. It
	// is also where the jail directory's name comes from, because the jailer names
	// the chroot after the exec file: <base>/<exec file name>/<id>/root. Empty
	// resolves "firecracker" from PATH.
	FirecrackerPath string

	// JailerPath is the jailer binary. Empty resolves "jailer" from PATH.
	JailerPath string

	// ParentCgroup is the cgroup the jailer creates the instance's cgroup under:
	// <jailedCgroupBase>/<ParentCgroup>/<id>. Empty uses the jailer's own default,
	// named here so that Stop removes the directory this launcher's launches created.
	ParentCgroup string

	// MemoryMaxBytes and PidsMax are the cgroup limits applied to the VMM, written
	// as the jailer's --cgroup <file>=<value> pairs. Zero leaves the limit unset.
	//
	// MemoryMaxBytes is a limit on the whole VMM, which is the guest's memory plus
	// Firecracker's own footprint, so a caller setting it to the guest's memory size
	// is asking for an OOM kill. It is configuration rather than something derived
	// from LaunchRequest.MemoryMB for exactly that reason.
	MemoryMaxBytes int64
	PidsMax        int

	// LogDir is the host directory the instance's serial console is written to:
	// <LogDir>/<InstanceID>.log, the file `vm logs` reads. Empty leaves the console
	// unrecorded.
	LogDir string

	// ConsoleSocketDir is the host directory an interactive console socket is
	// created in: <ConsoleSocketDir>/<InstanceID>.console.sock, the socket
	// `vm attach` dials. Empty gives the guest no console input at all, which is
	// what a launcher with no operator behind it wants.
	ConsoleSocketDir string

	// UIDs is the range of host uids instances run as. Required: a jailed instance
	// must not run as root, and two instances must not share a uid, so the launcher
	// leases one per launch (Allocate for an unspecified uid, Reserve for a named
	// one) and releases it only after the instance's teardown is complete.
	UIDs *jailer.UIDAllocator

	// cgroupRoot is the cgroup hierarchy a teardown removes directories from. It is a
	// field so a test can aim it at a fixture instead of at the host's cgroupfs;
	// production leaves it empty and takes jailedCgroupBase.
	cgroupRoot string
}

// The jailed launcher satisfies the interface, and the assertion is where the build
// fails if the contract drifts rather than at the first caller.
var _ Launcher = JailedLauncher{}

// Start launches one instance inside a jail and returns it running.
//
// A restore and a fork differ only in which disk the caller staged: both load the
// checkpoint and resume the guest. A cold launch boots from the staged kernel and
// disk, which is how a template is built inside the same fence it will later be
// restored into. Either way Start returns only once the guest is running, and a
// failed start leaves no process, socket, cgroup, jail directory or uid lease
// behind.
func (l JailedLauncher) Start(ctx context.Context, req LaunchRequest) (Launched, error) {
	if err := l.check(req); err != nil {
		return Launched{}, err
	}

	j, err := l.newJailed(req)
	if err != nil {
		return Launched{}, err
	}

	// From here on there is a leased uid and possibly a staged jail, and Start
	// returns no handle for a failed launch, so every step that fails has to undo its
	// own work through the same teardown Stop uses.
	if err := j.prepare(ctx); err != nil {
		return Launched{}, j.fail(err)
	}
	if err := j.spawn(); err != nil {
		return Launched{}, j.fail(err)
	}
	if err := jailedWaitForAPI(j.hostSocket, directLaunchSocketTimeout); err != nil {
		return Launched{}, j.fail(fmt.Errorf("firecracker API not ready in the jail: %w", err))
	}
	if err := j.start(ctx, req); err != nil {
		return Launched{}, j.fail(err)
	}

	log.Info().
		Str("instance_id", req.InstanceID).
		Str("mode", string(req.Mode)).
		Int("jail_uid", j.uid).
		Str("jail_root", j.jailRoot).
		Str("socket", j.hostSocket).
		Msg("Instance launched in a jail")

	launched := Launched{
		APISocket: j.hostSocket,
		// A jailed launch configures no vsock device: a resumed instance keeps the
		// muxer path recorded in its checkpoint, which lives inside the jail, and a
		// cold instance has no vsock device at all. Guessing a host path would point
		// the guest agent at a socket nobody listens on, so the caller that knows the
		// device configuration supplies it.
		VsockPath:   "",
		ConsolePath: j.consolePath,
		Stop:        j.stop,
	}
	if req.Mode != ModeCold {
		launched.StatePath = jailedStatePath
		launched.MemPath = jailedMemPath
	}
	return launched, nil
}

// JailRoot returns the host directory that holds instanceID's jail: the chroot the
// VMM runs in is JailRoot(id)/root, and every chroot path resolves beneath it.
//
// It is exported because the layout is the jailer's, not this package's
// (<chroot base dir>/<exec file name>/<id>/root), and a caller that has to reach a
// file the instance staged or wrote — a jail-aware snapshot create, a test — must
// not re-derive it.
func (l JailedLauncher) JailRoot(instanceID string) string {
	return filepath.Join(l.ChrootBaseDir, l.execFileName(), instanceID)
}

// execFileName is the directory the jailer groups one binary's chroots under: the
// base name of the exec file, resolved the same way Start resolves it.
func (l JailedLauncher) execFileName() string {
	name := l.FirecrackerPath
	if name == "" {
		name = "firecracker"
	}
	if resolved, err := jailedLookPath(name); err == nil {
		name = resolved
	}
	return filepath.Base(name)
}

// check refuses a request this launcher cannot serve, before anything is created.
func (l JailedLauncher) check(req LaunchRequest) error {
	switch req.Mode {
	case ModeCold:
		// A cold boot is the template path: the instance is booted from the files
		// this launcher stages, inside the fence its restores will later run in.
	case ModeRestore, ModeFork:
	case "":
		return fmt.Errorf("instance %q: launch mode is empty: want %q, %q or %q",
			req.InstanceID, ModeCold, ModeRestore, ModeFork)
	default:
		return fmt.Errorf("instance %q: unknown launch mode %q: want %q, %q or %q",
			req.InstanceID, req.Mode, ModeCold, ModeRestore, ModeFork)
	}

	// The ID names the instance's socket, its console and its cgroup, so it is
	// checked as a path element before any of them is built. The rule is the node's
	// one rule for "this string can name a file" (runtime.CheckTaskID); the sentinel
	// it returns is what a caller matches, the sentence is this launcher's.
	if err := runtime.CheckTaskID(req.InstanceID); err != nil {
		return fmt.Errorf("instance %q: %w", req.InstanceID, err)
	}
	if l.ChrootBaseDir == "" {
		return fmt.Errorf("instance %q: the jailed launcher has no chroot base directory", req.InstanceID)
	}
	if l.UIDs == nil {
		return fmt.Errorf("instance %q: the jailed launcher has no uid allocator: it must own the range of uids its instances run as", req.InstanceID)
	}
	if req.Rootfs == "" {
		return fmt.Errorf("instance %q: a jailed launch needs a disk to stage at %s", req.InstanceID, jailedRootfsPath)
	}

	// The kernel is needed even for a restore: the checkpoint records the path it
	// was booted with (/kernel/vmlinux in a jail-built checkpoint), and Firecracker
	// reopens that file when it loads the state.
	if req.Kernel == "" {
		return fmt.Errorf("instance %q: a jailed launch needs the kernel to stage at %s", req.InstanceID, jailedKernelPath)
	}

	if req.Mode == ModeCold {
		if req.VCPUs <= 0 || req.MemoryMB <= 0 {
			return fmt.Errorf("instance %q: a cold-booted instance needs a size, got %d vCPUs and %d MiB",
				req.InstanceID, req.VCPUs, req.MemoryMB)
		}
		return nil
	}

	if req.State == "" || req.Mem == "" {
		return fmt.Errorf("instance %q: a %q launch needs both checkpoint images, got state %q and memory %q",
			req.InstanceID, req.Mode, req.State, req.Mem)
	}
	return nil
}

// jailedInstance is one launch in progress or one running instance: everything Stop
// needs, and nothing derived a second time.
type jailedInstance struct {
	instanceID string
	mode       LaunchMode

	uid   int
	gid   int
	lease *jailer.Lease

	chrootBaseDir   string
	firecrackerPath string
	jailerPath      string
	parentCgroup    string
	cgroupRoot      string
	cgroups         []string
	netns           string

	logDir           string
	consoleSocketDir string

	// jailRoot is the host directory that holds this instance's jail; chroot is the
	// directory the VMM is chrooted into.
	jailRoot string
	chroot   string

	// The host files this launch staged, in the request's own namespace.
	kernelSource string
	rootfsSource string
	stateSource  string
	memSource    string

	// hostSocket and socketPath are the same socket seen from the host and from
	// inside the jail. Firecracker creates it at socketPath; the launcher dials it at
	// hostSocket.
	hostSocket string
	socketPath string

	consolePath string

	proc       *os.Process
	consoleSrv *console.Server

	// consoleMu guards logFile, which the reap goroutine closes when the VMM exits
	// and a stop closes when it ends the VMM itself: the two race by construction,
	// and closing a file twice is fine but reading the field twice is not.
	consoleMu sync.Mutex
	logFile   *os.File

	// staged is set once this instance owns the tree at jailRoot: the stale jail its
	// id may have left behind is gone and the files are this launch's. Until then a
	// failure must not remove anything, because the path may belong to a live
	// instance that refused this launch.
	staged bool

	// finished is set once this instance's process is gone and its jail is removed.
	// A retried stop must then do nothing at all: the paths belong to any instance
	// a later launch put under the same id, and a stop that removed them again would
	// take a live VM's socket and disk with it.
	finished bool
}

// newJailed resolves the binaries, leases a uid and computes the jail's paths. It
// touches nothing on disk: a request that cannot get a uid must not leave a
// half-built jail behind, and allocation is also where the node's process table is
// consulted, so it is the step most likely to refuse.
func (l JailedLauncher) newJailed(req LaunchRequest) (*jailedInstance, error) {
	firecrackerPath, err := resolveJailedBinary(l.FirecrackerPath, "firecracker")
	if err != nil {
		return nil, fmt.Errorf("instance %q: %w", req.InstanceID, err)
	}
	jailerPath, err := resolveJailedBinary(l.JailerPath, "jailer")
	if err != nil {
		return nil, fmt.Errorf("instance %q: %w", req.InstanceID, err)
	}

	parentCgroup := l.ParentCgroup
	if parentCgroup == "" {
		parentCgroup = jailedDefaultParentCgroup
	}
	cgroupRoot := l.cgroupRoot
	if cgroupRoot == "" {
		cgroupRoot = jailedCgroupBase
	}

	jailRoot := filepath.Join(l.ChrootBaseDir, filepath.Base(firecrackerPath), req.InstanceID)
	chroot := filepath.Join(jailRoot, "root")
	socketPath := path.Join(jailedRunDir, req.InstanceID+runtime.VMSocketSuffix)
	hostSocket := filepath.Join(chroot, strings.TrimPrefix(socketPath, "/"))

	consolePath := ""
	if l.LogDir != "" {
		consolePath = filepath.Join(l.LogDir, req.InstanceID+".log")
	}

	// The paths a caller dials have to fit in a unix socket address, and the chroot
	// base is the part of them the caller controls. Checking before the uid is leased
	// keeps a refused launch from costing an id.
	for _, hostPath := range []string{hostSocket, l.consoleSocketPath(req.InstanceID)} {
		if hostPath != "" && len(hostPath) > jailedMaxSocketPath {
			return nil, fmt.Errorf("instance %q: the host path %s is %d bytes, more than the %d a unix socket address holds: use a shorter chroot base directory or instance id",
				req.InstanceID, hostPath, len(hostPath), jailedMaxSocketPath)
		}
	}

	lease, err := l.leaseUID(req)
	if err != nil {
		return nil, fmt.Errorf("instance %q: %w", req.InstanceID, err)
	}

	uid := lease.UID()
	gid := req.JailGID
	if gid == 0 {
		// One uid per instance, and the group id follows it unless the caller names
		// another: the jailer's setgid needs a non-root gid, and an instance's own uid
		// is the gid that belongs to nothing else.
		gid = uid
	}

	return &jailedInstance{
		instanceID:       req.InstanceID,
		mode:             req.Mode,
		uid:              uid,
		gid:              gid,
		lease:            lease,
		chrootBaseDir:    l.ChrootBaseDir,
		firecrackerPath:  firecrackerPath,
		jailerPath:       jailerPath,
		parentCgroup:     parentCgroup,
		cgroupRoot:       cgroupRoot,
		cgroups:          l.cgroupLimits(),
		netns:            netnsPath(req.NetNS),
		logDir:           l.LogDir,
		consoleSocketDir: l.ConsoleSocketDir,
		jailRoot:         jailRoot,
		chroot:           chroot,
		kernelSource:     req.Kernel,
		rootfsSource:     req.Rootfs,
		stateSource:      req.State,
		memSource:        req.Mem,
		hostSocket:       hostSocket,
		socketPath:       socketPath,
		consolePath:      consolePath,
	}, nil
}

// consoleSocketPath is the host path of the instance's console socket, or "" when
// the launcher creates none. It is the path pkg/console creates, so the length check
// in newJailed measures the socket that has to fit in a unix address.
func (l JailedLauncher) consoleSocketPath(instanceID string) string {
	if l.ConsoleSocketDir == "" {
		return ""
	}
	return console.SocketPath(l.ConsoleSocketDir, instanceID)
}

// leaseUID takes the instance's uid from the allocator: the one the request names,
// or the lowest free one.
func (l JailedLauncher) leaseUID(req LaunchRequest) (*jailer.Lease, error) {
	if req.JailUID != 0 {
		lease, err := l.UIDs.Reserve(req.JailUID)
		if err != nil {
			return nil, err
		}
		return lease, nil
	}
	return l.UIDs.Allocate()
}

// cgroupLimits renders the configured limits as the jailer's --cgroup pairs. The
// jailer applies each one to the instance's own cgroup, which is why an OOM is
// attributed to the instance rather than to the node.
func (l JailedLauncher) cgroupLimits() []string {
	var limits []string
	if l.MemoryMaxBytes > 0 {
		limits = append(limits, fmt.Sprintf("memory.max=%d", l.MemoryMaxBytes))
	}
	if l.PidsMax > 0 {
		limits = append(limits, fmt.Sprintf("pids.max=%d", l.PidsMax))
	}
	return limits
}

// resolveJailedBinary turns a configured (or default) binary name into an absolute
// path that exists. The jailer needs an absolute exec file — it copies that file
// into the jail — so a name found on $PATH is resolved once, here.
func resolveJailedBinary(configured, fallback string) (string, error) {
	name := configured
	if name == "" {
		name = fallback
	}
	resolved, err := jailedLookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s binary not found: %w", fallback, err)
	}
	if !filepath.IsAbs(resolved) {
		resolved, err = filepath.Abs(resolved)
		if err != nil {
			return "", fmt.Errorf("resolve %s binary %q: %w", fallback, resolved, err)
		}
	}
	if _, err := os.Stat(resolved); err != nil {
		return "", fmt.Errorf("%s binary %s: %w", fallback, resolved, err)
	}
	return resolved, nil
}

// netnsPath turns the request's network namespace into the path the jailer joins:
// a name is a network namespace in /var/run/netns, where `ip netns add` puts them,
// and anything with a separator is already a path.
func netnsPath(netns string) string {
	if netns == "" || strings.ContainsRune(netns, os.PathSeparator) {
		return netns
	}
	return filepath.Join("/var/run/netns", netns)
}

// prepare makes the jail empty and stages the files the instance opens.
func (j *jailedInstance) prepare(ctx context.Context) error {
	if err := j.clearStaleJail(); err != nil {
		return err
	}
	if err := j.clearStaleCgroup(); err != nil {
		return err
	}

	// From here the tree at jailRoot is this launch's: the stale one is gone, so a
	// failure has to remove whatever staging built.
	j.staged = true
	return j.stage(ctx)
}

// clearStaleJail removes a jail a previous instance under this ID left behind.
//
// Removing it is what makes a retried launch after a crash work at all: the jailer
// refuses to build a chroot over an existing one (it creates /dev/net/tun inside
// it), and a socket left there would be dialled as if the new VMM had created it.
// A jail whose VMM is still running is not stale, though: the jailer writes the
// pid inside the jail, so that file is the difference between a leftover and a live
// instance the caller is about to destroy.
func (j *jailedInstance) clearStaleJail() error {
	if _, err := os.Stat(j.jailRoot); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect the jail %s: %w", j.jailRoot, err)
	}

	pid, err := j.jailPID()
	if err != nil {
		return err
	}
	if pid != 0 && processAlive(pid) {
		return fmt.Errorf("instance %q is already running in %s as pid %d: stop it before launching it again",
			j.instanceID, j.jailRoot, pid)
	}

	if err := os.RemoveAll(j.jailRoot); err != nil {
		return fmt.Errorf("remove the stale jail %s: %w", j.jailRoot, err)
	}
	return nil
}

// jailPID reads the pid the jailer wrote for this jail, or 0 when the jail holds no
// pid file.
func (j *jailedInstance) jailPID() (int, error) {
	path := filepath.Join(j.chroot, filepath.Base(j.firecrackerPath)+jailedStaleJailPIDFile)

	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read the jail's pid file %s: %w", path, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(body)))
	if err != nil {
		// An unreadable pid is not a running instance: the jail is unusable either
		// way, and treating it as stale is what lets the next launch proceed.
		return 0, nil
	}
	return pid, nil
}

// clearStaleCgroup removes a cgroup a previous instance left behind, so the jailer
// can create this instance's own. It refuses a cgroup that still holds processes:
// that is a live instance's cgroup, not a leftover, and removing it would move the
// running VMM into another one.
func (j *jailedInstance) clearStaleCgroup() error {
	dir := j.cgroupPath()

	if err := os.Remove(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("remove the cgroup %s a previous instance left behind (an instance may still be running in it): %w", dir, err)
	}
	return nil
}

// cgroupPath is the directory the jailer creates for this instance.
func (j *jailedInstance) cgroupPath() string {
	return filepath.Join(j.cgroupRoot, j.parentCgroup, j.instanceID)
}

// stage copies the instance's kernel, disk and checkpoint images into the jail,
// creating the directories they live in and giving them to the jail uid.
//
// The modes are the ones the VMM needs and nothing more: the kernel is read once, so
// it is not writable, and everything else is the jail uid's alone.
func (j *jailedInstance) stage(ctx context.Context) error {
	files := []jailedFile{
		{src: j.kernelSource, dest: jailedKernelPath, mode: 0o500},
		{src: j.rootfsSource, dest: jailedRootfsPath, mode: 0o600},
	}
	if j.mode != ModeCold {
		files = append(files,
			jailedFile{src: j.stateSource, dest: jailedStatePath, mode: 0o600},
			jailedFile{src: j.memSource, dest: jailedMemPath, mode: 0o600},
		)
	}

	// The run directory holds the API socket Firecracker creates. The snapshot
	// directory is made even for a cold boot: a jailed instance is the one a
	// checkpoint is taken *of*, and Firecracker will not create the directory
	// /snapshot/create writes the images into.
	dirs := []string{jailedRunDir, path.Dir(jailedStatePath)}
	for _, file := range files {
		dirs = append(dirs, path.Dir(file.dest))
	}
	for _, dir := range dirs {
		if err := j.mkdir(dir); err != nil {
			return err
		}
	}

	for _, file := range files {
		if err := j.place(ctx, file); err != nil {
			return err
		}
	}
	return nil
}

// jailedFile is one file a launch stages: where the caller's copy is, where the
// instance sees it, and the mode it gets in the jail.
type jailedFile struct {
	src  string
	dest string
	mode os.FileMode
}

// place copies one source file to its chroot path and hands it to the jail uid.
func (j *jailedInstance) place(ctx context.Context, file jailedFile) error {
	dest := filepath.Join(j.chroot, strings.TrimPrefix(file.dest, "/"))

	if err := copyFile(ctx, file.src, dest); err != nil {
		return fmt.Errorf("stage %s at %s: %w", file.src, file.dest, err)
	}
	// copyFile keeps the source's permission bits; the jail's files are the jail
	// uid's, with only the access the VMM needs.
	if err := os.Chmod(dest, file.mode); err != nil {
		return fmt.Errorf("mode %s in the jail: %w", file.dest, err)
	}
	if err := os.Chown(dest, j.uid, j.gid); err != nil {
		return fmt.Errorf("give %s in the jail to uid %d: %w", file.dest, j.uid, err)
	}
	return nil
}

// mkdir creates one directory inside the jail, owned by the jail uid and closed to
// everyone else. The jail root itself is the jailer's to create and chown, but the
// directories the staged files live in are this launcher's, and a file the VMM
// cannot traverse to is a file it cannot open.
func (j *jailedInstance) mkdir(chrootDir string) error {
	dir := filepath.Join(j.chroot, strings.TrimPrefix(chrootDir, "/"))

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s in the jail: %w", chrootDir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("mode %s in the jail: %w", chrootDir, err)
	}
	if err := os.Chown(dir, j.uid, j.gid); err != nil {
		return fmt.Errorf("give %s in the jail to uid %d: %w", chrootDir, j.uid, err)
	}
	return nil
}

// spawn starts the jailer, which becomes the VMM and inherits the console.
func (j *jailedInstance) spawn() error {
	cmd := exec.Command(j.jailerPath, j.jailerArgv()...) //nolint:gosec // G204: the jailer path and its arguments are this launcher's own configuration.

	logFile, err := j.openConsole()
	if err != nil {
		return err
	}

	if j.consoleSocketDir != "" {
		mirror := io.Writer(io.Discard)
		if logFile != nil {
			mirror = logFile
		}
		srv, err := console.New(console.Config{
			SocketDir: j.consoleSocketDir,
			TaskID:    j.instanceID,
			Mirror:    mirror,
		})
		if err != nil {
			_ = j.closeConsole()
			return fmt.Errorf("create the instance console: %w", err)
		}
		j.consoleSrv = srv
		cmd.Stdin = srv.Stdin()
		cmd.Stdout = srv.Stdout()
	} else if logFile != nil {
		cmd.Stdout = logFile
	}
	if logFile != nil {
		cmd.Stderr = logFile
	}

	if err := cmd.Start(); err != nil {
		if j.consoleSrv != nil {
			j.consoleSrv.Close()
			j.consoleSrv = nil
		}
		_ = j.closeConsole()
		return fmt.Errorf("start jailer: %w", err)
	}

	j.proc = cmd.Process
	if j.consoleSrv != nil {
		j.consoleSrv.Start()
	}
	go j.reap(cmd)

	return nil
}

// reap collects the VMM when it exits, and closes the console log with it. Without
// a Wait the process would stay a zombie under the daemon and the stop path's
// Signal would never report that it is done.
func (j *jailedInstance) reap(cmd *exec.Cmd) {
	if err := cmd.Wait(); err != nil {
		log.Debug().Err(err).Int("pid", cmd.Process.Pid).Str("instance_id", j.instanceID).
			Msg("Jailed firecracker process exited")
	}
	_ = j.closeConsole()
}

// openConsole opens the instance's console log, or returns nil when the launcher
// records no console. The file is the same one `vm logs` reads for a task, so a
// jailed instance is readable like any other.
func (j *jailedInstance) openConsole() (*os.File, error) {
	if j.consolePath == "" {
		return nil, nil
	}
	file, err := openRestoredConsole(j.consolePath)
	if err != nil {
		return nil, fmt.Errorf("open the instance console log: %w", err)
	}

	j.consoleMu.Lock()
	j.logFile = file
	j.consoleMu.Unlock()

	return file, nil
}

// closeConsole closes the console log if this launcher opened one. Closing twice is
// harmless: the reap and the stop path both want it closed, and whichever runs
// second finds nothing to do.
func (j *jailedInstance) closeConsole() error {
	j.consoleMu.Lock()
	defer j.consoleMu.Unlock()

	if j.logFile == nil {
		return nil
	}
	err := j.logFile.Close()
	j.logFile = nil
	return err
}

// jailerArgv is the jailer invocation, and the reason each argument is there is
// worth reading before changing one.
//
// The jailer sends --id to Firecracker itself, so Firecracker must not be given
// another one (it exits 153). The VMM's log goes to a file inside the jail, because
// its stdout is the guest's serial console. Everything after -- is Firecracker's.
func (j *jailedInstance) jailerArgv() []string {
	args := []string{
		"--id", j.instanceID,
		"--exec-file", j.firecrackerPath,
		"--uid", strconv.Itoa(j.uid),
		"--gid", strconv.Itoa(j.gid),
		"--chroot-base-dir", j.chrootBaseDir,
		"--cgroup-version", jailedCgroupVersion,
		"--parent-cgroup", j.parentCgroup,
	}

	for _, limit := range j.cgroups {
		args = append(args, "--cgroup", limit)
	}
	if j.netns != "" {
		args = append(args, "--netns", j.netns)
	}

	return append(args,
		"--",
		"--api-sock", j.socketPath,
		"--level", "Info",
		"--log-path", jailedVMMLogPath,
	)
}

// start asks the instance to enter the world: a cold boot configures the guest and
// starts it, a checkpoint is loaded and then resumed.
func (j *jailedInstance) start(ctx context.Context, req LaunchRequest) error {
	if req.Mode == ModeCold {
		if err := jailedBootGuest(ctx, j.hostSocket, req); err != nil {
			return fmt.Errorf("boot the instance in its jail: %w", err)
		}
		return nil
	}

	// resume_vm=false, then an explicit resume: a snapshot loaded and resumed in one
	// call leaves no window in which the guest is loaded but stopped, and a restore
	// that has to change something first (a per-instance drive, MMDS) needs that
	// window. InstanceStart is not it: Firecracker answers 400 there, and
	// PATCH /vm {"state":"Resumed"} is the call that actually runs the guest.
	if err := jailedLoadCheckpoint(ctx, j.hostSocket, jailedStatePath, jailedMemPath); err != nil {
		return fmt.Errorf("load the checkpoint into the jail: %w", err)
	}
	if err := jailedResumeGuest(ctx, j.hostSocket); err != nil {
		return fmt.Errorf("resume the guest: %w", err)
	}
	return nil
}

// fail ends a launch that did not finish and returns the error that stopped it.
func (j *jailedInstance) fail(cause error) error {
	if err := j.stop(context.Background()); err != nil {
		log.Warn().Err(err).Str("instance_id", j.instanceID).
			Msg("Failed to tear down an instance whose launch failed")
	}
	return cause
}

// stop ends the instance and hands back everything it held: the process, its
// console, the socket, the cgroup, the jail directory and the uid lease. It is
// idempotent — every step tolerates work another call already did — because a
// teardown is retried.
//
// The order is the point. The process goes first, because while it runs it owns the
// socket and holds the cgroup; the uid lease goes last, because a uid may only be
// handed to another instance once nothing of this one is left. If the process
// cannot be stopped, nothing is removed and the lease stays: the socket, cgroup and
// jail are then the only handles to a VM that is still running.
func (j *jailedInstance) stop(ctx context.Context) error {
	if j.finished {
		return nil
	}

	if err := stopLaunchProcess(ctx, j.proc); err != nil {
		if j.consoleSrv != nil {
			j.consoleSrv.Close()
			j.consoleSrv = nil
		}
		return err
	}

	if j.consoleSrv != nil {
		j.consoleSrv.Close()
		j.consoleSrv = nil
	}
	_ = j.closeConsole()

	if !j.staged {
		// This launch never got as far as owning the tree: whatever is at jailRoot
		// belongs to a live instance that refused it, and removing the socket, cgroup
		// or jail by name would take that instance's resources with it.
		j.lease.Release()
		j.proc = nil
		return nil
	}

	j.collectVMMLog()

	var errs []error
	if err := removeIfPresent(j.hostSocket); err != nil {
		errs = append(errs, fmt.Errorf("remove the instance socket %s: %w", j.hostSocket, err))
	}
	if err := removeJailedCgroup(j.cgroupPath()); err != nil {
		errs = append(errs, err)
	}
	// The jailer creates the instance's parent cgroup too, so a node that runs one
	// instance at a time would collect an empty parent per launch. Removing it is best
	// effort: it fails while another instance still lives in it, which is exactly when
	// it must stay.
	_ = os.Remove(filepath.Dir(j.cgroupPath()))
	if err := os.RemoveAll(j.jailRoot); err != nil {
		errs = append(errs, fmt.Errorf("remove the jail %s: %w", j.jailRoot, err))
	} else {
		j.finished = true
	}

	j.lease.Release()
	j.proc = nil

	return errors.Join(errs...)
}

// collectVMMLog copies Firecracker's own log out of the jail before the jail is
// removed. It is the only place a VMM that died inside the fence says why, and the
// jail is the one directory the teardown does delete.
func (j *jailedInstance) collectVMMLog() {
	if j.logDir == "" {
		return
	}
	src := filepath.Join(j.chroot, strings.TrimPrefix(jailedVMMLogPath, "/"))

	body, err := os.ReadFile(src)
	if err != nil {
		return // a VMM that never started wrote no log
	}
	dest := filepath.Join(j.logDir, j.instanceID+".firecracker.log")
	if err := os.WriteFile(dest, body, 0o644); err != nil {
		log.Warn().Err(err).Str("path", dest).Msg("Failed to keep a jailed VMM's log")
	}
}

// removeIfPresent removes one file, treating an absent one as done.
func removeIfPresent(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// removeJailedCgroup removes the instance's cgroup, waiting out the kernel's own
// references to it. A cgroup that is still populated is an error: it means a process
// is still in it, and silently leaving it behind would leak a cgroup per instance.
func removeJailedCgroup(dir string) error {
	deadline := time.Now().Add(jailedCgroupSettle)

	for {
		err := os.Remove(dir)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("remove the instance cgroup %s: %w", dir, err)
		}
		time.Sleep(jailedCgroupPoll)
	}
}

// bootJailedGuest configures and starts a cold guest inside the jail: the kernel and
// disk it boots from, its size, and InstanceStart. Each file is named by its chroot
// path, so the checkpoint Firecracker later writes records paths that load in any
// jail.
func bootJailedGuest(ctx context.Context, socketPath string, req LaunchRequest) error {
	calls := []struct {
		path    string
		payload map[string]interface{}
	}{
		{
			path: "/boot-source",
			payload: map[string]interface{}{
				"kernel_image_path": jailedKernelPath,
				"boot_args":         jailedBootArgs,
			},
		},
		{
			path: "/drives/rootfs",
			payload: map[string]interface{}{
				"drive_id":       "rootfs",
				"path_on_host":   jailedRootfsPath,
				"is_root_device": true,
				"is_read_only":   false,
			},
		},
		{
			path: "/machine-config",
			payload: map[string]interface{}{
				"vcpu_count":        req.VCPUs,
				"mem_size_mib":      req.MemoryMB,
				"smt":               false,
				"track_dirty_pages": false,
			},
		},
		{
			path:    "/actions",
			payload: map[string]interface{}{"action_type": "InstanceStart"},
		},
	}

	for _, call := range calls {
		if err := putFirecrackerAPI(ctx, socketPath, call.path, call.payload); err != nil {
			return fmt.Errorf("configure %s: %w", call.path, err)
		}
	}
	return nil
}

// loadJailedCheckpoint loads the checkpoint with the guest left stopped, so the
// caller has a window before the guest runs.
//
// The memory backend is named explicitly as a File: without it Firecracker has to
// infer the backend from a path, and a checkpoint written by a newer version may
// carry a backend a path alone cannot express.
func loadJailedCheckpoint(ctx context.Context, socketPath, statePath, memPath string) error {
	payload := map[string]interface{}{
		"snapshot_path": statePath,
		"mem_backend": map[string]string{
			"backend_path": memPath,
			"backend_type": "File",
		},
		"resume_vm": false,
	}
	return putFirecrackerAPI(ctx, socketPath, "/snapshot/load", payload)
}

// resumeJailedGuest runs a loaded guest. It is a PATCH of the VM state, not
// InstanceStart: Firecracker refuses InstanceStart once a snapshot is loaded.
func resumeJailedGuest(ctx context.Context, socketPath string) error {
	return patchFirecrackerAPI(ctx, socketPath, "/vm", map[string]string{"state": "Resumed"})
}
