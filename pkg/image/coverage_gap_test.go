package image

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// injectDumbInitIntoDir tests (0.0%)
// ============================================================================

func TestInjectDumbInitIntoDir_Basic(t *testing.T) {
	ii := NewInitInjector(&InitSystemConfig{Type: InitSystemDumbInit, GracePeriodSec: 10})
	tmpDir := t.TempDir()

	err := ii.injectDumbInitIntoDir(tmpDir)
	require.NoError(t, err)

	dumbInitPath := filepath.Join(tmpDir, "sbin", "dumb-init")
	info, err := os.Stat(dumbInitPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0755), info.Mode())

	data, err := os.ReadFile(dumbInitPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "#!/bin/sh")

	initLink := filepath.Join(tmpDir, "init")
	linkTarget, err := os.Readlink(initLink)
	require.NoError(t, err)
	assert.Equal(t, "/sbin/init", linkTarget)
}

func TestInjectDumbInitIntoDir_ExistingSbin(t *testing.T) {
	ii := NewInitInjector(&InitSystemConfig{Type: InitSystemDumbInit})
	tmpDir := t.TempDir()

	// Pre-create /sbin with a file
	sbinDir := filepath.Join(tmpDir, "sbin")
	require.NoError(t, os.MkdirAll(sbinDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sbinDir, "existing"), []byte("keep"), 0644))

	err := ii.injectDumbInitIntoDir(tmpDir)
	require.NoError(t, err)

	// Existing file preserved
	_, err = os.Stat(filepath.Join(sbinDir, "existing"))
	assert.NoError(t, err)

	// dumb-init created
	_, err = os.Stat(filepath.Join(sbinDir, "dumb-init"))
	assert.NoError(t, err)
}

// ============================================================================
// injectTini stub test (0.0%)
// ============================================================================

func TestInjectTini_Stub(t *testing.T) {
	ii := NewInitInjector(&InitSystemConfig{Type: InitSystemTini})
	err := ii.injectTini("/fake/rootfs")
	assert.NoError(t, err)
}

// ============================================================================
// validateNonCriticalSymlinks tests (0.0%)
// ============================================================================

func TestValidateNonCriticalSymlinks_NoSymlinks(t *testing.T) {
	tmpDir := t.TempDir()
	warnings := validateNonCriticalSymlinks(tmpDir)
	assert.Empty(t, warnings)
}

func TestValidateNonCriticalSymlinks_Dangling(t *testing.T) {
	tmpDir := t.TempDir()

	binDir := filepath.Join(tmpDir, "bin")
	usrBinDir := filepath.Join(tmpDir, "usr", "bin")
	require.NoError(t, os.MkdirAll(binDir, 0755))
	require.NoError(t, os.MkdirAll(usrBinDir, 0755))

	// Create dangling symlinks
	require.NoError(t, os.Symlink("nonexistent", filepath.Join(binDir, "sh")))
	require.NoError(t, os.Symlink("nonexistent", filepath.Join(binDir, "bash")))
	require.NoError(t, os.Symlink("nonexistent", filepath.Join(usrBinDir, "env")))

	warnings := validateNonCriticalSymlinks(tmpDir)
	assert.NotEmpty(t, warnings)
	assert.Contains(t, warnings[0], "dangling symlink")
}

func TestValidateNonCriticalSymlinks_ValidSymlinks(t *testing.T) {
	tmpDir := t.TempDir()

	binDir := filepath.Join(tmpDir, "bin")
	usrBinDir := filepath.Join(tmpDir, "usr", "bin")
	require.NoError(t, os.MkdirAll(binDir, 0755))
	require.NoError(t, os.MkdirAll(usrBinDir, 0755))

	// Create real targets
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "dash"), []byte("x"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "bash-real"), []byte("x"), 0755))

	// Create valid symlinks pointing within tmpDir
	require.NoError(t, os.Symlink(filepath.Join(binDir, "dash"), filepath.Join(binDir, "sh")))
	require.NoError(t, os.Symlink(filepath.Join(binDir, "bash-real"), filepath.Join(binDir, "bash")))

	warnings := validateNonCriticalSymlinks(tmpDir)
	// bin/sh and bin/bash are valid symlinks
	// usr/bin/env doesn't exist (no warning)
	assert.Empty(t, warnings)
}

