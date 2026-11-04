package golden

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// elfBytes is a stand-in for the guest agent: verifyAgentInstalled only needs a
// real ELF header to accept it, and the boot probe is what proves the real
// binary runs.
func elfBytes() []byte {
	return append([]byte{0x7f, 'E', 'L', 'F'}, []byte("fake guest agent")...)
}

func TestInstallAgent_EnablesEachInitSystem(t *testing.T) {
	tests := []struct {
		initSystem string
		script     string
		links      []string
		target     string
	}{
		{
			initSystem: InitSystemd,
			script:     "/etc/systemd/system/sc-agent.service",
			links:      []string{"/etc/systemd/system/multi-user.target.wants/sc-agent.service"},
			target:     "/etc/systemd/system/sc-agent.service",
		},
		{
			initSystem: InitOpenRC,
			script:     "/etc/init.d/sc-agent",
			links:      []string{"/etc/runlevels/default/sc-agent"},
			target:     "/etc/init.d/sc-agent",
		},
		{
			// Debian-family guests default to runlevel 2, traditional SysV to 3.
			initSystem: InitSysvinit,
			script:     "/etc/init.d/sc-agent",
			links:      []string{"/etc/rc2.d/S99sc-agent", "/etc/rc3.d/S99sc-agent"},
			target:     "/etc/init.d/sc-agent",
		},
	}

	for _, tc := range tests {
		t.Run(tc.initSystem, func(t *testing.T) {
			rootfs := t.TempDir()

			require.NoError(t, installAgent(rootfs, tc.initSystem, elfBytes()))
			require.NoError(t, verifyAgentInstalled(rootfs, tc.initSystem))

			// The binary is written executable at the path the host dials.
			info, err := os.Stat(filepath.Join(rootfs, AgentPath))
			require.NoError(t, err)
			assert.True(t, info.Mode().IsRegular())
			assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())

			// The service is enabled: every link exists, points at the service
			// file, and that file exists.
			for _, link := range tc.links {
				got, err := os.Readlink(filepath.Join(rootfs, link))
				require.NoError(t, err, link)
				assert.Equal(t, tc.target, got, link)
			}
			assert.FileExists(t, filepath.Join(rootfs, tc.script))

			// A template must never carry a token: it is per task and written
			// into the task's private rootfs during preparation.
			assert.NoFileExists(t, filepath.Join(rootfs, "etc/swarmcracker/agent-token"))
		})
	}
}

func TestInstallAgent_RefusesUnsupportedInit(t *testing.T) {
	for _, initSystem := range []string{InitTini, InitCustom, "unknown"} {
		t.Run(initSystem, func(t *testing.T) {
			err := installAgent(t.TempDir(), initSystem, elfBytes())
			require.Error(t, err)
			assert.Contains(t, err.Error(), "cannot carry the guest agent")
		})
	}
}

