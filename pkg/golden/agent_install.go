package golden

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// sysvStartLink matches the start links sysvinit reads, whatever number
// insserv assigned.
var sysvStartLink = regexp.MustCompile(`^S[0-9][0-9]sc-agent$`)

// Guest agent layout inside a golden image. The binary path matches the one the
// preparer injects into single-workload images and the one the host dials on
// the vsock port, so a host has one place to look regardless of how a VM was
// built.
const (
	// AgentPath is the guest agent binary inside the image.
	AgentPath = "/sbin/sc-agent"
	// agentUnitPath is the systemd unit that starts the agent at boot.
	agentUnitPath = "/etc/systemd/system/sc-agent.service"
	// agentInitScript is the SysV/OpenRC service script.
	agentInitScript = "/etc/init.d/sc-agent"
)

// agentEnable is one runlevel directory the init system reads at boot. name is
// the exact link name, or empty for any S<nn><service> link, because insserv
// renames and reorders the link it creates.
type agentEnable struct {
	dir  string
	name string
}

// agentService is the wiring one init system needs: the file that describes the
// service and the runlevel links that make the init system start it at boot.
type agentService struct {
	// script is the unit (systemd) or init script (OpenRC/SysV) to write.
	script string
	// scriptPath is where script is written.
	scriptPath string
	// scriptMode is scriptPath's permission bits.
	scriptMode os.FileMode
	// enable lists the runlevel directories that must contain a start link.
	// Enabling a service is precisely those links existing, so verifying them
	// is verifying the service starts at boot.
	enable []agentEnable
}

// agentServiceFor returns the wiring for initSystem. Only real init systems are
// accepted: a tini/custom image is assembled by the task-time preparer, not by
// this builder, and an unknown name must not silently get a service that no
// init will read.
func agentServiceFor(initSystem string) (agentService, error) {
	switch initSystem {
	case InitSystemd:
		return agentService{
			script:     systemdUnit,
			scriptPath: agentUnitPath,
			scriptMode: 0o644,
			enable: []agentEnable{
				{dir: "/etc/systemd/system/multi-user.target.wants", name: "sc-agent.service"},
			},
		}, nil
	case InitOpenRC:
		return agentService{
			script:     openRCService,
			scriptPath: agentInitScript,
			scriptMode: 0o755,
			enable: []agentEnable{
				{dir: "/etc/runlevels/default", name: "sc-agent"},
			},
		}, nil
	case InitSysvinit:
		// Debian-family guests default to runlevel 2 and traditional SysV to 3,
		// so the service is enabled in both. Linking only rc3.d leaves a Debian
		// guest booting with no agent, which the first-boot probe catches.
		return agentService{
			script:     sysvService,
			scriptPath: agentInitScript,
			scriptMode: 0o755,
			enable: []agentEnable{
				{dir: "/etc/rc2.d"},
				{dir: "/etc/rc3.d"},
			},
		}, nil
	default:
		return agentService{}, fmt.Errorf("init system %q cannot carry the guest agent (want %s, %s, or %s)",
			initSystem, InitSystemd, InitOpenRC, InitSysvinit)
	}
}

// installAgent writes the guest agent into rootfsDir and enables it for
// initSystem. It never writes a token: the token is per task and the node
// writes it into the task's private rootfs during preparation, so a template
// carrying one would both be useless to every other task and leak a credential
// into a shared artifact.
//
// The caller must verify afterwards (verifyAgentInstalled) because a later
// build step such as a seal script can still remove what this writes.
func installAgent(rootfsDir, initSystem string, binary []byte) error {
	if len(binary) == 0 {
		return fmt.Errorf("guest agent binary is empty; build it with `make binaries`")
	}
	service, err := agentServiceFor(initSystem)
	if err != nil {
		return err
	}

	if err := writeRootfsFile(rootfsDir, AgentPath, binary, 0o755); err != nil {
		return err
	}
	if err := writeRootfsFile(rootfsDir, service.scriptPath, []byte(service.script), service.scriptMode); err != nil {
		return err
	}

	for _, enable := range service.enable {
		name := enable.name
		if name == "" {
			name = sysvLinkName
		}
		linkPath := filepath.Join(rootfsDir, enable.dir, name)
		if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", enable.dir, err)
		}
		// Replace whatever is there rather than writing through it: an image
		// may ship its own dangling link or a file with this name, and a
		// symlink written through would land outside the rootfs.
		if err := os.Remove(linkPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("replace %s: %w", linkPath, err)
		}
		if err := os.Symlink(service.scriptPath, linkPath); err != nil {
			return fmt.Errorf("enable %s: %w", linkPath, err)
		}
	}
	return nil
}