// ============================================================================
// GetOCIInfo tests (0.0%)
// ============================================================================

func TestGetOCIInfo_Nil(t *testing.T) {
	ip := &ImagePreparer{ociInfo: nil}
	assert.Nil(t, ip.GetOCIInfo())
}

func TestGetOCIInfo_NonNil(t *testing.T) {
	oci := &OCIImageInfo{
		Architecture: "amd64",
		OS:           "linux",
	}
	ip := &ImagePreparer{ociInfo: oci}
	result := ip.GetOCIInfo()
	assert.NotNil(t, result)
	assert.Equal(t, "amd64", result.Architecture)
	assert.Equal(t, "linux", result.OS)
}

// ============================================================================
// injectInitSystem — exercise mount-failure path (already tested, but
// adding more scenarios to close gaps)
// ============================================================================

func TestInjectInitSystem_MountFails_Graceful(t *testing.T) {
	ip := &ImagePreparer{
		config: &PreparerConfig{
			InitSystem:      "tini",
			InitGracePeriod: 10,
		},
		initInjector: NewInitInjector(&InitSystemConfig{Type: InitSystemTini}),
	}

	// Non-existent path → mountExt4 fails → returns nil (graceful)
	err := ip.injectInitSystem("/nonexistent/rootfs.ext4")
	// Should return nil (mount failure is non-fatal)
	assert.NoError(t, err)
}

// ============================================================================
// hasSysvinit — test uncovered branches (42.1% → higher)
// ============================================================================

func TestHasSysvinit_SystemdBlock(t *testing.T) {
	// Create a directory that looks like systemd
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "usr", "lib", "systemd"), 0755))

	assert.False(t, hasSysvinit(tmpDir), "systemd dir should block sysvinit detection")
}

func TestHasSysvinit_RegularSbinInit(t *testing.T) {
	tmpDir := t.TempDir()
	sbinDir := filepath.Join(tmpDir, "sbin")
	require.NoError(t, os.MkdirAll(sbinDir, 0755))
	// Create /sbin/init as a regular file
	require.NoError(t, os.WriteFile(filepath.Join(sbinDir, "init"), []byte("#!/bin/sh"), 0755))

	assert.True(t, hasSysvinit(tmpDir), "regular /sbin/init should indicate sysvinit")
}

func TestHasSysvinit_SymlinkNonSystemd(t *testing.T) {
	tmpDir := t.TempDir()
	sbinDir := filepath.Join(tmpDir, "sbin")
	require.NoError(t, os.MkdirAll(sbinDir, 0755))
	// Create /sbin/init as symlink to non-systemd target
	require.NoError(t, os.Symlink("/bin/busybox", filepath.Join(sbinDir, "init")))

	assert.True(t, hasSysvinit(tmpDir), "symlink to non-systemd should indicate sysvinit")
}

func TestHasSysvinit_InitDPath(t *testing.T) {
	tmpDir := t.TempDir()
	// Create /etc/init.d directory without OpenRC markers
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "etc", "init.d"), 0755))

	assert.True(t, hasSysvinit(tmpDir), "init.d without OpenRC should indicate sysvinit")
}

func TestHasSysvinit_InitDWithRunlevels(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "etc", "init.d"), 0755))
	// Create runlevels dir (OpenRC marker) — should NOT be sysvinit
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "etc", "runlevels"), 0755))

	assert.False(t, hasSysvinit(tmpDir), "init.d with runlevels should NOT be sysvinit")
}

func TestHasSysvinit_InitDWithRcBinary(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "etc", "init.d"), 0755))
	sbinDir := filepath.Join(tmpDir, "sbin")
	require.NoError(t, os.MkdirAll(sbinDir, 0755))
	// Create /sbin/rc (OpenRC marker)
	require.NoError(t, os.WriteFile(filepath.Join(sbinDir, "rc"), []byte("x"), 0755))

	assert.False(t, hasSysvinit(tmpDir), "init.d with rc binary should NOT be sysvinit")
}

// ============================================================================
// hasOpenRC — test uncovered branches (61.1% → higher)
// ============================================================================

