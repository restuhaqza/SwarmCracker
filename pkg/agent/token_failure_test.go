package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failDebugfsAt replaces the debugfs seam so the nth invocation fails and every
// earlier one runs for real. It is how the failure of each provisioning step —
// mkdir, rm, write, and the three set_inode_field calls — is exercised against a
// real image without a fake debugfs implementation.
func failDebugfsAt(t *testing.T, n int) {
	t.Helper()
	calls := 0
	original := debugfsCommand
	debugfsCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		calls++
		if calls >= n {
			return exec.CommandContext(ctx, "false")
		}
		return original(ctx, name, args...)
	}
	t.Cleanup(func() { debugfsCommand = original })
}

func TestInjectTokenReportsEachDebugfsFailure(t *testing.T) {
	image := newTestExt4(t)

	// mkdir /etc, mkdir /etc/swarmcracker, rm, write, mode, uid, gid.
	for n := 1; n <= 7; n++ {
		t.Run(fmt.Sprintf("call-%d", n), func(t *testing.T) {
			failDebugfsAt(t, n)
			err := InjectToken(context.Background(), image, "per-boot-secret")
			require.Error(t, err, "a failed debugfs step must not be reported as a provisioned token")
		})
	}
}

func TestInjectTokenRejectsMissingOrInvalidInput(t *testing.T) {
	image := newTestExt4(t)
	var noContext context.Context

	require.Error(t, InjectToken(context.Background(), filepath.Join(t.TempDir(), "absent.ext4"), "secret"))
	require.Error(t, InjectToken(noContext, filepath.Join(t.TempDir(), "absent.ext4"), "secret"))
	require.Error(t, InjectToken(context.Background(), "", "secret"))
	require.Error(t, InjectToken(context.Background(), image, ""))
	require.Error(t, InjectToken(context.Background(), image, "two words"))

	// A nil context reaches the command builders with no deadline; provisioning
	// still works, which is what a caller that forgot a context gets.
	require.NoError(t, InjectToken(noContext, image, "secret"))
}

func TestRepairRootfsIsBestEffort(t *testing.T) {
	// repairRootfs only runs when e2fsck is on PATH; a stub binary plus a stubbed
	// command lets all three outcomes (clean, corrected, hard failure) run
	// without touching a real filesystem.
	binDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "e2fsck"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	for _, exitCode := range []string{"0", "1", "2"} {
		original := e2fsckCommand
		e2fsckCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "sh", "-c", "exit "+exitCode)
		}
		repairRootfs(context.Background(), filepath.Join(t.TempDir(), "image.ext4"))
		repairRootfs(nil, filepath.Join(t.TempDir(), "image.ext4"))
		e2fsckCommand = original
	}
}

func TestWriteTokenFileReportsFilesystemFailures(t *testing.T) {
	// A parent that is a regular file makes the directory creation fail.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	require.Error(t, WriteTokenFile(filepath.Join(blocker, "token"), "secret"))

	// A destination that is a directory makes the install rename fail.
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "token"), 0o700))
	require.Error(t, WriteTokenFile(filepath.Join(dir, "token"), "secret"))
}

func TestReadTokenFileReportsAReadError(t *testing.T) {
	_, _, err := ReadTokenFile(t.TempDir())
	require.Error(t, err, "a directory is not a readable token file")
}

func TestRemoveTokenFileReportsAFailure(t *testing.T) {
	vsock := filepath.Join(t.TempDir(), "task.vsock")
	path := TokenFilePath(vsock)
	require.NoError(t, os.MkdirAll(filepath.Join(path, "child"), 0o700))

	require.Error(t, RemoveTokenFile(vsock), "a non-empty directory is not removable as a token file")
}

func TestLoadAuthenticatorRejectsANonRegularFile(t *testing.T) {
	_, err := LoadAuthenticator(t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a regular file")

	// A symlink is not followed: the node writes a regular file, and a link is a
	// way to point the agent at one the workload controls.
	target := writeTokenFile(t, t.TempDir(), "secret")
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(target, link))
	_, err = LoadAuthenticator(link)
	require.Error(t, err)
}

// TestAuthenticatorRejectsAFileNotOwnedByRoot covers the guest-side ownership
// check. The agent runs as root there; the seam lets an unprivileged test host
// reach the branch, which is the fail-closed case for a token another guest user
// planted.
func TestAuthenticatorRejectsAFileNotOwnedByRoot(t *testing.T) {
	path := writeTokenFile(t, t.TempDir(), "secret")

	original := currentEUID
	currentEUID = func() int { return 0 }
	t.Cleanup(func() { currentEUID = original })

	_, err := LoadAuthenticator(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owned by uid")

	// With the same file owned by uid 0 the authenticator loads: the check is
	// about ownership, not about running as root.
	currentEUID = func() int { return os.Geteuid() }
	if os.Geteuid() != 0 {
		t.Skip("cannot create a root-owned file without root")
	}
	require.NoError(t, os.Chown(path, 0, 0))
	auth, err := LoadAuthenticator(path)
	require.NoError(t, err)
	assert.NotNil(t, auth)
}

func TestReadTokenFromRootfsReportsFailures(t *testing.T) {
	_, err := ReadTokenFromRootfs(context.Background(), "")
	require.Error(t, err)

	// A file that is not an ext4 image makes debugfs fail, which must surface
	// rather than look like a tokenless image.
	notAnImage := filepath.Join(t.TempDir(), "plain")
	require.NoError(t, os.WriteFile(notAnImage, []byte("not an ext4 image"), 0o600))
	_, err = ReadTokenFromRootfs(context.Background(), notAnImage)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNoToken)
}

