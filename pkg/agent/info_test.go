//go:build !integration

package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// infoFixture lays out a guest's /proc and /etc under one root, which the two
// readers are pointed at for the duration of a test.
type infoFixture struct {
	root     string
	procRoot string
	etcRoot  string
}

// newInfoFixture installs the fixture in place of the guest's own files.
func newInfoFixture(t *testing.T) *infoFixture {
	t.Helper()

	root := t.TempDir()
	f := &infoFixture{
		root:     root,
		procRoot: filepath.Join(root, "proc"),
		etcRoot:  filepath.Join(root, "etc"),
	}
	require.NoError(t, os.MkdirAll(filepath.Join(f.procRoot, "sys", "kernel"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(f.procRoot, "net"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(f.etcRoot, "bin"), 0o755))

	origProc, origEtc := procRoot, etcRoot
	procRoot, etcRoot = f.procRoot, f.etcRoot
	t.Cleanup(func() { procRoot, etcRoot = origProc, origEtc })
	return f
}

// write writes one fixture file, creating its directory.
func (f *infoFixture) write(t *testing.T, rel, body string) {
	t.Helper()

	path := filepath.Join(f.root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

// TestReadGuestInfoReportsTheGuestsOwnFacts pins every field to the file it comes
// from, so a field filled from the wrong source fails here rather than reading
// plausibly in a report.
func TestReadGuestInfoReportsTheGuestsOwnFacts(t *testing.T) {
	f := newInfoFixture(t)
	f.write(t, "proc/sys/kernel/osrelease", "6.1.128-sc\n")
	f.write(t, "proc/sys/kernel/version", "#1 SMP PREEMPT_DYNAMIC\n")
	f.write(t, "proc/sys/kernel/hostname", "sandbox-7f3a\n")
	f.write(t, "proc/uptime", "1234.57 4321.00\n")
	f.write(t, "proc/meminfo", "MemTotal:         508000 kB\nMemFree:           1000 kB\n")
	f.write(t, "etc/machine-id", "0123456789abcdef0123456789abcdef\n")
	f.write(t, "etc/os-release", "# comment\nID=alpine\nNAME=\"Alpine Linux\"\nPRETTY_NAME=\"Alpine Linux v3.20\"\n")
	require.NoError(t, os.WriteFile(filepath.Join(f.etcRoot, "bin", "tar"), nil, 0o644))

	origDirs := toolDirs
	toolDirs = []string{filepath.Join(f.etcRoot, "bin")}
	t.Cleanup(func() { toolDirs = origDirs })

	info, err := readGuestInfo()
	require.NoError(t, err)

	assert.Equal(t, "sandbox-7f3a", info.Hostname)
	assert.Equal(t, "0123456789abcdef0123456789abcdef", info.MachineID)
	assert.Equal(t, "6.1.128-sc", info.KernelRelease)
	assert.Equal(t, "#1 SMP PREEMPT_DYNAMIC", info.KernelVersion)
	assert.NotEmpty(t, info.Machine, "the architecture comes from the kernel, not from a caller's request")
	assert.Equal(t, "Alpine Linux v3.20", info.OSName)
	assert.Equal(t, "alpine", info.OSID)
	assert.Equal(t, uint64(508000*1024), info.MemoryTotalBytes)
	assert.Equal(t, 1234.57, info.UptimeSeconds)
	assert.Equal(t, cpuCount(), info.CPUs)
	assert.Equal(t, Version, info.Protocol)
	assert.Equal(t, AgentVersion, info.AgentVersion)
	assert.Equal(t, BuildID, info.BuildID)
	assert.Equal(t, BootID(), info.BootID)
	// Only the one probe file exists in the fixture's bin directory.
	assert.Equal(t, []string{"tar"}, info.Tools)
}

// TestReadGuestInfoToleratesATemplateWithoutIdentityFiles covers an image that
// ships neither os-release nor a machine id: the fields are empty, which says
// exactly that, and the report is still a report.
func TestReadGuestInfoToleratesATemplateWithoutIdentityFiles(t *testing.T) {
	f := newInfoFixture(t)
	f.write(t, "proc/sys/kernel/osrelease", "6.1.128-sc\n")
	f.write(t, "proc/sys/kernel/version", "#1 SMP\n")
	f.write(t, "proc/sys/kernel/hostname", "guest\n")
	f.write(t, "proc/uptime", "1.00 1.00\n")
	f.write(t, "proc/meminfo", "MemTotal:         1000 kB\n")

	info, err := readGuestInfo()
	require.NoError(t, err)
	assert.Empty(t, info.MachineID)
	assert.Empty(t, info.OSID)
	assert.Empty(t, info.OSName)
	assert.True(t, info.CPUs > 0)
}

// TestReadGuestInfoFailsWithoutProc proves the required files stay required: a
// guest whose /proc cannot be read reports a failure instead of a report full of
// zeros that a scheduler would believe.
func TestReadGuestInfoFailsWithoutProc(t *testing.T) {
	f := newInfoFixture(t)
	// Everything except the uptime file, which is the one left out.
	f.write(t, "proc/sys/kernel/osrelease", "6.1.128-sc\n")
	f.write(t, "proc/sys/kernel/version", "#1 SMP\n")
	f.write(t, "proc/sys/kernel/hostname", "guest\n")
	f.write(t, "proc/meminfo", "MemTotal:         1000 kB\n")

	_, err := readGuestInfo()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "uptime")
}

// TestReadOSReleasePrefersPrettyName covers the two shapes a template's identity
// file is written in, and that an unquoted value keeps its spaces.
func TestReadOSReleasePrefersPrettyName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "os-release")

	require.NoError(t, os.WriteFile(path, []byte("ID=debian\nNAME=Debian GNU/Linux\n"), 0o644))
	id, name := readOSRelease(path)
	assert.Equal(t, "debian", id)
	assert.Equal(t, "Debian GNU/Linux", name)

	require.NoError(t, os.WriteFile(path, []byte("ID='custom'\nPRETTY_NAME='Custom 1.0'\n"), 0o644))
	id, name = readOSRelease(path)
	assert.Equal(t, "custom", id)
	assert.Equal(t, "Custom 1.0", name)
}