func TestInstallAgent_RefusesEmptyBinary(t *testing.T) {
	err := installAgent(t.TempDir(), InitSystemd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

func TestVerifyAgentInstalled_RefusesBrokenWiring(t *testing.T) {
	tests := []struct {
		name   string
		broken func(t *testing.T, rootfs string)
		want   string
	}{
		{
			name: "binary removed",
			broken: func(t *testing.T, rootfs string) {
				require.NoError(t, os.Remove(filepath.Join(rootfs, AgentPath)))
			},
			want: "/sbin/sc-agent is missing",
		},
		{
			name: "binary not executable",
			broken: func(t *testing.T, rootfs string) {
				require.NoError(t, os.Chmod(filepath.Join(rootfs, AgentPath), 0o644))
			},
			want: "not executable",
		},
		{
			name: "binary is not an ELF",
			broken: func(t *testing.T, rootfs string) {
				require.NoError(t, os.WriteFile(filepath.Join(rootfs, AgentPath), []byte("deadbeef"), 0o755))
			},
			want: "not an ELF binary",
		},
		{
			name: "unit removed",
			broken: func(t *testing.T, rootfs string) {
				require.NoError(t, os.Remove(filepath.Join(rootfs, agentUnitPath)))
			},
			want: "sc-agent.service is missing",
		},
		{
			name: "enablement link removed",
			broken: func(t *testing.T, rootfs string) {
				require.NoError(t, os.Remove(filepath.Join(rootfs,
					"/etc/systemd/system/multi-user.target.wants/sc-agent.service")))
			},
			want: "not enabled",
		},
		{
			name: "enablement link points elsewhere",
			broken: func(t *testing.T, rootfs string) {
				link := filepath.Join(rootfs, "/etc/systemd/system/multi-user.target.wants/sc-agent.service")
				require.NoError(t, os.Remove(link))
				require.NoError(t, os.Symlink("/sbin/sc-agent", link))
			},
			want: "points at",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rootfs := t.TempDir()
			require.NoError(t, installAgent(rootfs, InitSystemd, elfBytes()))
			require.NoError(t, verifyAgentInstalled(rootfs, InitSystemd))

			tc.broken(t, rootfs)

			err := verifyAgentInstalled(rootfs, InitSystemd)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// sysvInsservLayout reproduces what insserv does to the links the installer
// wrote: it renames them from S99 to its own order and stores them relative.
func sysvInsservLayout(t *testing.T, rootfs string) {
	t.Helper()
	for _, dir := range []string{"/etc/rc2.d", "/etc/rc3.d"} {
		require.NoError(t, os.Remove(filepath.Join(rootfs, dir, "S99sc-agent")))
		require.NoError(t, os.Symlink("../init.d/sc-agent", filepath.Join(rootfs, dir, "S01sc-agent")))
	}
}

// TestVerifyAgentInstalled_SysvRequiresStartparEntry covers the failure the
// first-boot probe found on Debian: the runlevel link exists, but startpar in
// makefile mode starts only what /etc/init.d/.depend.start lists.
func TestVerifyAgentInstalled_SysvRequiresStartparEntry(t *testing.T) {
	rootfs := t.TempDir()
	require.NoError(t, installAgent(rootfs, InitSysvinit, elfBytes()))
	sysvInsservLayout(t, rootfs)

	depends := filepath.Join(rootfs, "etc/init.d/.depend.start")
	require.NoError(t, os.MkdirAll(filepath.Dir(depends), 0o755))
	require.NoError(t, os.WriteFile(depends, []byte("TARGETS = bootlogs rmnologin\n"), 0o644))

	err := verifyAgentInstalled(rootfs, InitSysvinit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), ".depend.start does not list sc-agent")

	// insserv adds the service to the list; that is what makes it start.
	require.NoError(t, os.WriteFile(depends,
		[]byte("TARGETS = bootlogs sc-agent rmnologin\nrc.local: rc.local sc-agent rmnologin\n"), 0o644))
	require.NoError(t, verifyAgentInstalled(rootfs, InitSysvinit))
}

// TestVerifyAgentInstalled_SysvLegacyBootOrderingSkipsStartpar proves an image
// that opted out of the makefile mechanism is judged by its links alone.
func TestVerifyAgentInstalled_SysvLegacyBootOrderingSkipsStartpar(t *testing.T) {
	rootfs := t.TempDir()
	require.NoError(t, installAgent(rootfs, InitSysvinit, elfBytes()))
	sysvInsservLayout(t, rootfs)

	depends := filepath.Join(rootfs, "etc/init.d/.depend.start")
	require.NoError(t, os.MkdirAll(filepath.Dir(depends), 0o755))
	require.NoError(t, os.WriteFile(depends, []byte("TARGETS = bootlogs\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(rootfs, "etc/init.d/.legacy-bootordering"), nil, 0o644))

	require.NoError(t, verifyAgentInstalled(rootfs, InitSysvinit))
}

// TestVerifyAgentInstalled_SysvRejectsLinkToAnotherScript proves the check is
// about which script is started, not merely that some link exists.
func TestVerifyAgentInstalled_SysvRejectsLinkToAnotherScript(t *testing.T) {
	rootfs := t.TempDir()
	require.NoError(t, installAgent(rootfs, InitSysvinit, elfBytes()))

	link := filepath.Join(rootfs, "etc/rc3.d/S99sc-agent")
	require.NoError(t, os.Remove(link))
	require.NoError(t, os.Symlink("/etc/init.d/networking", link))

	err := verifyAgentInstalled(rootfs, InitSysvinit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not enabled")
	assert.Contains(t, err.Error(), "/etc/init.d/networking")
}
