package golden

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// This file is the seal: what the builder removes from a template so that
// everything made from it starts clean, and the check that proves it worked.
//
// A template is a *starting point*, and five kinds of leftover say "this was a
// machine that ran": the machine identity, the SSH host keys that name a host,
// the shell history of whoever built the image, the logs of the build itself,
// and the runtime state that claims a service is already running. A template
// that keeps any of them does not fail loudly — it quietly gives every VM made
// from it the same identity, which is why the seal is the builder's own step
// rather than a line each recipe is trusted to remember, and why the build
// fails if the result is not clean rather than recording an almost-clean
// template.
//
// # The three steps that are not here, and why
//
// A seal normally also stops services, zeroes free space and syncs. The builder
// seals an offline rootfs, so those are not steps it can take, and pretending
// otherwise with a no-op would be worse than saying so:
//
//   - Stopping services needs a running guest. What an offline rootfs can do is
//     remove the state that would make a service look like it is already
//     running, which is what clearRuntimeState does.
//   - Zeroing free space matters on a disk that had data deleted from it, where
//     the deleted blocks are still readable. The artifact is a filesystem the
//     packer creates from scratch in one pass, so no block ever held anything:
//     the evidence is that every free block of the packed image reads as zero,
//     which is checked in the PR body against the artifact itself rather than
//     assumed here.
//   - sync belongs to the packer, which writes and fsyncs the image; the files
//     it reads are in the page cache either way.
//
// The guest-side half — a real shutdown, a quiesce, a sync from inside — needs
// agent operations that do not exist yet, and is SBX-083/SBX-087's.

// sealStep is one part of the seal, named so a failure says which part.
type sealStep struct {
	name string
	run  func(rootfsDir string) error
}

// sealSteps are applied in this order, after the recipe's own seal scripts.
var sealSteps = []sealStep{
	{"truncate logs", truncateLogs},
	{"clear shell history", clearShellHistory},
	{"reset machine-id", resetMachineID},
	{"remove SSH host keys", removeSSHHostKeys},
	{"clear service runtime state", clearRuntimeState},
}

// Image paths the seal works on.
const (
	// logsDir is where a guest keeps its logs.
	logsDir = "/var/log"
	// runDir and varRunDir are where a running system keeps pid files, sockets
	// and lock files. On Debian-family images one is a symlink to the other.
	runDir    = "/run"
	varRunDir = "/var/run"
	// homeDir holds the other users a template may ship.
	homeDir = "/home"
	// sshHostKeyGlob names the private and public host keys OpenSSH generates
	// for itself on first boot. A template that ships one makes every guest
	// made from it the same host.
	sshHostKeyGlob = "/etc/ssh/ssh_host_*"
)

// machineIDPaths hold a machine's identity: what systemd reports as
// /etc/machine-id, and the D-Bus copy older Debian-family images keep in step
// with it.
var machineIDPaths = []string{"/etc/machine-id", "/var/lib/dbus/machine-id"}

// historyNames are the files a shell writes its history to.
var historyNames = []string{
	".bash_history",
	".ash_history",
	".sh_history",
	".zsh_history",
	".history",
	".local/share/fish/fish_history",
}

// sealTemplate applies the standard seal to a provisioned root filesystem. It
// runs after the recipe's own seal scripts so a recipe cannot leave the image
// half-clean, and before the build verifies anything, so what is verified is
// what is packed.
func sealTemplate(rootfsDir string) error {
	if strings.TrimSpace(rootfsDir) == "" {
		return errors.New("no rootfs directory")
	}
	for _, step := range sealSteps {
		if err := step.run(rootfsDir); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
	}
	return nil
}

// truncateLogs empties every regular file under /var/log and keeps the files
// themselves: a service that expects its log to exist still finds it, and the
// build has left no record of itself behind. Symlinks are left alone, because
// emptying one would empty whatever it points at — including /dev/null.
func truncateLogs(rootfsDir string) error {
	return walkRegularFiles(filepath.Join(rootfsDir, logsDir), func(path string, info fs.FileInfo) error {
		if info.Size() == 0 {
			return nil
		}
		return os.Truncate(path, 0)
	})
}