func TestHasOpenRC_RcBinary(t *testing.T) {
	tmpDir := t.TempDir()
	sbinDir := filepath.Join(tmpDir, "sbin")
	require.NoError(t, os.MkdirAll(sbinDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sbinDir, "rc"), []byte("x"), 0755))

	assert.True(t, hasOpenRC(tmpDir))
}

func TestHasOpenRC_RcStatus(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "etc", "init.d"), 0755))
	sbinDir := filepath.Join(tmpDir, "sbin")
	require.NoError(t, os.MkdirAll(sbinDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sbinDir, "rc-status"), []byte("x"), 0755))

	assert.True(t, hasOpenRC(tmpDir))
}

// ============================================================================
// injectTiniIntoDir — test more branches (69.2% → higher)
// ============================================================================

func TestInjectTiniIntoDir_WithDumbInit(t *testing.T) {
	ii := NewInitInjector(&InitSystemConfig{Type: InitSystemDumbInit, GracePeriodSec: 10})
	tmpDir := t.TempDir()

	// Should use injectTiniIntoDir internally since InjectIntoDir falls through for Unknown type
	// Let's test the Scratch → tini injection path through InjectIntoDir
	info := &OCIImageInfo{}
	err := ii.InjectIntoDir(tmpDir, info)
	require.NoError(t, err)

	// Should have created tini and init wrappers
	_, err = os.Stat(filepath.Join(tmpDir, "sbin", "tini"))
	assert.NoError(t, err)
}

func TestInjectTiniIntoDir_WithImageInfo(t *testing.T) {
	ii := NewInitInjector(&InitSystemConfig{Type: InitSystemTini})
	tmpDir := t.TempDir()

	info := &OCIImageInfo{
		Entrypoint: []string{"/usr/bin/nginx"},
		Cmd:        []string{"-g", "daemon off"},
		Env:        []string{"PATH=/usr/local/sbin:/usr/local/bin"},
		WorkDir:    "/",
	}
	err := ii.injectTiniIntoDir(tmpDir, info)
	require.NoError(t, err)

	// Check tini binary
	data, err := os.ReadFile(filepath.Join(tmpDir, "sbin", "tini"))
	require.NoError(t, err)
	assert.NotEmpty(t, data)
}

// ============================================================================
// handleMounts — exercise mount-failure path 
// ============================================================================

// ============================================================================
// createEssentialDirs — test more branches (62.5% → higher)
// ============================================================================

func TestCreateEssentialDirs_AllExist(t *testing.T) {
	tmpDir := t.TempDir()
	// Pre-create some dirs
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "proc"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "dev"), 0755))

	err := createEssentialDirs(tmpDir)
	assert.NoError(t, err)
}

func TestCreateEssentialDirs_ReadOnlyParent(t *testing.T) {
	// Skip if not root — we can't make read-only dir without root
	t.Skip("requires root to test read-only parent")
}

// ============================================================================
// injectEssentialFiles — test more branches (60.0% → higher)
// ============================================================================

