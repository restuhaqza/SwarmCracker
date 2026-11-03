package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTokenFile writes a guest-side token file with the mode the node uses.
func writeTokenFile(t *testing.T, dir, token string) string {
	t.Helper()
	path := filepath.Join(dir, "agent-token")
	require.NoError(t, os.WriteFile(path, []byte(token), 0o400))
	return path
}

func TestNewTokenIsRandomAndHex(t *testing.T) {
	first, err := NewToken()
	require.NoError(t, err)
	second, err := NewToken()
	require.NoError(t, err)

	assert.Len(t, first, tokenBytes*2, "a token is 32 bytes hex encoded")
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]+$`), first)
	assert.NotEqual(t, first, second, "each boot must get its own token")
}

func TestTokenFilePath(t *testing.T) {
	assert.Equal(t, "/var/run/firecracker/task-1.agent-token",
		TokenFilePath("/var/run/firecracker/task-1.vsock"))
	assert.Equal(t, "", TokenFilePath(""), "no muxer means no token file")
}

func TestWriteAndReadTokenFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "task.agent-token")

	require.NoError(t, WriteTokenFile(path, "abc123"))
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "the host copy is readable only by its owner")

	got, found, err := ReadTokenFile(path)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "abc123", got)

	// Rewriting replaces the token in place, which is what rotation does.
	require.NoError(t, WriteTokenFile(path, "def456"))
	got, found, err = ReadTokenFile(path)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "def456", got)
}

func TestReadTokenFileMissingIsNotAnError(t *testing.T) {
	got, found, err := ReadTokenFile(filepath.Join(t.TempDir(), "absent"))
	require.NoError(t, err)
	assert.False(t, found)
	assert.Empty(t, got)
}

func TestReadTokenFileEmptyIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty")
	require.NoError(t, os.WriteFile(path, nil, 0o600))

	_, _, err := ReadTokenFile(path)
	require.Error(t, err, "an empty token file must not become an unauthenticated dial")
}

func TestWriteTokenFileRejectsUnusableTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	for _, token := range []string{"", " padded", "two words", "line\nbreak"} {
		assert.Error(t, WriteTokenFile(path, token), "token %q", token)
	}
}

func TestLoadAuthenticatorMissingFileDisablesAuth(t *testing.T) {
	auth, err := LoadAuthenticator(filepath.Join(t.TempDir(), "absent"))
	require.NoError(t, err)
	assert.Nil(t, auth, "a VM booted without a token keeps today's behaviour")
}

func TestLoadAuthenticatorRejectsWorldReadableToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-token")
	require.NoError(t, os.WriteFile(path, []byte("secret"), 0o644))

	_, err := LoadAuthenticator(path)
	require.Error(t, err, "a token the workload can read authenticates nothing")
	assert.Contains(t, err.Error(), "permissions")
}

func TestLoadAuthenticatorRejectsEmptyToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-token")
	require.NoError(t, os.WriteFile(path, nil, 0o400))

	_, err := LoadAuthenticator(path)
	require.Error(t, err)
}

func TestAuthenticatorVerifiesAndRotates(t *testing.T) {
	dir := t.TempDir()
	path := writeTokenFile(t, dir, "boot-one")

	auth, err := LoadAuthenticator(path)
	require.NoError(t, err)
	require.NotNil(t, auth)

	assert.True(t, auth.Verify("boot-one"))
	assert.False(t, auth.Verify("boot-two"))
	assert.False(t, auth.Verify(""), "an empty token never authenticates")

	// Rotation replaces the file; the same authenticator must pick it up with
	// no restart, because the agent outlives a `prepare`.
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.WriteFile(path, []byte("boot-two"), 0o400))
	assert.True(t, auth.Verify("boot-two"), "a rotated token must work")
	assert.False(t, auth.Verify("boot-one"), "the previous token must stop working")
}

func TestAuthenticatorFailsClosedWhenTokenFileDisappears(t *testing.T) {
	path := writeTokenFile(t, t.TempDir(), "boot-one")
	auth, err := LoadAuthenticator(path)
	require.NoError(t, err)

	require.NoError(t, os.Remove(path))
	assert.False(t, auth.Verify("boot-one"), "a missing token file must not authenticate anyone")
}

func TestRemoveTokenFile(t *testing.T) {
	vsock := filepath.Join(t.TempDir(), "task.vsock")
	require.NoError(t, WriteTokenFile(TokenFilePath(vsock), "abc"))

	require.NoError(t, RemoveTokenFile(vsock))
	_, found, err := ReadTokenFile(TokenFilePath(vsock))
	require.NoError(t, err)
	assert.False(t, found)

	// Removing twice is fine: teardown is idempotent.
	require.NoError(t, RemoveTokenFile(vsock))
}

func TestAncestorDirs(t *testing.T) {
	assert.Equal(t, []string{"/etc", "/etc/swarmcracker"}, ancestorDirs("/etc/swarmcracker"))
	assert.Equal(t, []string{"/etc"}, ancestorDirs("/etc"))
	assert.Nil(t, ancestorDirs("/"))
	assert.Nil(t, ancestorDirs(""))
}

// newTestExt4 builds a small empty ext4 image, skipping the test when the ext4
// tools are not installed. The token path is written and read with debugfs, so
// a real image is the only honest fixture.
func newTestExt4(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("mkfs.ext4"); err != nil {
		t.Skip("mkfs.ext4 is not installed")
	}
	if _, err := exec.LookPath("debugfs"); err != nil {
		t.Skip("debugfs is not installed")
	}

	path := filepath.Join(t.TempDir(), "rootfs.ext4")
	f, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(8<<20))
	require.NoError(t, f.Close())

	out, err := exec.Command("mkfs.ext4", "-q", "-F", path).CombinedOutput()
	require.NoError(t, err, string(out))
	return path
}

// debugfsStat returns the inode description of path inside image.
func debugfsStat(t *testing.T, image, path string) string {
	t.Helper()
	out, err := exec.Command("debugfs", "-R", "stat "+path, image).CombinedOutput()
	require.NoError(t, err, string(out))
	return string(out)
}

func TestInjectTokenWritesRootOwnedReadOnlyFile(t *testing.T) {
	image := newTestExt4(t)

	require.NoError(t, InjectToken(context.Background(), image, "per-boot-secret"))

	got, err := ReadTokenFromRootfs(context.Background(), image)
	require.NoError(t, err)
	assert.Equal(t, "per-boot-secret", got)

	stat := debugfsStat(t, image, TokenPath)
	assert.Regexp(t, regexp.MustCompile(`Mode:\s+0400`), stat, "the guest token must be 0400")
	assert.Regexp(t, regexp.MustCompile(`User:\s+0\s+Group:\s+0`), stat, "the guest token must be root-owned")
}

func TestInjectTokenReplacesAPreviousToken(t *testing.T) {
	image := newTestExt4(t)

	require.NoError(t, InjectToken(context.Background(), image, "first"))
	require.NoError(t, InjectToken(context.Background(), image, "second"))

	got, err := ReadTokenFromRootfs(context.Background(), image)
	require.NoError(t, err)
	assert.Equal(t, "second", got, "a retried provision must not leave the old token behind")
}

func TestReadTokenFromRootfsWithoutToken(t *testing.T) {
	image := newTestExt4(t)

	_, err := ReadTokenFromRootfs(context.Background(), image)
	assert.ErrorIs(t, err, ErrNoToken, "a legacy image has no token, which is not a failure")
}

func TestProvisionForBootPublishesBothCopies(t *testing.T) {
	image := newTestExt4(t)
	vsock := filepath.Join(t.TempDir(), "task-1.vsock")

	require.NoError(t, ProvisionForBoot(context.Background(), image, vsock))

	guestToken, err := ReadTokenFromRootfs(context.Background(), image)
	require.NoError(t, err)
	require.NotEmpty(t, guestToken)

	hostToken, found, err := ReadTokenFile(TokenFilePath(vsock))
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, guestToken, hostToken, "the host must hold the token the guest checks")

	info, err := os.Stat(TokenFilePath(vsock))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestProvisionForBootRequiresAPath(t *testing.T) {
	assert.Error(t, ProvisionForBoot(context.Background(), "", "/tmp/task.vsock"))
	assert.Error(t, ProvisionForBoot(context.Background(), "/tmp/rootfs.ext4", ""))
}

func TestTokenPathIsNotOnTheCommandLine(t *testing.T) {
	// /proc/cmdline is world-readable in the guest, so the token must never
	// travel there. Pin the guest path to a rootfs file instead.
	assert.True(t, strings.HasPrefix(TokenPath, "/etc/"), "the token lives in a root-owned rootfs path, not /proc")
}
