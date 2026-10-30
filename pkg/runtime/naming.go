package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
)

// tapNameDigestPrefix is how many hex characters of the task-ID digest name a
// TAP device. Eight characters of a SHA-256 digest give 32 bits of task
// separation, which is enough to keep the names of the microVMs of one node
// distinct while leaving room for the index inside the kernel's 15-character
// interface-name limit.
const tapNameDigestPrefix = 8

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
	return filepath.Join(socketDir, taskID+".vsock")
}
