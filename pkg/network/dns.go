package network

import (
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/rs/zerolog/log"
)

// dnsmasqArgvForPID returns the argv of a running process, or nil when it
// cannot be read. It is the shared /proc reader for the dnsmasq lifecycle
// helpers — including the check that a PID taken from a pid file really is a
// dnsmasq bound to our bridge — and a variable so a test can describe a process
// without having to spawn one that looks like dnsmasq.
var dnsmasqArgvForPID = func(pid string) []string {
	data, err := os.ReadFile(filepath.Join("/proc", pid, "cmdline"))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
}

// DNSRecord is the desired name-resolution state for one service: the name it
// answers to and the guest addresses to answer with. Records are derived on a
// manager from the cluster state and programmed on every node, so a guest can
// resolve a service whose replicas run anywhere in the cluster.
//
// The addresses are replica IPs rather than the service VIP: the VIP is
// allocated for ingress services but is not a routable address on this
// datapath, so answering with the replicas is the honest equivalent of Docker's
// `dnsrr` endpoint mode.
type DNSRecord struct {
	// Name is the bare service name (e.g. "web").
	Name string
	// IPs are the running replicas' guest addresses on the shared L2 overlay.
	IPs []string
}

// dnsmasqRuntimeDirDefault is where the per-bridge dnsmasq files live. It is a
// root-owned tmpfs path rather than a world-writable one: at predictable names in
// /tmp, any local user could plant a symlink for the daemon to write through as
// root, or a pid file for the daemon to signal as root.
var dnsmasqRuntimeDirDefault = "/run/swarmcracker/dnsmasq"

// dnsmasqRuntimeDir is the directory in use, defaulting to
// dnsmasqRuntimeDirDefault.
func (nm *NetworkManager) dnsmasqRuntimeDir() string {
	if nm.runtimeDir != "" {
		return nm.runtimeDir
	}
	return dnsmasqRuntimeDirDefault
}

// dnsmasqFileFor is the path of one per-bridge dnsmasq file for a named bridge.
// Every path is derived from here, so the directory is never spelled twice.
func (nm *NetworkManager) dnsmasqFileFor(bridgeName, ext string) string {
	return filepath.Join(nm.dnsmasqRuntimeDir(), "dnsmasq-"+bridgeName+"."+ext)
}

// dnsmasqFile is dnsmasqFileFor for this manager's bridge.
func (nm *NetworkManager) dnsmasqFile(ext string) string {
	return nm.dnsmasqFileFor(nm.config.BridgeName, ext)
}

// dnsRecordsPath is the addn-hosts file dnsmasq re-reads on SIGHUP.
func (nm *NetworkManager) dnsRecordsPath() string { return nm.dnsmasqFile("hosts") }

// dnsmasqPIDPath is the pid file dnsmasq writes for the daemon to read,
// dnsmasqLogPath the log it is pointed at, and dnsmasqLeasePath the DHCP lease
// file (dnsmasq's compiled-in default, /var/lib/misc/dnsmasq.leases, is
// read-only under the hardened unit).
func (nm *NetworkManager) dnsmasqPIDPath() string   { return nm.dnsmasqFile("pid") }
func (nm *NetworkManager) dnsmasqLogPath() string   { return nm.dnsmasqFile("log") }
func (nm *NetworkManager) dnsmasqLeasePath() string { return nm.dnsmasqFile("leases") }

// dnsmasqRunAsGroup returns the gid dnsmasq drops to. dnsmasq's compiled default
// user is "nobody", and it needs the runtime directory's group so it can create
// and rewrite its own pid and lease files after the drop. It is a variable so a
// test can simulate a system where that user is missing.
var dnsmasqRunAsGroup = func() (int, error) {
	u, err := user.Lookup("nobody")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(u.Gid)
}

// ensureDnsmasqRuntimeDir creates the runtime directory and hands it to the group
// dnsmasq drops to, so dnsmasq can write its pid and lease files there while no
// other local user can create, replace or list anything in it.
//
// A symlink or a non-directory at the path is refused rather than used: this
// directory holds files the daemon writes as root and PIDs it signals as root.
func (nm *NetworkManager) ensureDnsmasqRuntimeDir() error {
	dir := nm.dnsmasqRuntimeDir()

	if info, err := os.Lstat(dir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("dnsmasq runtime directory %s is a symlink", dir)
		}
		if !info.IsDir() {
			return fmt.Errorf("dnsmasq runtime directory %s is not a directory", dir)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("cannot stat dnsmasq runtime directory %s: %w", dir, err)
	}

	// MkdirAll's mode is masked by the umask, so the mode is set explicitly below.
	if err := os.MkdirAll(dir, 0o770); err != nil {
		return fmt.Errorf("cannot create dnsmasq runtime directory %s: %w", dir, err)
	}

	gid, err := dnsmasqRunAsGroup()
	if err == nil {
		err = os.Chown(dir, 0, gid)
	}
	if err != nil {
		// Without the handover (a system with no "nobody", a non-root caller)
		// fall back to a directory that is searchable but not writable: dnsmasq
		// still serves, only rewriting its lease file degrades.
		log.Warn().Err(err).Str("dir", dir).
			Msg("Could not hand the dnsmasq runtime directory to the user dnsmasq drops to")
		return os.Chmod(dir, 0o755)
	}
	return os.Chmod(dir, 0o770)
}

