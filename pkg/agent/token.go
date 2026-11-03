package agent

// This file owns the per-boot token that authenticates the host to the guest
// agent (SBX-082).
//
// The guest agent runs as root and its exec/put/get operations run as root, so
// anything in the guest that can reach the vsock port could otherwise run
// commands as root. A token only the guest's root can read removes that path:
// the node mints one per boot, writes it into the guest rootfs at TokenPath
// (root-owned, mode 0400), and keeps its own copy next to the VM's vsock muxer
// at TokenFilePath. The guest requires the token in `hello` and closes the
// connection without a frame when it is missing or wrong.
//
// The token is not passed on the kernel command line (/proc/cmdline is
// world-readable) and is never shared between VMs: a token minted for one boot
// is rejected by every other agent.

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	// TokenPath is where the node writes the per-boot token inside the guest
	// rootfs. The directory is created by InjectToken; the file is root-owned
	// with mode 0400 so only the root-run guest agent (and a root workload,
	// which already has root) can read it.
	//
	//nolint:gosec // G101: this is the path the token is written to, not a credential.
	TokenPath = "/etc/swarmcracker/agent-token"

	// TokenFileSuffix is the extension of the host-side token file that sits
	// next to a VM's vsock muxer: <socketDir>/<taskID>.agent-token.
	TokenFileSuffix = ".agent-token"

	// tokenBytes is the entropy of a minted token. 256 bits is far beyond
	// guessable over a local channel.
	tokenBytes = 32
)

// ErrNoToken reports that a rootfs image carries no agent token. It is the
// answer for every image built before token authentication existed, and for a
// golden template that was never provisioned.
var ErrNoToken = errors.New("agent: rootfs carries no agent token")

// ErrTokenRejected reports that the guest closed the connection during token
// authentication. The guest deliberately sends no detail, so the host cannot
// distinguish a wrong token from a missing one — that is the point.
var ErrTokenRejected = errors.New("agent: guest rejected the connection during token authentication")

// ErrTokenUnsupported reports that the guest agent answered hello without
// acknowledging token authentication. The node provisioned a token, so this is
// an agent that predates the feature (or a template baked with an old agent),
// and the connection is refused rather than silently accepted unauthenticated.
var ErrTokenUnsupported = errors.New("agent: guest agent does not support token authentication")

// ErrTokenRequired reports that the guest requires a token and the host sent
// none: the node has no token file for this VM, so it cannot prove who it is.
var ErrTokenRequired = errors.New("agent: guest agent requires a token but the host sent none")

// debugfsCommand and e2fsckCommand are the seams tests replace to observe or
// stub the ext4 tools. The production values are the real binaries.
var (
	debugfsCommand = exec.CommandContext
	e2fsckCommand  = exec.CommandContext
)

// NewToken mints a per-boot agent token: 32 bytes from crypto/rand, hex encoded
// so it survives being written to a file and compared as text.
func NewToken() (string, error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("mint agent token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// TokenFilePath returns the host-side token file for a VM's vsock muxer:
// <socketDir>/<taskID>.agent-token for <socketDir>/<taskID>.vsock. An empty
// path stays empty, so a caller can tell "no VM" from a path in the current
// directory.
func TokenFilePath(vsockPath string) string {
	if vsockPath == "" {
		return ""
	}
	return strings.TrimSuffix(vsockPath, filepath.Ext(vsockPath)) + TokenFileSuffix
}

// WriteTokenFile publishes a token for the host at path. The file is written to
// a temporary name in the same directory and renamed into place, so a reader
// never sees a partial token, and it is mode 0600: only the user that can read
// the vsock muxer's directory should be able to authenticate to the guest.
func WriteTokenFile(path, token string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("agent: token file path is required")
	}
	if err := validateToken(token); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("agent: create token directory %s: %w", dir, err)
	}

	f, err := os.CreateTemp(dir, ".agent-token-*")
	if err != nil {
		return fmt.Errorf("agent: create token file: %w", err)
	}
	tmp := f.Name()
	// Best effort: a no-op once the rename below succeeds.
	defer func() { _ = os.Remove(tmp) }()

	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("agent: set token file mode: %w", err)
	}
	if _, err := f.WriteString(token); err != nil {
		_ = f.Close()
		return fmt.Errorf("agent: write token file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("agent: close token file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("agent: install token file %s: %w", path, err)
	}
	return nil
}

// ReadTokenFile reads a host-side token file. A missing file is (\"\", false,
// nil): a VM booted without token authentication has no token, and that is not
// an error. An unreadable or empty file is an error, never a silent fallback to
// an unauthenticated dial.
func ReadTokenFile(path string) (string, bool, error) {
	if strings.TrimSpace(path) == "" {
		return "", false, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("agent: read token file %s: %w", path, err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", false, fmt.Errorf("agent: token file %s is empty", path)
	}
	return token, true, nil
}

// RemoveTokenFile removes the host-side token file for a VM. It is safe to call
// for a VM that never had one.
func RemoveTokenFile(vsockPath string) error {
	path := TokenFilePath(vsockPath)
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("agent: remove token file %s: %w", path, err)
	}
	return nil
}