// verifyAgentInstalled proves the built rootfs really carries the agent and
// that initSystem will start it at boot. A build that cannot show both must
// fail: the template would boot without a control channel, and a sandbox made
// from it could not exec, copy files, report health, or be prepared.
func verifyAgentInstalled(rootfsDir, initSystem string) error {
	service, err := agentServiceFor(initSystem)
	if err != nil {
		return err
	}

	info, err := os.Lstat(filepath.Join(rootfsDir, AgentPath))
	if err != nil {
		return fmt.Errorf("%s is missing: %w", AgentPath, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file (mode %s)", AgentPath, info.Mode())
	}
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s is not executable (mode %s)", AgentPath, info.Mode())
	}
	if err := verifyELF(filepath.Join(rootfsDir, AgentPath)); err != nil {
		return err
	}

	if _, err := os.Stat(filepath.Join(rootfsDir, service.scriptPath)); err != nil {
		return fmt.Errorf("%s is missing: %w", service.scriptPath, err)
	}

	for _, enable := range service.enable {
		link, err := findStartLink(rootfsDir, enable)
		if err != nil {
			return err
		}
		resolved, err := resolveLink(rootfsDir, link)
		if err != nil {
			return err
		}
		if resolved != service.scriptPath {
			return fmt.Errorf("%s is not enabled: %s points at %s, want %s",
				initSystem, strings.TrimPrefix(link, rootfsDir), resolved, service.scriptPath)
		}
		if _, err := os.Stat(filepath.Join(rootfsDir, resolved)); err != nil {
			return fmt.Errorf("%s is enabled but %s is missing: %w", initSystem, resolved, err)
		}
	}

	if initSystem == InitSysvinit {
		if err := verifySysvStartOrder(rootfsDir); err != nil {
			return err
		}
	}
	return nil
}

// sysvLinkName is the start link installAgent writes before the image's own
// tooling (insserv) reorders or renames it.
const sysvLinkName = "S99sc-agent"

// sysvStartparScript regenerates the SysV dependency lists after the start
// links are written. Debian-family init runs startpar in makefile mode, which
// starts only the services listed in /etc/init.d/.depend.*: without this the
// link exists and the service still never runs. Images without insserv or
// update-rc.d keep the plain runlevel links and are proved by the first boot.
const sysvStartparScript = `set -e
if command -v insserv >/dev/null 2>&1; then
    insserv -f sc-agent
elif command -v update-rc.d >/dev/null 2>&1; then
    update-rc.d sc-agent defaults 99
fi
`

// findStartLink returns the runlevel link that starts the agent in enable.dir.
// The name is fixed for systemd and OpenRC; for SysV any S<nn>sc-agent link is
// accepted because insserv owns the number.
func findStartLink(rootfsDir string, enable agentEnable) (string, error) {
	if enable.name != "" {
		link := filepath.Join(rootfsDir, enable.dir, enable.name)
		if _, err := os.Lstat(link); err != nil {
			return "", fmt.Errorf("not enabled: %s is missing: %w", filepath.Join(enable.dir, enable.name), err)
		}
		return link, nil
	}

	entries, err := os.ReadDir(filepath.Join(rootfsDir, enable.dir))
	if err != nil {
		return "", fmt.Errorf("not enabled: read %s: %w", enable.dir, err)
	}
	for _, entry := range entries {
		if !sysvStartLink.MatchString(entry.Name()) {
			continue
		}
		return filepath.Join(rootfsDir, enable.dir, entry.Name()), nil
	}
	return "", fmt.Errorf("not enabled: no S<nn>sc-agent link in %s", enable.dir)
}

// resolveLink resolves a link inside a rootfs without touching the host's own
// filesystem: an absolute target is a guest path, not a host path.
func resolveLink(rootfsDir, link string) (string, error) {
	target, err := os.Readlink(link)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", strings.TrimPrefix(link, rootfsDir), err)
	}
	if filepath.IsAbs(target) {
		return filepath.Clean(target), nil
	}
	guestDir := strings.TrimPrefix(filepath.Dir(link), rootfsDir)
	return filepath.Clean(filepath.Join(guestDir, target)), nil
}