func TestProvisionForBootReportsFailures(t *testing.T) {
	// A rootfs that debugfs cannot open fails before any host copy is written.
	notAnImage := filepath.Join(t.TempDir(), "plain")
	require.NoError(t, os.WriteFile(notAnImage, []byte("plain"), 0o600))
	vsock := filepath.Join(t.TempDir(), "task.vsock")
	require.Error(t, ProvisionForBoot(context.Background(), notAnImage, vsock))
	_, found, err := ReadTokenFile(TokenFilePath(vsock))
	require.NoError(t, err)
	assert.False(t, found, "a failed provision must not leave a host token behind")

	// A host copy that cannot be written fails the provision, because the guest
	// would otherwise require a token the node cannot present.
	image := newTestExt4(t)
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	require.Error(t, ProvisionForBoot(context.Background(), image, filepath.Join(blocker, "task.vsock")))
}

func TestTokenHelpersAcceptAnAbsentPath(t *testing.T) {
	require.Error(t, WriteTokenFile("", "secret"))

	_, found, err := ReadTokenFile("")
	require.NoError(t, err)
	assert.False(t, found, "no path means no token, not an error")

	require.NoError(t, RemoveTokenFile(""))

	auth, err := LoadAuthenticator("")
	require.NoError(t, err)
	assert.Nil(t, auth)
}

func TestWriteTokenFileReportsACreateFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	require.Error(t, WriteTokenFile(filepath.Join(dir, "token"), "secret"))
}

func TestInjectTokenReportsAStagingFailure(t *testing.T) {
	// os.MkdirTemp("") reads TMPDIR; a TMPDIR that is a file cannot host the
	// staged token.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	t.Setenv("TMPDIR", blocker)

	require.Error(t, InjectToken(context.Background(), newTestExt4(t), "secret"))
}

func TestReadTokenFromRootfsReportsARunFailure(t *testing.T) {
	original := debugfsCommand
	debugfsCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "false")
	}
	t.Cleanup(func() { debugfsCommand = original })

	_, err := ReadTokenFromRootfs(context.Background(), filepath.Join(t.TempDir(), "image"))
	require.Error(t, err)
}

func TestReadTokenFromRootfsWithoutAContext(t *testing.T) {
	image := newTestExt4(t)
	require.NoError(t, InjectToken(context.Background(), image, "secret"))

	var noContext context.Context
	got, err := ReadTokenFromRootfs(noContext, image)
	require.NoError(t, err)
	assert.Equal(t, "secret", got)
}

// TestNewTokenReportsAnEntropyFailure covers the one branch a real crypto/rand
// never takes. ProvisionForBoot must surface it rather than publish a zero token.
func TestNewTokenReportsAnEntropyFailure(t *testing.T) {
	original := randomRead
	randomRead = func([]byte) (int, error) { return 0, errors.New("no entropy") }
	t.Cleanup(func() { randomRead = original })

	_, err := NewToken()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mint agent token")

	err = ProvisionForBoot(context.Background(), newTestExt4(t), filepath.Join(t.TempDir(), "task.vsock"))
	require.Error(t, err)
}

// TestLoadAuthenticatorReportsAnUnreadableFile keeps a provisioned token that
// the agent cannot read from being mistaken for "no token" (which would serve
// unauthenticated).
func TestLoadAuthenticatorReportsAnUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read a mode-0000 file")
	}
	path := filepath.Join(t.TempDir(), "agent-token")
	require.NoError(t, os.WriteFile(path, []byte("secret"), 0o400))
	require.NoError(t, os.Chmod(path, 0o000))

	_, err := LoadAuthenticator(path)
	require.Error(t, err)
	assert.NotErrorIs(t, err, os.ErrNotExist, "an unreadable token is an error, not an absent one")
}

// TestReadTokenFromRootfsRejectsAnEmptyTokenFile covers a token file that exists
// but holds nothing: the image is broken, and the restore must not treat it as
// authenticated.
func TestReadTokenFromRootfsRejectsAnEmptyTokenFile(t *testing.T) {
	image := newTestExt4(t)
	empty := filepath.Join(t.TempDir(), "empty")
	require.NoError(t, os.WriteFile(empty, nil, 0o600))
	for _, dir := range ancestorDirs(filepath.Dir(TokenPath)) {
		out, err := exec.Command("debugfs", "-w", "-R", "mkdir "+dir, image).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	out, err := exec.Command("debugfs", "-w", "-R", "write "+empty+" "+TokenPath, image).CombinedOutput()
	require.NoError(t, err, string(out))

	_, err = ReadTokenFromRootfs(context.Background(), image)
	require.ErrorIs(t, err, ErrNoToken)
}