// writeDnsmasqFile writes one of the daemon-owned dnsmasq files without following
// a symlink at its final path, so a link planted at a predictable name can never
// redirect a root write. It is a variable so a test can drive a failing write.
var writeDnsmasqFile = func(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, mode)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.Write(data)
	return err
}

// ProgramDNS reconciles this node's service-name records with the desired set.
// It is idempotent: when the rendered records are unchanged since the last call
// it does nothing, so callers can invoke it on a timer without rewriting the
// file or reloading dnsmasq.
//
// dnsmasq answers with every address found for a name in an addn-hosts file, so
// a name with several replicas becomes several A records (DNS round-robin).
// Names it does not know are forwarded to the host's upstream resolver, so
// external names keep working.
func (nm *NetworkManager) ProgramDNS(records []DNSRecord) error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	path := nm.dnsRecordsPath()
	content := renderDNSRecords(records)
	if nm.dnsProgrammed && content == nm.dnsRecords {
		// setupDHCP truncates the hosts file before starting a new dnsmasq, so
		// the cache alone is not proof that the file still holds the records:
		// confirm it before skipping the write, otherwise a replaced dnsmasq
		// would serve nothing until the record set happened to change.
		if data, err := os.ReadFile(path); err == nil && string(data) == content {
			return nil
		}
	}

	if err := writeDnsmasqFile(path, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write dns records %s: %w", path, err)
	}

	nm.dnsRecords = content
	nm.dnsProgrammed = true

	// dnsmasq clears its cache and re-reads its hosts files on SIGHUP. Reload
	// every instance serving this bridge, including one that outlived an
	// earlier daemon run.
	pids := findDnsmasqPIDs(nm.config.BridgeName)
	if len(pids) == 0 {
		// No live server: the records are on disk and setupDHCP starts dnsmasq
		// with --addn-hosts on the next PrepareNetwork.
		log.Debug().Msg("DNS records written; no live dnsmasq to reload")
		return nil
	}
	for _, pid := range pids {
		if err := execCommand("kill", "-HUP", pid).Run(); err != nil {
			log.Warn().Str("pid", pid).Err(err).Msg("Failed to reload dnsmasq after DNS record change")
		}
	}
	log.Debug().
		Str("bridge", nm.config.BridgeName).
		Int("records", len(records)).
		Msg("Programmed service DNS records")
	return nil
}

// renderDNSRecords renders records as an /etc/hosts-format file, one line per
// address. The output is fully sorted and deduplicated so an unchanged desired
// set always renders identically and the reconcile stays a no-op.
func renderDNSRecords(records []DNSRecord) string {
	lines := make([]string, 0, len(records))
	for _, r := range records {
		name := strings.TrimSpace(r.Name)
		// Never let a name inject a second hosts entry (or a dnsmasq
		// directive): only plain DNS labels are accepted.
		if !validDNSName(name) {
			continue
		}
		for _, ip := range dedupeSortedStrings(r.IPs) {
			if net.ParseIP(ip) == nil {
				continue
			}
			lines = append(lines, ip+"\t"+name)
		}
	}
	sort.Strings(lines)
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// validDNSName reports whether name is safe to put in a hosts file: letters,
// digits, dots and hyphens, and nothing else. Service names come from the
// control plane, but a hosts file is a format, not a string field.
func validDNSName(name string) bool {
	if name == "" || len(name) > 253 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '-' || c == '_':
		default:
			return false
		}
	}
	return true
}

// dnsmasqServing reports whether a dnsmasq instance is currently bound to this
// manager's bridge, i.e. whether the bridge gateway is a working resolver. It
// scans /proc rather than reading the pid file because the pid file is only
// visible to the process that started the server: a restarted daemon may not
// share the same /tmp, so the pid file can be absent while the server is alive.
func (nm *NetworkManager) dnsmasqServing() bool {
	return len(findDnsmasqPIDs(nm.config.BridgeName)) > 0
}

// bridgeGatewayIP returns the bridge's gateway address without any CIDR suffix,
// or "" when the configured value is unusable.
func (nm *NetworkManager) bridgeGatewayIP() string {
	gw := strings.TrimSpace(nm.config.BridgeIP)
	if gw == "" {
		return ""
	}
	if ip, _, err := net.ParseCIDR(gw); err == nil {
		return ip.String()
	}
	if ip := net.ParseIP(gw); ip != nil {
		return ip.String()
	}
	return ""
}

// dnsmasqRecordsCapable reports whether the process with this PID was started
// with --addn-hosts, i.e. it can serve service records at all. It is a variable
// so a test can drive the reuse decision without a real dnsmasq process.
var dnsmasqRecordsCapable = func(pid string) bool {
	for _, a := range dnsmasqArgvForPID(pid) {
		if strings.HasPrefix(a, "--addn-hosts") {
			return true
		}
	}
	return false
}

// dnsmasqServesRecords reports whether any of the given dnsmasq processes can
// serve service records. An instance started before this feature cannot, so
// setupDHCP replaces it rather than reusing it and silently serving nothing.
func dnsmasqServesRecords(pids []string) bool {
	for _, pid := range pids {
		if dnsmasqRecordsCapable(pid) {
			return true
		}
	}
	return false
}