// verifySysvStartOrder proves the service will actually be started by a
// Debian-family init. sysvinit's rc uses startpar in makefile mode, which only
// starts services listed in /etc/init.d/.depend.start: a runlevel link alone
// leaves the service silently unstarted, which is exactly the failure the
// first-boot probe found on Debian. Images that do not use that mechanism (no
// .depend.start, or legacy boot ordering) are checked by their links alone.
func verifySysvStartOrder(rootfsDir string) error {
	depends := filepath.Join(rootfsDir, "/etc/init.d/.depend.start")
	if info, err := os.Stat(filepath.Join(rootfsDir, "/etc/init.d/.legacy-bootordering")); err == nil && info.Mode().IsRegular() {
		return nil
	}
	info, statErr := os.Stat(depends)
	if os.IsNotExist(statErr) {
		return nil
	}
	if statErr != nil {
		return fmt.Errorf("stat /etc/init.d/.depend.start: %w", statErr)
	}
	if info.Size() == 0 {
		return nil
	}
	data, err := os.ReadFile(depends)
	if err != nil {
		return fmt.Errorf("read /etc/init.d/.depend.start: %w", err)
	}
	if !strings.Contains(string(data), "sc-agent") {
		return fmt.Errorf("sysvinit will not start the agent: /etc/init.d/.depend.start does not list sc-agent")
	}
	return nil
}

// verifyELF checks the file starts with an ELF header, so a truncated or
// placeholder write cannot pass as the guest agent.
func verifyELF(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var header [4]byte
	if _, err := f.Read(header[:]); err != nil {
		return fmt.Errorf("%s is too small to be an ELF binary: %w", path, err)
	}
	if header != [4]byte{0x7f, 'E', 'L', 'F'} {
		return fmt.Errorf("%s is not an ELF binary", path)
	}
	return nil
}

// writeRootfsFile writes data to rootfsDir/guestPath, creating parents and
// removing any existing file or symlink first so the write always lands inside
// the rootfs.
func writeRootfsFile(rootfsDir, guestPath string, data []byte, mode os.FileMode) error {
	path := filepath.Join(rootfsDir, guestPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(guestPath), err)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("replace %s: %w", guestPath, err)
	}
	// The mode is the guest file's mode, not a host policy, so the exec bit on
	// the agent and init scripts is intentional.
	if err := os.WriteFile(path, data, mode); err != nil { //nolint:gosec // guest file mode
		return fmt.Errorf("write %s: %w", guestPath, err)
	}
	return nil
}

// systemdUnit starts the agent as soon as the local filesystem is up. The agent
// needs only /dev/vsock, which the kernel probes before init runs, so it does
// not wait for networking: a host may need to reach it to configure the guest.
// Restart=always keeps it present across a crash, and no token is referenced:
// the agent reads its default token path only when the node writes one.
const systemdUnit = `[Unit]
Description=SwarmCracker guest agent
After=local-fs.target
Wants=local-fs.target

[Service]
Type=simple
ExecStart=/sbin/sc-agent -port 52
Restart=always
RestartSec=1

[Install]
WantedBy=multi-user.target
`

// openRCService runs the agent under supervise-daemon, which restarts it if it
// exits while /dev/vsock is still being probed.
const openRCService = `#!/sbin/openrc-run
name="sc-agent"
description="SwarmCracker guest agent"
command="/sbin/sc-agent"
command_args="-port 52"
command_background=true
pidfile="/run/sc-agent.pid"
supervisor="supervise-daemon"
respawn_delay=1
respawn_max=0

depend() {
	after localmount
}
`

// sysvService starts the agent from rc3.d. The shell wrapper waits briefly for
// /dev/vsock so a slow device probe cannot make the first start a no-op.
const sysvService = `#!/bin/sh
### BEGIN INIT INFO
# Provides:          sc-agent
# Required-Start:    $local_fs
# Required-Stop:     $local_fs
# Default-Start:     2 3
# Default-Stop:      0 1 6
# Short-Description: SwarmCracker guest agent
### END INIT INFO

NAME=sc-agent
DAEMON=/sbin/sc-agent
PIDFILE=/run/sc-agent.pid

case "$1" in
	start)
		echo "Starting $NAME"
		i=0
		while [ ! -e /dev/vsock ] && [ "$i" -lt 50 ]; do sleep 0.1; i=$((i + 1)); done
		"$DAEMON" -port 52 &
		echo $! > "$PIDFILE"
		;;
	stop)
		echo "Stopping $NAME"
		[ -f "$PIDFILE" ] && kill "$(cat "$PIDFILE")" 2>/dev/null
		rm -f "$PIDFILE"
		;;
	restart|force-reload)
		"$0" stop
		"$0" start
		;;
	status)
		if [ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
			echo "$NAME is running"
		else
			echo "$NAME is not running"
			exit 3
		fi
		;;
	*)
		echo "Usage: $0 {start|stop|restart|status}" >&2
		exit 1
		;;
esac
`