// clearShellHistory removes the history files of root and of every home
// directory, so nothing a human typed while building the image survives into
// every VM made from it.
func clearShellHistory(rootfsDir string) error {
	users := []string{"/root"}
	entries, err := os.ReadDir(filepath.Join(rootfsDir, homeDir))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			users = append(users, filepath.Join(homeDir, entry.Name()))
		}
	}

	for _, user := range users {
		for _, name := range historyNames {
			path := filepath.Join(rootfsDir, user, name)
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

// resetMachineID empties the files that hold a machine's identity. The file is
// kept and emptied rather than removed, which is the convention a distribution's
// own tooling establishes and what systemd expects to find: an empty
// /etc/machine-id means "this filesystem has no identity yet", so the first
// boot generates one, and every boot after it keeps it.
//
// A symlink carries no identity of its own, so it is left for the verification
// to judge: one that points at another path this seal resets is clean, and one
// that points anywhere else fails the build.
func resetMachineID(rootfsDir string) error {
	for _, guestPath := range machineIDPaths {
		path := filepath.Join(rootfsDir, guestPath)
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			continue
		}
		if err := os.Truncate(path, 0); err != nil {
			return err
		}
	}
	return nil
}

// removeSSHHostKeys removes every SSH host key an image may have generated, so
// no two guests made from this template present the same host identity.
func removeSSHHostKeys(rootfsDir string) error {
	matches, err := filepath.Glob(filepath.Join(rootfsDir, sshHostKeyGlob))
	if err != nil {
		return err
	}
	for _, match := range matches {
		if err := os.RemoveAll(match); err != nil {
			return err
		}
	}
	return nil
}

// clearRuntimeState removes what is under /run (and /var/run where that is its
// own directory) — the pid files, sockets and lock files of a running system.
// An offline rootfs has no service to stop, but it can still carry the state
// one left behind, and a template whose /run claims a service is already
// running starts its clones in a state nobody chose.
//
// A /var/run that is a symlink to /run is skipped: clearing /run is clearing
// it, and following the link would only do the same work twice.
func clearRuntimeState(rootfsDir string) error {
	for _, dir := range []string{runDir, varRunDir} {
		root := filepath.Join(rootfsDir, dir)
		info, err := os.Lstat(root)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
			continue
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// verifySealed reports every reason the sealed image is not clean, or nil. It
// reads the root filesystem the packer is about to write, and it is the check
// that fails a build: an almost-clean template is worse than a failed build,
// because every VM made from it inherits whatever was left behind, and nothing
// downstream can tell.
//
// It reports all the problems it finds rather than the first, because a seal
// that missed one step usually missed the others.
func verifySealed(rootfsDir string) error {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	for _, guestPath := range machineIDPaths {
		clean, err := machineIdentityIsCleared(rootfsDir, guestPath)
		if err != nil {
			add("%s cannot be read: %v", guestPath, err)
			continue
		}
		if !clean {
			add("%s carries a machine identity", guestPath)
		}
	}

	matches, err := filepath.Glob(filepath.Join(rootfsDir, sshHostKeyGlob))
	if err != nil {
		add("%s cannot be listed: %v", sshHostKeyGlob, err)
	}
	for _, match := range matches {
		add("%s is an SSH host key", strings.TrimPrefix(match, rootfsDir))
	}

	users := []string{"/root"}
	entries, err := os.ReadDir(filepath.Join(rootfsDir, homeDir))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		add("%s cannot be listed: %v", homeDir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			users = append(users, filepath.Join(homeDir, entry.Name()))
		}
	}
	for _, user := range users {
		for _, name := range historyNames {
			guestPath := filepath.Join(user, name)
			info, err := os.Lstat(filepath.Join(rootfsDir, guestPath))
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				add("%s cannot be read: %v", guestPath, err)
				continue
			}
			if info.Size() > 0 {
				add("%s holds shell history", guestPath)
			}
		}
	}

	if err := walkRegularFiles(filepath.Join(rootfsDir, logsDir), func(path string, info fs.FileInfo) error {
		if info.Size() > 0 {
			add("%s holds a log", strings.TrimPrefix(path, rootfsDir))
		}
		return nil
	}); err != nil {
		add("%s cannot be read: %v", logsDir, err)
	}

	if len(problems) > 0 {
		return fmt.Errorf("the sealed image is not clean: %s", strings.Join(problems, "; "))
	}
	return nil
}

// machineIdentityIsCleared reports whether the file at guestPath carries no
// machine identity: it is absent, empty, not a regular file, or a symlink that
// resolves inside this image to another path the seal resets. Anything else —
// in particular a link out of the image, which would make the guest's identity
// somebody else's file — is not clean.
func machineIdentityIsCleared(rootfsDir, guestPath string) (bool, error) {
	path := filepath.Join(rootfsDir, guestPath)
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&fs.ModeSymlink == 0 {
		return !info.Mode().IsRegular() || info.Size() == 0, nil
	}

	// resolveLink reports the guest path the link points at, which for an
	// absolute target is a guest path and not a host one.
	target, err := resolveLink(rootfsDir, path)
	if err != nil {
		return false, err
	}
	for _, known := range machineIDPaths {
		if target == known {
			return true, nil
		}
	}
	return false, nil
}

// walkRegularFiles calls fn for every regular file under root. A root that is
// not there is not a failure: not every image has a directory to clean.
func walkRegularFiles(root string, fn func(path string, info fs.FileInfo) error) error {
	if _, err := os.Stat(root); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// WalkDir does not follow symlinks, so a link is never a regular file
		// here and is never written through.
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		return fn(path, info)
	})
}
