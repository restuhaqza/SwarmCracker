package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
)

// tapNameDigestPrefix is how many hex characters of the task-ID digest name a
// TAP device. Eight characters of a SHA-256 digest give 32 bits of task
// separation, which is enough to keep the names of the microVMs of one node
// distinct while leaving room for the index inside the kernel's 15-character
// interface-name limit.
const tapNameDigestPrefix = 8

// vsockSuffix is the extension of a task's vsock muxer socket, beside the API
// socket's ".sock". The suffix is spelled here once: the executor, both VMM
// managers and the node's restore path all resolve the muxer through VsockPath
// or VsockPathFromSocket.
const vsockSuffix = ".vsock"

// TapName returns the TAP device name for one network interface of a task:
//
//	tap-<first 8 hex characters of sha256(taskID)>-<index>
//
// The name is a pure function of (taskID, index). It has to be, because three
// places must agree on it and none of them can ask the others: the network
// manager creates the device, Firecracker is configured with the name in its
// network-interface config, and a migrating VM must recreate the name recorded
// inside its snapshot state file (which is the source task's name, not the new
// task's). Deriving it from the task ID also makes it stable across restarts,
// so a retried create finds its own device instead of a second one.
func TapName(taskID string, index int) string {
	sum := sha256.Sum256([]byte(taskID))
	return fmt.Sprintf("tap-%s-%d", hex.EncodeToString(sum[:])[:tapNameDigestPrefix], index)
}

// VsockPath returns the Firecracker vsock muxer path for a task:
//
//	<socketDir>/<taskID>.vsock
//
// It sits next to the task's API socket (<socketDir>/<taskID>.sock) because the
// executor configures both from the same directory. It returns "" when either
// input is empty, so a caller can tell "this VM has no vsock" from a path that
// happens to point at the current directory.
func VsockPath(socketDir, taskID string) string {
	if socketDir == "" || taskID == "" {
		return ""
	}
	return filepath.Join(socketDir, taskID+vsockSuffix)
}

// VsockPathFromSocket returns the vsock muxer path of a VM described by its API
// socket path: <socketDir>/<taskID>.sock becomes <socketDir>/<taskID>.vsock.
//
// It exists for callers that hold a socket path rather than the (socketDir,
// taskID) pair — the VMM managers configuring the vsock device, the node
// clearing a stale muxer socket before a restore — so they share the one rule
// with the executor instead of trimming ".sock" and appending their own suffix.
// A path without the socket suffix only gains the vsock one, and "" stays ""
// (no socket, no vsock).
func VsockPathFromSocket(socketPath string) string {
	if socketPath == "" {
		return ""
	}
	return VsockPath(filepath.Dir(socketPath), strings.TrimSuffix(filepath.Base(socketPath), VMSocketSuffix))
}
