package agent

// The `quiesce` operation: stopping the guest's writers before a checkpoint (RFC D9,
// SBX-087).
//
// A checkpoint is two images taken at one instant: the guest's memory and its disk
// file. A guest that keeps writing between the two produces a pair that disagrees —
// the memory says a file was extended, the disk copy says it was not — and a fork of
// that pair boots a filesystem it has to repair. So the node stops the guest's
// writers first, takes both images while nothing can write, and releases them after.
//
// # The mechanism, and why this one
//
// `freeze` syncs the guest's filesystems and then freezes every writable filesystem
// that is backed by a block device, with the same ioctl (`FIFREEZE`) `fsfreeze(8)`
// uses. A frozen filesystem has no dirty pages: the kernel has flushed them and
// blocks new writers until `thaw` (`FITHAW`). That is the property a checkpoint
// needs, and it is stronger than stopping processes one by one: a process that would
// write blocks inside its write, and a process that has not been started yet blocks
// on its first write too.
//
// The RFC's task line names "cgroup freeze" as the mechanism. This build freezes the
// filesystems instead, for two reasons: the sandbox templates ship no workload cgroup
// for the agent to freeze (a sandbox's processes are whatever its caller started), and
// a cgroup freeze alone leaves the page cache dirty — it stops new writes without
// flushing the ones already made, which is exactly the disagreement above. The
// blocking effect the RFC asks for ("the guest stops writing") is what FIFREEZE
// provides; the report says which mechanism ran, so a caller never has to assume.
//
// # What it does not guarantee
//
//   - A process inside a write system call is blocked, not undone: its write either
//     happened before the freeze or happens after the thaw, never half-way in the
//     image. What it does before that point is not the agent's business.
//   - Writers that bypass the filesystem are not stopped by it: a raw block-device
//     write, an in-flight DMA, or a filesystem the guest did not mount read-write.
//   - Freezing is not a snapshot. It stops writes for as long as it is held, so a
//     caller that freezes and then fails to thaw leaves the guest's writers blocked
//     until it does. The node's checkpoint path defers the thaw for that reason.
//   - A filesystem that does not implement the ioctl is reported as a failure rather
//     than skipped: a checkpoint whose consistency is unknown is not one this node
//     should take.
//
// # Idempotence
//
// `freeze` on a frozen guest and `thaw` on a thawed one succeed and say so
// (`already`), which is the RFC's exit criterion for this op: a retried checkpoint
// must not fail because the first attempt got as far as freezing.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// Quiesce actions. The op set is closed to nine operations (RFC D9), so freeze and
// thaw are two directions of one operation rather than a tenth capability: they are
// the same decision about the same filesystems, taken and released.
const (
	// QuiesceFreeze flushes the guest's dirty pages and blocks its writers.
	QuiesceFreeze = "freeze"
	// QuiesceThaw releases the writers a freeze blocked.
	QuiesceThaw = "thaw"
)

// fifreeze and fithaw are Linux's filesystem-freeze ioctls (uapi/linux/fs.h):
// _IOWR('X', 119, int) and _IOWR('X', 120, int). x/sys/unix does not carry them, and
// the encoding is identical on every Linux architecture because the size field is
// the width of an int.
const (
	fifreeze = 0xc0045877
	fithaw   = 0xc0045878
)

// QuiesceReport is what the guest carries out and reports: which filesystems it
// froze (or released), by which mechanism, and whether it was already in that state.
//
// It is a report rather than a bare acknowledgement because the node records it
// beside a checkpoint: "this image was taken with the guest's writers blocked" is a
// fact about the image, and a checkpoint taken without it should be visible as such
// rather than indistinguishable.
type QuiesceReport struct {
	// Action is what the guest did: "freeze" or "thaw".
	Action string `json:"action"`
	// Mode names the mechanism. This build has one — "fsfreeze" — and names it so a
	// later build that adds another does not change the meaning of an old report.
	Mode string `json:"mode"`
	// Mounts are the mount points whose writers are now blocked (freeze) or were
	// released (thaw), in the order the guest mounted them.
	Mounts []string `json:"mounts,omitempty"`
	// Already reports that the guest was already frozen (or already thawed), which
	// is what makes a retried checkpoint succeed.
	Already bool `json:"already,omitempty"`
}

// QuiesceModeFreeze is the report's name for the mechanism this build uses.
const QuiesceModeFreeze = "fsfreeze"

// The seams a test replaces. The real ones touch the kernel, and a unit test cannot
// freeze the filesystem it is running on.
var (
	// guestMounts is where the guest's mount table is read from.
	guestMounts = "/proc/mounts"
	// syncFilesystems flushes dirty pages to the block device.
	syncFilesystems = func() { unix.Sync() }
	// freezeFilesystem blocks a mount's writers; already reports a filesystem that
	// was frozen by someone else.
	freezeFilesystem = freezeMount
	// thawFilesystem releases a mount's writers; already reports a filesystem that
	// was not frozen.
	thawFilesystem = thawMount
)

