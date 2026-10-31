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

// interfaceNameLimit is the kernel's IFNAMSIZ minus the terminating NUL: the
// longest interface name that can be created.
const interfaceNameLimit = 15

// vxlanInterfaceDigestPrefix is how many hex characters of the bridge digest a
// shortened VXLAN interface name carries, so the fallback name still fits in
// interfaceNameLimit ("vx-" plus 12 characters).
const vxlanInterfaceDigestPrefix = 12

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

// GuestMAC returns the MAC address of one network interface of a task's guest:
//
//	AA:FC:<b0>:<b1>:<b2>:<b3>
//
// where b0..b3 are the first four bytes of sha256("<taskID>/<index>"). The
// address is locally administered unicast (the AA:FC prefix), so it cannot
// collide with a vendor-registered MAC on the segment the microVMs share.
//
// It has to be a pure function of (taskID, index) for the same reason TapName
// does: the guest's address is fixed when the VM boots and lives inside the
// guest, so a migrating VM keeps the address the source task's interface had.
// The network manager's re-anneal (and anything else that has to name the
// guest's L2 identity without asking the guest) derives it here rather than
// reading it from a device: the host-side TAP device's MAC is not the guest's.
func GuestMAC(taskID string, index int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", taskID, index)))
	return fmt.Sprintf("AA:FC:%02X:%02X:%02X:%02X", sum[0], sum[1], sum[2], sum[3])
}

// VXLANInterfaceName returns the overlay interface name for a bridge:
// "<bridge>-vxlan" when it fits in the kernel's interface-name limit, and
// "vx-<first 12 hex characters of sha256(bridge)>" when it does not.
//
// The fallback exists so a long bridge name (br-backend, say) does not silently
// produce a name the kernel refuses. It lives here rather than in pkg/network
// because a node's re-anneal has to name the overlay interface too, and that
// code path is not Linux-only.
func VXLANInterfaceName(bridge string) string {
	name := bridge + "-vxlan"
	if len(name) <= interfaceNameLimit {
		return name
	}
	sum := sha256.Sum256([]byte(bridge))
	return "vx-" + hex.EncodeToString(sum[:])[:vxlanInterfaceDigestPrefix]
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