// TokenAuthenticator verifies a client's hello against the token file the node
// wrote into this boot's rootfs. It re-reads the file on every check, so a
// rotated token takes effect without restarting the agent (the `prepare` op of
// SBX-083 is what delivers the new guest-side file).
type TokenAuthenticator struct {
	path string
}

// LoadAuthenticator opens the authenticator for path. A missing file returns
// (nil, nil): the VM was booted without token authentication, which is today's
// behaviour and must keep working. A file that exists but is not a root-owned,
// non-group/other-readable regular file is an error: a provisioned token whose
// permissions let the workload read it would not authenticate anything, so the
// agent fails closed instead of serving unauthenticated.
func LoadAuthenticator(path string) (*TokenAuthenticator, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	if _, err := readValidatedToken(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return &TokenAuthenticator{path: path}, nil
}

// Verify reports whether token is this boot's token. It re-reads the file each
// time, so rotation needs no restart; a file that became unreadable or was
// removed fails closed.
func (a *TokenAuthenticator) Verify(token string) bool {
	if a == nil || token == "" {
		return false
	}
	want, err := readValidatedToken(a.path)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(token)) == 1
}

// readValidatedToken reads and validates a guest-side token file.
func readValidatedToken(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("agent: token path %s is not a regular file", path)
	}
	// The token is only a secret while the workload cannot read it. Reject a
	// group- or world-readable file outright rather than authenticate with a
	// token every process in the guest could have copied.
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return "", fmt.Errorf("agent: token file %s is mode %04o: want no group or other permissions", path, perm)
	}
	// The agent runs as root, so a token owned by anyone else is not the
	// node's. The check is skipped off-root so the unit tests can run as an
	// ordinary user.
	if os.Geteuid() == 0 && info.Sys() != nil {
		if uid, ok := fileUID(info); ok && uid != 0 {
			return "", fmt.Errorf("agent: token file %s is owned by uid %d: want root", path, uid)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("agent: token file %s is empty", path)
	}
	return token, nil
}

// ProvisionForBoot mints a per-boot token, writes it into the guest rootfs at
// TokenPath, and publishes the same token for the host next to the VM's vsock
// muxer. rootfsPath must be the task's private, writable copy: writing a token
// into a shared image cache or a golden artifact would give every VM built from
// it the same credential.
func ProvisionForBoot(ctx context.Context, rootfsPath, vsockPath string) error {
	if strings.TrimSpace(rootfsPath) == "" {
		return errors.New("agent: rootfs path is required to provision a token")
	}
	if TokenFilePath(vsockPath) == "" {
		return errors.New("agent: vsock path is required to provision a token")
	}

	token, err := NewToken()
	if err != nil {
		return err
	}
	if err := InjectToken(ctx, rootfsPath, token); err != nil {
		return err
	}
	if err := WriteTokenFile(TokenFilePath(vsockPath), token); err != nil {
		return err
	}
	return nil
}