// quiesceGuest carries out one quiesce action and reports what it did. An action
// this build does not implement is refused here rather than guessed at, because the
// wrong direction leaves a checkpoint inconsistent (freeze) or a guest's writers
// blocked (thaw).
func quiesceGuest(ctx context.Context, action string) (QuiesceReport, error) {
	if err := ctx.Err(); err != nil {
		return QuiesceReport{}, err
	}
	if action != QuiesceFreeze && action != QuiesceThaw {
		return QuiesceReport{}, fmt.Errorf(
			"quiesce: action %q is neither %q nor %q", action, QuiesceFreeze, QuiesceThaw)
	}

	mounts, err := writableDeviceMounts(guestMounts)
	if err != nil {
		return QuiesceReport{}, err
	}
	if len(mounts) == 0 {
		// A guest whose filesystems are all read-only or in memory has nothing to
		// freeze, and nothing a checkpoint could catch half-written. Saying so is
		// better than reporting a success that froze nothing.
		return QuiesceReport{}, fmt.Errorf(
			"quiesce: no writable filesystem backed by a block device in %s", guestMounts)
	}

	if action == QuiesceFreeze {
		// Sync first: a freeze flushes too, but a guest that is killed between the
		// two must still leave a filesystem that replays cleanly, and a caller that
		// only syncs pays for one flush rather than two.
		syncFilesystems()
	}

	report := QuiesceReport{Action: action, Mode: QuiesceModeFreeze}
	already := 0
	for _, mount := range mounts {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		release := thawFilesystem
		if action == QuiesceFreeze {
			release = freezeFilesystem
		}
		was, err := release(mount)
		if err != nil {
			return report, fmt.Errorf("quiesce %s: %w", mount, err)
		}
		if was {
			already++
		}
		report.Mounts = append(report.Mounts, mount)
	}
	report.Already = already == len(mounts)
	return report, nil
}

// thawAll releases every writable, block-device-backed filesystem, and tolerates a
// guest that has none.
//
// It is what the post-restore hook calls before it writes anything: a resumed guest's
// image carries the freeze that was taken when it was checkpointed, so the hook's first
// write would block until something released it. A guest whose filesystems are all
// read-only or in memory has nothing to release, which is a success with nothing to
// report rather than a failure.
func thawAll(ctx context.Context) (QuiesceReport, error) {
	mounts, err := writableDeviceMounts(guestMounts)
	if err != nil {
		return QuiesceReport{}, err
	}
	if len(mounts) == 0 {
		return QuiesceReport{Action: QuiesceThaw, Mode: QuiesceModeFreeze}, nil
	}
	return quiesceGuest(ctx, QuiesceThaw)
}

// writableDeviceMounts reads a mount table and returns the mount points that hold a
// guest's files: mounted read-write, and backed by a block device. Everything else is
// outside a checkpoint's concern — the pseudo filesystems are rebuilt on boot, and a
// read-only mount cannot disagree with the disk image.
//
// The table escapes the characters a path may contain as octal (\040 for a space), and
// the escape is left as it stands: the kernel accepts it back in an open(), and
// unescaping here would be a second implementation of a rule the kernel already owns.
func writableDeviceMounts(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("quiesce: read the mount table %s: %w", path, err)
	}

	var mounts []string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		device, mountpoint, options := fields[0], fields[1], fields[3]
		if !strings.HasPrefix(device, "/dev/") {
			continue
		}
		if !hasMountOption(options, "rw") || hasMountOption(options, "ro") {
			continue
		}
		mounts = append(mounts, mountpoint)
	}
	return mounts, nil
}

// hasMountOption reports whether one comma-separated mount option list contains want.
func hasMountOption(options, want string) bool {
	for _, option := range strings.Split(options, ",") {
		if option == want {
			return true
		}
	}
	return false
}

// freezeMount blocks one mount's writers. A filesystem that is already frozen answers
// EBUSY, which is not a failure: the state the caller asked for is the state it has.
func freezeMount(mountpoint string) (bool, error) {
	return ioctlFreeze(mountpoint, fifreeze, unix.EBUSY)
}

// thawMount releases one mount's writers. A filesystem that is not frozen answers
// EINVAL, which is the same kind of success: there is nothing left to release.
func thawMount(mountpoint string) (bool, error) {
	return ioctlFreeze(mountpoint, fithaw, unix.EINVAL)
}

// ioctlFreeze issues one freeze or thaw ioctl against a mount point, and reads the
// "it was already in that state" error as success.
func ioctlFreeze(mountpoint string, request uintptr, alreadyErr unix.Errno) (bool, error) {
	fd, err := unix.Open(mountpoint, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, fmt.Errorf("open mount point: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()

	if err := unix.IoctlSetInt(fd, uint(request), 0); err != nil {
		if errors.Is(err, alreadyErr) {
			return true, nil
		}
		return false, fmt.Errorf("ioctl 0x%x: %w", request, err)
	}
	return false, nil
}