func TestInjectEssentialFiles_ExistingFiles(t *testing.T) {
	tmpDir := t.TempDir()
	// Pre-create /etc with resolv.conf and hosts
	etcDir := filepath.Join(tmpDir, "etc")
	require.NoError(t, os.MkdirAll(etcDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(etcDir, "resolv.conf"), []byte("nameserver 1.1.1.1"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(etcDir, "hosts"), []byte("127.0.0.1 localhost"), 0644))

	err := injectEssentialFiles(tmpDir, "test-image-id")
	assert.NoError(t, err)
}

func TestInjectEssentialFiles_NoEtcDir(t *testing.T) {
	tmpDir := t.TempDir()
	// No /etc directory — injectEssentialFiles creates it
	err := injectEssentialFiles(tmpDir, "test-image-id")
	assert.NoError(t, err)

	// Verify files were created
	_, err = os.Stat(filepath.Join(tmpDir, "etc", "resolv.conf"))
	assert.NoError(t, err)
	_, err = os.Stat(filepath.Join(tmpDir, "etc", "hosts"))
	assert.NoError(t, err)
	_, err = os.Stat(filepath.Join(tmpDir, "etc", "machine-id"))
	assert.NoError(t, err)
}

// ============================================================================
// shellEscape — test more branches (80.0% → higher)
// ============================================================================

func TestShellEscape_AlreadyDoubleQuoted(t *testing.T) {
	assert.Equal(t, `"already"`, shellEscape(`"already"`))
}

func TestShellEscape_Empty(t *testing.T) {
	assert.Equal(t, "", shellEscape(""))
}

func TestShellEscape_NoSpecialChars(t *testing.T) {
	assert.Equal(t, "simple", shellEscape("simple"))
}

func TestShellEscape_Backticks(t *testing.T) {
	result := shellEscape("cmd`id`")
	assert.Contains(t, result, "\\`")
}

func TestShellEscape_DollarSign(t *testing.T) {
	result := shellEscape("$HOME")
	assert.Contains(t, result, "\\$")
}

// ============================================================================
// isSystemd — test more branches (73.3% → higher)
// ============================================================================

func TestIsSystemd_SystemdSymlink(t *testing.T) {
	tmpDir := t.TempDir()
	// Create /sbin/init as symlink to /lib/systemd/systemd
	libDir := filepath.Join(tmpDir, "usr", "lib", "systemd")
	require.NoError(t, os.MkdirAll(libDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(libDir, "systemd"), []byte("x"), 0755))

	sbinDir := filepath.Join(tmpDir, "sbin")
	require.NoError(t, os.MkdirAll(sbinDir, 0755))
	require.NoError(t, os.Symlink("/usr/lib/systemd/systemd", filepath.Join(sbinDir, "init")))

	assert.True(t, isSystemd(tmpDir))
}

func TestIsSystemd_LibSystemdDir(t *testing.T) {
	tmpDir := t.TempDir()
	libDir := filepath.Join(tmpDir, "usr", "lib", "systemd")
	require.NoError(t, os.MkdirAll(libDir, 0755))
	// Create the systemd binary file
	require.NoError(t, os.WriteFile(filepath.Join(libDir, "systemd"), []byte("x"), 0755))
	assert.True(t, isSystemd(tmpDir))
}

func TestIsSystemd_NoSystemd(t *testing.T) {
	tmpDir := t.TempDir()
	assert.False(t, isSystemd(tmpDir))
}

// ============================================================================
// InjectIntoDir — test more init types (78.6% → higher)
// ============================================================================

func TestInjectIntoDir_OpenRC_Preserved(t *testing.T) {
	ii := NewInitInjector(&InitSystemConfig{Type: InitSystemDumbInit})
	tmpDir := t.TempDir()

	// Set up OpenRC markers
	sbinDir := filepath.Join(tmpDir, "sbin")
	require.NoError(t, os.MkdirAll(sbinDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sbinDir, "openrc"), []byte("x"), 0755))

	info := &OCIImageInfo{}
	err := ii.InjectIntoDir(tmpDir, info)
	assert.NoError(t, err)

	// Should have created /init symlink
	linkTarget, err := os.Readlink(filepath.Join(tmpDir, "init"))
	require.NoError(t, err)
	assert.Equal(t, "/sbin/init", linkTarget)
}

func TestInjectIntoDir_Sysvinit_Preserved(t *testing.T) {
	ii := NewInitInjector(&InitSystemConfig{Type: InitSystemDumbInit})
	tmpDir := t.TempDir()

	// Set up sysvinit markers
	sbinDir := filepath.Join(tmpDir, "sbin")
	require.NoError(t, os.MkdirAll(sbinDir, 0755))
	// Create /sbin/init as regular file
	require.NoError(t, os.WriteFile(filepath.Join(sbinDir, "init"), []byte("#!/bin/sh"), 0755))

	info := &OCIImageInfo{}
	err := ii.InjectIntoDir(tmpDir, info)
	assert.NoError(t, err)
}

func TestInjectIntoDir_Tini_Preserved(t *testing.T) {
	ii := NewInitInjector(&InitSystemConfig{Type: InitSystemDumbInit})
	tmpDir := t.TempDir()

	// Set up tini binary
	sbinDir := filepath.Join(tmpDir, "sbin")
	require.NoError(t, os.MkdirAll(sbinDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sbinDir, "tini"), []byte("x"), 0755))

	info := &OCIImageInfo{}
	err := ii.InjectIntoDir(tmpDir, info)
	assert.NoError(t, err)
}