// InjectToken writes token into the rootfs ext4 image at TokenPath, root-owned
// and mode 0400, without mounting it. It uses debugfs like secret injection
// does, then replays the journal so the guest does not boot a filesystem whose
// journal and checksums predate the write.
func InjectToken(ctx context.Context, rootfsPath, token string) error {
	if strings.TrimSpace(rootfsPath) == "" {
		return errors.New("agent: rootfs path is required")
	}
	if err := validateToken(token); err != nil {
		return err
	}
	if _, err := os.Stat(rootfsPath); err != nil {
		return fmt.Errorf("agent: stat rootfs %s: %w", rootfsPath, err)
	}

	dir, err := os.MkdirTemp("", "sc-agent-token-")
	if err != nil {
		return fmt.Errorf("agent: create token staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	staged := filepath.Join(dir, "agent-token")
	if err := os.WriteFile(staged, []byte(token), 0o400); err != nil {
		return fmt.Errorf("agent: stage token: %w", err)
	}

	for _, dir := range ancestorDirs(filepath.Dir(TokenPath)) {
		if err := debugfs(ctx, rootfsPath, "mkdir "+dir); err != nil && !strings.Contains(err.Error(), "File exists") {
			return fmt.Errorf("agent: create %s in rootfs: %w", dir, err)
		}
	}
	// A retried provision of the same rootfs finds the previous token; debugfs
	// write refuses to overwrite, so drop it first. A missing file is expected
	// on the first boot.
	if err := debugfs(ctx, rootfsPath, "rm "+TokenPath); err != nil && !strings.Contains(err.Error(), "File not found") {
		return fmt.Errorf("agent: remove old token from rootfs: %w", err)
	}
	if err := debugfs(ctx, rootfsPath, fmt.Sprintf("write %s %s", staged, TokenPath)); err != nil {
		return fmt.Errorf("agent: write token into rootfs: %w", err)
	}
	// debugfs creates the file with default ownership and mode and ignores the
	// staged file's attributes, so set them explicitly. Regular file (0100000)
	// with 0400.
	if err := debugfs(ctx, rootfsPath, "sif "+TokenPath+" mode 0100400"); err != nil {
		return fmt.Errorf("agent: set token mode: %w", err)
	}
	if err := debugfs(ctx, rootfsPath, "sif "+TokenPath+" uid 0"); err != nil {
		return fmt.Errorf("agent: set token owner: %w", err)
	}
	if err := debugfs(ctx, rootfsPath, "sif "+TokenPath+" gid 0"); err != nil {
		return fmt.Errorf("agent: set token group: %w", err)
	}

	repairRootfs(ctx, rootfsPath)
	return nil
}

// ReadTokenFromRootfs reads the token an image carries, without mounting it.
// It is how a restored VM's token is adopted: the resumed guest keeps the token
// that was baked into its disk, so the target must publish that one rather than
// mint a token the guest has never seen. A rootfs without a token answers
// ErrNoToken.
func ReadTokenFromRootfs(ctx context.Context, rootfsPath string) (string, error) {
	if strings.TrimSpace(rootfsPath) == "" {
		return "", errors.New("agent: rootfs path is required")
	}
	cmd := debugfsReadCommand(ctx, rootfsPath, "cat "+TokenPath)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("agent: read token from %s: %w: %s", rootfsPath, err, strings.TrimSpace(stderr.String()))
	}
	if strings.Contains(stderr.String(), "File not found") || strings.Contains(stderr.String(), "No such file") {
		return "", ErrNoToken
	}
	token := strings.TrimSpace(stdout.String())
	if token == "" {
		return "", ErrNoToken
	}
	return token, nil
}

// debugfs runs one debugfs command against the image and turns a non-zero exit
// or one of debugfs's error messages into an error. debugfs exits 0 for some
// failures, so the output is checked too.
func debugfs(ctx context.Context, image, request string) error {
	cmd := debugfsCommandContext(ctx, image, request)
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err != nil {
		return fmt.Errorf("debugfs %q: %w: %s", request, err, strings.TrimSpace(text))
	}
	for _, marker := range []string{"Filesystem not open", "No such file or directory", "while trying to open"} {
		if strings.Contains(text, marker) {
			return fmt.Errorf("debugfs %q: %s", request, strings.TrimSpace(text))
		}
	}
	return nil
}

// debugfsCommandContext builds a debugfs invocation. Requests are written as a
// single -R argument; no request contains user-controlled text (TokenPath is a
// constant and the staged path comes from os.MkdirTemp).
func debugfsCommandContext(ctx context.Context, image, request string) *exec.Cmd {
	if ctx == nil {
		ctx = context.Background()
	}
	return debugfsCommand(ctx, "debugfs", "-w", "-R", request, image)
}

// debugfsReadCommand is debugfsCommandContext without -w. Reading a token from
// a disk a running guest owns must never open it read-write.
func debugfsReadCommand(ctx context.Context, image, request string) *exec.Cmd {
	if ctx == nil {
		ctx = context.Background()
	}
	return debugfsCommand(ctx, "debugfs", "-R", request, image)
}

// ancestorDirs returns every directory from the first path element down to dir,
// in order: /etc/swarmcracker becomes ["/etc", "/etc/swarmcracker"]. debugfs
// mkdir does not create parents, so each one is made explicitly.
func ancestorDirs(dir string) []string {
	if dir == "" || dir == "/" || dir == "." {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(filepath.Clean(dir), "/"), "/")
	dirs := make([]string, 0, len(parts))
	current := ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		current += "/" + part
		dirs = append(dirs, current)
	}
	return dirs
}

// repairRootfs replays the ext4 journal and repairs metadata checksums after a
// debugfs write, exactly like secret injection does. It is best effort: a host
// without e2fsprogs still gets a provisioned token, and the guest's own fsck
// handles the rest at boot.
func repairRootfs(ctx context.Context, rootfsPath string) {
	if _, err := exec.LookPath("e2fsck"); err != nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := e2fsckCommand(ctx, "e2fsck", "-fy", rootfsPath)
	if _, err := cmd.CombinedOutput(); err != nil {
		var exitErr *exec.ExitError
		// e2fsck exits 1 when it corrected errors, which is success here.
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return
		}
	}
}

// validateToken rejects a token that cannot be stored or compared as one line
// of text.
func validateToken(token string) error {
	if token == "" {
		return errors.New("agent: token is empty")
	}
	if strings.TrimSpace(token) != token {
		return errors.New("agent: token has leading or trailing whitespace")
	}
	for _, r := range token {
		if r < 0x21 || r > 0x7e {
			return fmt.Errorf("agent: token contains a non-printable character %q", r)
		}
	}
	return nil
}
