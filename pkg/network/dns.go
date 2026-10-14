package network

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rs/zerolog/log"
)

// dnsmasqArgvForPID returns the argv of a running process, or nil when it
// cannot be read. It is the shared /proc reader for the dnsmasq lifecycle
// helpers.
func dnsmasqArgvForPID(pid string) []string {
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

// dnsRecordsPath is the addn-hosts file dnsmasq re-reads on SIGHUP. It sits
// next to the other per-bridge dnsmasq files (pid, log, leases) in this
// process's private /tmp, which dnsmasq shares because the daemon starts it.
func (nm *NetworkManager) dnsRecordsPath() string {
	return filepath.Join("/tmp", "dnsmasq-"+nm.config.BridgeName+".hosts")
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

	content := renderDNSRecords(records)
	if nm.dnsProgrammed && content == nm.dnsRecords {
		return nil
	}

	path := nm.dnsRecordsPath()
	if err := osWriteFile(path, []byte(content), 0644); err != nil {
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
// is a /proc scan rather than a pid-file read because a daemon restart gives
// this process a fresh private /tmp, hiding the pid file of a server started by
// the previous run.
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
