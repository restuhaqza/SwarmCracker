package network

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/rs/zerolog/log"
)

// RemoveNetNS removes a network namespace by the path it was created at, and
// treats one that is already gone as success.
//
// A namespace path is what the node records for a VM (`Instance.NetNS`), and it is
// a path rather than a name on purpose: `ip netns add <name>` creates
// /var/run/netns/<name> as a bind mount, but a VM can be started in a namespace at
// any path, and the record has to name the one it was actually given. Deleting
// that mount is the unmount-then-remove `ip netns delete` performs, without
// needing the namespace to be one ip(8) created.
//
// It is idempotent for the reason a teardown needs it to be: a retried teardown,
// and a VM that died outside a stop path, arrive with the namespace already gone,
// and must not fail the cleanup that still has to free the disk beside it.
func RemoveNetNS(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("netns path is required")
	}

	// Only a mount is unmounted. A path that is not one is either already gone or
	// was never a namespace, and the remove below is the answer for both; asking
	// the kernel to unmount it would fail with an error that means nothing about
	// this namespace.
	if isMountPoint(path) {
		if err := unix.Unmount(path, unix.MNT_DETACH); err != nil {
			return fmt.Errorf("failed to unmount netns %s: %w", path, err)
		}
	}

	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to remove netns %s: %w", path, err)
	}

	log.Debug().Str("netns", path).Msg("Network namespace is removed or was already gone")
	return nil
}

// isMountPoint reports whether path is a mount point, by the device-number rule:
// a mount point's device differs from its parent directory's, and a plain file's
// does not. A path that cannot be inspected is not a mount point, which is what a
// namespace that is already gone looks like.
func isMountPoint(path string) bool {
	var info, parent unix.Stat_t
	if err := unix.Stat(path, &info); err != nil {
		return false
	}
	if err := unix.Stat(filepath.Dir(path), &parent); err != nil {
		return false
	}
	return info.Dev != parent.Dev
}