func TestInjectIntoDir_Disabled(t *testing.T) {
	ii := NewInitInjector(&InitSystemConfig{Type: InitSystemNone})
	tmpDir := t.TempDir()

	info := &OCIImageInfo{}
	// When disabled, IsEnabled returns false → no-op
	err := ii.InjectIntoDir(tmpDir, info)
	assert.NoError(t, err)
}

func TestInjectIntoDir_Incompatible_Systemd(t *testing.T) {
	ii := NewInitInjector(&InitSystemConfig{Type: InitSystemTini})
	tmpDir := t.TempDir()

	// Prevent Scratch detection: add /bin/sh
	binDir := filepath.Join(tmpDir, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "sh"), []byte("#!/bin/sh"), 0755))

	// Set up systemd markers to trigger InitTypeIncompatible
	libDir := filepath.Join(tmpDir, "usr", "lib", "systemd")
	require.NoError(t, os.MkdirAll(libDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(libDir, "systemd"), []byte("x"), 0755))

	info := &OCIImageInfo{}
	err := ii.InjectIntoDir(tmpDir, info)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "incompatible")
}

func TestInjectIntoDir_ScratchDetection(t *testing.T) {
	// Create a scratch image (no paths, no init)
	ii := NewInitInjector(&InitSystemConfig{Type: InitSystemTini})
	tmpDir := t.TempDir()

	info := &OCIImageInfo{}
	err := ii.InjectIntoDir(tmpDir, info)
	// Should detect as Unknown and inject tini
	assert.NoError(t, err)
}

// ============================================================================
// handleBindMount — test file-copy branch (72.2% → higher)
// ============================================================================

func TestHandleBindMount_SourceIsFile(t *testing.T) {
	ip := &ImagePreparer{config: &PreparerConfig{}}

	tmpDir := t.TempDir()
	sourceFile := filepath.Join(t.TempDir(), "source.txt")
	require.NoError(t, os.WriteFile(sourceFile, []byte("content"), 0644))

	mount := &types.Mount{
		Source: sourceFile,
		Target: "/etc/config.txt",
	}

	err := ip.handleBindMount(tmpDir, mount)
	require.NoError(t, err)

	// Verify file was copied
	copied, err := os.ReadFile(filepath.Join(tmpDir, mount.Target))
	require.NoError(t, err)
	assert.Equal(t, "content", string(copied))
}

func TestHandleBindMount_StatError(t *testing.T) {
	ip := &ImagePreparer{config: &PreparerConfig{}}

	tmpDir := t.TempDir()
	// Source file is a directory path without execute permission
	sourceDir := filepath.Join(t.TempDir(), "noperm")
	require.NoError(t, os.MkdirAll(sourceDir, 0000))

	mount := &types.Mount{
		Source: filepath.Join(sourceDir, "inaccessible"),
		Target: "/test",
	}

	err := ip.handleBindMount(tmpDir, mount)
	// Should be an error (not ENOENT which is handled gracefully)
	assert.Error(t, err)
}

// ============================================================================
// copyDirectory — test error path (80.0% → higher)
// ============================================================================

func TestCopyDirectory_NonExistentSource(t *testing.T) {
	err := copyDirectory("/nonexistent/path", "/tmp/dst")
	assert.Error(t, err)
}

// ============================================================================
// strContains — test false return (75.0% → higher)
// ============================================================================

func TestStrContains_NotFound(t *testing.T) {
	assert.False(t, strContains("hello world", "xyz"))
	assert.False(t, strContains("abc", "abcd"))
	assert.False(t, strContains("", "x"))
}

// ============================================================================
// injectBusybox — test with existing dirs (84.0% → higher)
// ============================================================================

func TestInjectBusybox_ExistingDirs(t *testing.T) {
	tmpDir := t.TempDir()
	// Pre-create /bin and /sbin
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "bin"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "sbin"), 0755))

	err := injectBusybox(tmpDir)
	assert.NoError(t, err)
}

func TestHandleMounts_MountFails_Graceful(t *testing.T) {
	ip := &ImagePreparer{
		config: &PreparerConfig{},
	}

	// Non-existent path → mountExt4 fails → returns nil (graceful)
	err := ip.handleMounts(nil, nil, "/nonexistent/rootfs.ext4", nil)
	assert.NoError(t, err)
}
