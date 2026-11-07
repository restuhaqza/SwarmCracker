//go:build linux

package agent

// The Linux half of `prepare`: the guest's own interface, files and clock.
//
// Everything here runs inside the guest, as root, because nothing outside it can
// do this work. Firecracker refuses to rewrite an interface's MAC, its drives or
// its metadata *config* once an image is loaded, so a clone's identity has to be
// applied by the guest that is wearing it (RFC §5.4, D10).
//
// The applier works by difference: every part it is asked for is compared with
// what the guest already has, and only a real difference is applied and
// reported. That is what makes the operation idempotent — a hook that runs again
// with the same identity rewrites nothing and says so.

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unsafe"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	// resolvConfPath is the resolver configuration the guest uses.
	resolvConfPath = "/etc/resolv.conf"
	// machineIDPath is systemd's machine identity; dbusMachineIDPath is the copy
	// the D-Bus library keeps, which older images still read.
	machineIDPath     = "/etc/machine-id"
	dbusMachineIDPath = "/var/lib/dbus/machine-id"
	// hostnamePath is the persistent host name; unix.Sethostname sets the one the
	// running kernel reports.
	hostnamePath = "/etc/hostname"
	// hostKeyDir is where OpenSSH keeps the keys that name a host.
	hostKeyDir = "/etc/ssh"
	// preparedPath records the identity generation this guest has already
	// applied, so the parts that can only be minted once per machine — its
	// machine-id, its SSH host keys, its fresh entropy — are not minted again on
	// every resume.
	preparedPath = "/var/lib/swarmcracker/prepared"
	// sshKeygenCommand generates the host keys a guest is missing.
	sshKeygenCommand = "ssh-keygen"

	// identityBootstrapAddress is the address `prepare` gives an interface that
	// has none, so the metadata service is on-link for the one read it needs. The
	// identity replaces it.
	identityBootstrapAddress = "169.254.1.1/16"

	// machineIDBytes is the entropy of a machine id: 16 bytes, hex encoded into
	// the 32 characters the file holds.
	machineIDBytes = 16
	// clockTolerance is how far the guest's wall clock may be ahead of the instant
	// the node reports before `prepare` leaves it alone. The clock is only ever
	// moved forward: a resumed guest is behind, and moving a clock backwards
	// breaks every timer and every certificate a workload holds.
	clockTolerance = 2 * time.Second
)

// reachMetadataServiceOS brings iface up and gives the guest a way to the
// metadata service, before anything is known about the identity.
func reachMetadataServiceOS(iface string) error {
	link, err := netlink.LinkByName(iface)
	if err != nil {
		return fmt.Errorf("interface %s: %w", iface, err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("bring %s up: %w", iface, err)
	}
	if err := ensureMetadataRoute(link); err != nil {
		return err
	}

	// The service answers an address it is routed to, and its reply needs a
	// source the guest will accept. An interface that carries no address at all —
	// a template whose own init configures nothing — gets a link-local one for
	// the length of the read; the identity replaces it.
	addrs, err := ipv4Addresses(link)
	if err != nil {
		return err
	}
	if len(addrs) == 0 {
		bootstrap, err := netlink.ParseAddr(identityBootstrapAddress)
		if err != nil {
			return fmt.Errorf("parse the bootstrap address %s: %w", identityBootstrapAddress, err)
		}
		bootstrap.Scope = int(netlink.SCOPE_LINK)
		if err := netlink.AddrReplace(link, bootstrap); err != nil {
			return fmt.Errorf("give %s the bootstrap address %s: %w", iface, identityBootstrapAddress, err)
		}
	}
	return nil
}

// applyIdentityOS makes the guest carry id, and reports what it carries
// afterwards. Every value in the report is read back from the guest, so the node
// can hold the guest to the identity it published instead of trusting that the
// request was carried out.
func applyIdentityOS(ctx context.Context, id Identity) (PrepareReport, error) {
	link, err := netlink.LinkByName(id.Interface)
	if err != nil {
		return PrepareReport{}, fmt.Errorf("interface %s: %w", id.Interface, err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return PrepareReport{}, fmt.Errorf("bring %s up: %w", id.Interface, err)
	}

	changed := newChangeSet()

	if err := applyMAC(link, id, changed); err != nil {
		return PrepareReport{}, err
	}
	if err := applyAddresses(link, id, changed); err != nil {
		return PrepareReport{}, err
	}
	if err := applyRoutes(link, id, changed); err != nil {
		return PrepareReport{}, err
	}
	// The address change above can take the metadata route with it — the kernel
	// removes a route whose source address is gone — so it is restored here. A
	// guest that could not read its identity a second time would be a guest that
	// cannot be prepared twice.
	if err := ensureMetadataRoute(link); err != nil {
		return PrepareReport{}, err
	}

	if err := applyDNS(id, changed); err != nil {
		return PrepareReport{}, err
	}
	if err := applyHostname(id, changed); err != nil {
		return PrepareReport{}, err
	}
	if err := applyMachineIdentity(ctx, id, changed); err != nil {
		return PrepareReport{}, err
	}
	if err := applyToken(id, changed); err != nil {
		return PrepareReport{}, err
	}
	if err := applyClock(id, changed); err != nil {
		return PrepareReport{}, err
	}

	report, err := observeIdentity(link)
	if err != nil {
		return PrepareReport{}, err
	}
	report.Changed = changed.sorted()
	report.TokenRotated = changed.tokenRotated
	report.HostKeys = changed.hostKeys
	report.EntropyBits = changed.entropyBits
	return report, nil
}

// changeSet collects the parts a run changed, without repeating one, together
// with the two values a caller reads straight from it.
type changeSet struct {
	parts map[string]bool

	tokenRotated bool
	hostKeys     []string
	entropyBits  int
}

func newChangeSet() *changeSet { return &changeSet{parts: map[string]bool{}} }

func (c *changeSet) mark(part string) { c.parts[part] = true }

// sorted returns the changed parts in a stable order.
func (c *changeSet) sorted() []string {
	parts := make([]string, 0, len(c.parts))
	for part := range c.parts {
		parts = append(parts, part)
	}
	sort.Strings(parts)
	return parts
}

// applyMAC gives the interface the identity's link address. Two clones of one
// checkpoint that kept the same MAC would be one station on the wire.
func applyMAC(link netlink.Link, id Identity, changed *changeSet) error {
	if id.MAC == "" {
		return nil
	}
	want, err := net.ParseMAC(id.MAC)
	if err != nil {
		return fmt.Errorf("identity mac %q: %w", id.MAC, err)
	}
	if bytes.Equal(link.Attrs().HardwareAddr, want) {
		return nil
	}
	if err := netlink.LinkSetHardwareAddr(link, want); err != nil {
		return fmt.Errorf("give %s the mac %s: %w", id.Interface, id.MAC, err)
	}
	changed.mark(PartMAC)
	return nil
}

// applyAddresses leaves the interface carrying the identity's address and nothing
// else. The addresses the checkpoint froze belong to the machine the checkpoint
// was taken from: leaving one in place is how two clones end up answering for one
// address.
func applyAddresses(link netlink.Link, id Identity, changed *changeSet) error {
	want, err := parseOptionalAddress(id.Address)
	if err != nil {
		return err
	}

	have, err := ipv4Addresses(link)
	if err != nil {
		return err
	}
	for _, addr := range have {
		if want != nil && sameAddress(addr, *want) {
			continue
		}
		if err := netlink.AddrDel(link, &addr); err != nil {
			return fmt.Errorf("remove %s from %s: %w", addr.IPNet, id.Interface, err)
		}
		changed.mark(PartAddress)
	}
	if want == nil {
		return nil
	}

	if err := netlink.AddrReplace(link, want); err != nil {
		return fmt.Errorf("give %s the address %s: %w", id.Interface, want.IPNet, err)
	}
	if !anyAddress(have, *want) {
		changed.mark(PartAddress)
	}
	return nil
}

// applyRoutes gives the guest the identity's default route and extra routes, and
// takes away the default route the checkpoint carried. A clone that kept its
// source's gateway would send its traffic to the source's network.
func applyRoutes(link netlink.Link, id Identity, changed *changeSet) error {
	index := link.Attrs().Index
	have, err := ipv4Routes(link)
	if err != nil {
		return err
	}

	if id.Gateway != "" {
		gw := net.ParseIP(id.Gateway).To4()
		if gw == nil {
			return fmt.Errorf("identity gateway %q is not an IPv4 address", id.Gateway)
		}
		want := &netlink.Route{LinkIndex: index, Gw: gw}
		if !anyRoute(have, want) {
			changed.mark(PartRoutes)
		}
		// Every other default route goes: the kernel would otherwise pick between
		// them by metric, and half the clone's traffic would leave through the
		// checkpoint's network.
		for _, route := range have {
			if !isDefaultRoute(route) || route.Gw.Equal(gw) {
				continue
			}
			if err := netlink.RouteDel(&route); err != nil {
				return fmt.Errorf("remove the default route via %s: %w", route.Gw, err)
			}
		}
		if err := netlink.RouteReplace(want); err != nil {
			return fmt.Errorf("set the default route via %s: %w", id.Gateway, err)
		}
	}

	for _, route := range id.Routes {
		_, dst, err := net.ParseCIDR(route.Destination)
		if err != nil {
			return fmt.Errorf("identity route %q: %w", route.Destination, err)
		}
		next := &netlink.Route{LinkIndex: index, Dst: dst}
		if route.Gateway != "" {
			gw := net.ParseIP(route.Gateway).To4()
			if gw == nil {
				return fmt.Errorf("identity route %q gateway %q is not an IPv4 address", route.Destination, route.Gateway)
			}
			next.Gw = gw
		}
		if !anyRoute(have, next) {
			changed.mark(PartRoutes)
		}
		if err := netlink.RouteReplace(next); err != nil {
			return fmt.Errorf("apply the route %s: %w", route.Destination, err)
		}
	}
	return nil
}

// applyDNS writes the identity's nameservers into the resolver configuration,
// keeping whatever else the template put there: the search list and the resolver
// options are the template's, not the clone's.
func applyDNS(id Identity, changed *changeSet) error {
	if len(id.DNS) == 0 {
		return nil
	}

	body, err := resolvConf(id.DNS)
	if err != nil {
		return err
	}
	current, err := os.ReadFile(resolvConfPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", resolvConfPath, err)
	}
	if string(current) == body {
		return nil
	}
	if err := writeFileAtomic(resolvConfPath, []byte(body), 0o644); err != nil {
		return err
	}
	changed.mark(PartDNS)
	return nil
}

// resolvConf builds the resolver configuration for nameservers, preserving the
// non-nameserver lines of what is there now.
func resolvConf(nameservers []string) (string, error) {
	var kept []string
	if current, err := os.ReadFile(resolvConfPath); err == nil {
		for _, line := range strings.Split(string(current), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "nameserver") ||
				strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
				continue
			}
			kept = append(kept, trimmed)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read %s: %w", resolvConfPath, err)
	}

	var b strings.Builder
	for _, server := range nameservers {
		if net.ParseIP(server) == nil {
			return "", fmt.Errorf("identity nameserver %q is not an address", server)
		}
		fmt.Fprintf(&b, "nameserver %s\n", server)
	}
	for _, line := range kept {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// applyHostname sets the guest's host name, and keeps /etc/hostname in step so a
// workload that reads the file sees the name the kernel reports.
func applyHostname(id Identity, changed *changeSet) error {
	if id.Hostname == "" {
		return nil
	}
	current, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("read the host name: %w", err)
	}
	if current == id.Hostname {
		return nil
	}
	if err := unix.Sethostname([]byte(id.Hostname)); err != nil {
		return fmt.Errorf("set the host name to %q: %w", id.Hostname, err)
	}
	if err := writeFileAtomic(hostnamePath, []byte(id.Hostname+"\n"), 0o644); err != nil {
		return err
	}
	changed.mark(PartHostname)
	return nil
}

// applyMachineIdentity gives the guest the machine identity of the clone it now
// is: the machine id, the SSH host keys that name it, and entropy to draw from.
//
// These are the parts that can only be minted once per machine, so they are tied
// to the identity's generation: a guest resumed with the identity it already has
// keeps them, and a guest resumed as a different machine — which is every clone
// of a checkpoint — gets its own. Without that, a hook that runs on every resume
// would hand the same machine a new machine id and new host keys every time.
func applyMachineIdentity(ctx context.Context, id Identity, changed *changeSet) error {
	fresh := id.GenerationID()
	previous, err := readPreparedGeneration()
	if err != nil {
		return err
	}
	if previous == fresh && !id.HostKeys {
		return nil
	}

	if previous != fresh {
		if err := writeMachineID(id.MachineID); err != nil {
			return err
		}
		changed.mark(PartMachineID)
	}
	if err := regenerateHostKeys(ctx, id, changed); err != nil {
		return err
	}
	if previous != fresh && id.Entropy != "" {
		entropy, err := decodeEntropy(id.Entropy)
		if err != nil {
			return err
		}
		if err := addEntropyOS(entropy); err != nil {
			return err
		}
		changed.entropyBits = len(entropy) * 8
		changed.mark(PartEntropy)
	}
	if previous == fresh {
		return nil
	}
	return writePreparedGeneration(fresh)
}

// writeMachineID installs value as the machine id, or mints one when the identity
// names none: every clone must carry one of its own, different from the machine
// the checkpoint came from.
func writeMachineID(value string) error {
	if value == "" {
		minted, err := newMachineID()
		if err != nil {
			return err
		}
		value = minted
	}
	body := value + "\n"
	for _, path := range []string{machineIDPath, dbusMachineIDPath} {
		// The D-Bus copy only exists on images that carry the library; creating it
		// where nothing reads it would be a claim about the guest that is not true.
		if path == dbusMachineIDPath {
			if _, err := os.Stat(filepath.Dir(path)); err != nil {
				continue
			}
		}
		current, err := os.ReadFile(path)
		if err == nil && string(current) == body {
			continue
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read the machine id %s: %w", path, err)
		}
		if err := writeFileAtomic(path, []byte(body), 0o444); err != nil {
			return err
		}
	}
	return nil
}

// newMachineID mints a machine id: 16 random bytes, hex encoded, which is the 32
// characters systemd's file holds.
func newMachineID() (string, error) {
	var b [machineIDBytes]byte
	if _, err := randomRead(b[:]); err != nil {
		return "", fmt.Errorf("mint a machine id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// regenerateHostKeys replaces the SSH host keys a checkpoint carried with keys of
// this clone's own. A client that pins a host key would otherwise accept the
// clone as the machine the checkpoint was taken from.
//
// A guest with no host keys at all is a guest without OpenSSH: there is nothing
// to regenerate, which is only a failure when the identity asked for it.
func regenerateHostKeys(ctx context.Context, id Identity, changed *changeSet) error {
	existing, err := filepath.Glob(filepath.Join(hostKeyDir, "ssh_host_*"))
	if err != nil {
		return fmt.Errorf("list the SSH host keys: %w", err)
	}
	keygen, lookErr := exec.LookPath(sshKeygenCommand)
	if len(existing) == 0 {
		if id.HostKeys {
			return fmt.Errorf("the identity asks for new SSH host keys, but this guest has none and no %s to make them", sshKeygenCommand)
		}
		return nil
	}
	if lookErr != nil {
		return fmt.Errorf("regenerate the SSH host keys: %w", lookErr)
	}

	for _, path := range existing {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove the SSH host key %s: %w", path, err)
		}
	}
	if out, err := exec.CommandContext(ctx, keygen, "-A").CombinedOutput(); err != nil {
		return fmt.Errorf("generate SSH host keys with %s -A: %w: %s", sshKeygenCommand, err, strings.TrimSpace(string(out)))
	}

	types, err := hostKeyTypes()
	if err != nil {
		return err
	}
	changed.mark(PartHostKeys)
	changed.hostKeys = types
	return nil
}

// hostKeyTypes lists the host key types the guest now has.
func hostKeyTypes() ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(hostKeyDir, "ssh_host_*_key"))
	if err != nil {
		return nil, fmt.Errorf("list the SSH host keys: %w", err)
	}
	types := make([]string, 0, len(paths))
	for _, path := range paths {
		name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "ssh_host_"), "_key")
		types = append(types, name)
	}
	sort.Strings(types)
	return types, nil
}

// applyToken installs the per-boot token the node minted for this clone.
//
// The token frozen in a checkpoint's disk is the token of the machine the
// checkpoint came from: every clone that kept it would authenticate to its own
// root agent with the same credential. The guest agent re-reads this file on
// every handshake, so a rotation takes effect on the next connection.
func applyToken(id Identity, changed *changeSet) error {
	if id.Token == "" {
		return nil
	}
	if err := validateToken(id.Token); err != nil {
		return err
	}
	body := []byte(id.Token + "\n")
	if current, err := os.ReadFile(TokenPath); err == nil && bytes.Equal(current, body) {
		return nil
	}
	if err := writeFileAtomic(TokenPath, body, 0o400); err != nil {
		return err
	}
	changed.mark(PartToken)
	changed.tokenRotated = true
	return nil
}

// applyClock moves the guest's wall clock forward to the instant the node
// reported. It never moves it backwards: a resumed guest is behind, while a clock
// that jumps back breaks every timer and every certificate a workload holds. A
// guest whose clock is already ahead — or behind by less than the tolerance — is
// left alone, which is what makes a repeated prepare a no-op.
func applyClock(id Identity, changed *changeSet) error {
	if id.ClockUnixNano == 0 {
		return nil
	}
	want := time.Unix(0, id.ClockUnixNano)
	if !clockNeedsSetting(time.Now(), want) {
		return nil
	}
	if err := setClockOS(id.ClockUnixNano); err != nil {
		return err
	}
	changed.mark(PartClock)
	return nil
}

// clockNeedsSetting reports whether the guest's clock has to be moved to want: only a
// guest that is behind by more than the tolerance is moved, so a clock that is ahead
// is never pulled back and a second prepare of the same identity changes nothing.
func clockNeedsSetting(now, want time.Time) bool {
	return want.After(now.Add(clockTolerance))
}

// setClockOS sets the guest's wall clock.
func setClockOS(unixNano int64) error {
	ts := unix.NsecToTimespec(unixNano)
	if err := unix.ClockSettime(unix.CLOCK_REALTIME, &ts); err != nil {
		return fmt.Errorf("set the wall clock: %w", err)
	}
	return nil
}

// addEntropyOS mixes fresh bytes into the kernel's entropy pool. A restored
// guest's pool is a copy of its source's, so without this every clone draws the
// same "random" stream.
func addEntropyOS(entropy []byte) error {
	f, err := os.OpenFile("/dev/urandom", os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open /dev/urandom: %w", err)
	}
	defer func() { _ = f.Close() }()

	// RNDADDENTROPY takes a struct rand_pool_info: two native ints — the bits the
	// caller credits the pool, and the buffer's size — followed by the buffer
	// itself. The kernel credits at most 8 bits per byte.
	info := make([]byte, 8+len(entropy))
	//nolint:gosec // G115: len(entropy) is bounded by maxEntropyBytes.
	binary.LittleEndian.PutUint32(info[0:], uint32(len(entropy)*8))
	//nolint:gosec // G115: as above.
	binary.LittleEndian.PutUint32(info[4:], uint32(len(entropy)))
	copy(info[8:], entropy)

	buf := unsafe.Pointer(&info[0]) //nolint:gosec // G103: the ioctl's argument is a pointer to this buffer by definition.
	//nolint:gosec // G103: as above.
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), uintptr(unix.RNDADDENTROPY), uintptr(buf))
	if errno != 0 {
		return fmt.Errorf("mix %d bytes into the entropy pool: %w", len(entropy), errno)
	}
	return nil
}

// observeIdentity reads back what the guest now carries.
func observeIdentity(link netlink.Link) (PrepareReport, error) {
	report := PrepareReport{Interface: link.Attrs().Name}

	// The link is re-read: the MAC was set through the same handle, but the report
	// has to be the interface's own answer, not the value this run wrote.
	current, err := netlink.LinkByName(link.Attrs().Name)
	if err != nil {
		return PrepareReport{}, fmt.Errorf("read back %s: %w", link.Attrs().Name, err)
	}
	if hw := current.Attrs().HardwareAddr; hw != nil {
		report.MAC = hw.String()
	}

	addrs, err := ipv4Addresses(current)
	if err != nil {
		return PrepareReport{}, err
	}
	for _, addr := range addrs {
		report.Addresses = append(report.Addresses, addr.IPNet.String())
	}

	routes, err := ipv4Routes(current)
	if err != nil {
		return PrepareReport{}, err
	}
	for _, route := range routes {
		report.Routes = append(report.Routes, formatRoute(route))
	}

	hostname, err := os.Hostname()
	if err != nil {
		return PrepareReport{}, fmt.Errorf("read the host name: %w", err)
	}
	report.Hostname = hostname
	if report.MachineID, err = readMachineID(); err != nil {
		return PrepareReport{}, err
	}
	if report.DNS, err = readNameservers(); err != nil {
		return PrepareReport{}, err
	}
	report.ClockUnixNano = time.Now().UnixNano()
	return report, nil
}

// readMachineID reads the machine id the guest carries, or reports an empty one
// when it has none.
func readMachineID() (string, error) {
	body, err := os.ReadFile(machineIDPath)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read the machine id: %w", err)
	}
	return strings.TrimSpace(string(body)), nil
}

// readNameservers reads the nameservers the resolver configuration holds.
func readNameservers() ([]string, error) {
	body, err := os.ReadFile(resolvConfPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", resolvConfPath, err)
	}
	var servers []string
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "nameserver" {
			servers = append(servers, fields[1])
		}
	}
	return servers, nil
}

// readPreparedGeneration reads the identity generation this guest has applied, if
// any.
func readPreparedGeneration() (string, error) {
	body, err := os.ReadFile(preparedPath)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", preparedPath, err)
	}
	return strings.TrimSpace(string(body)), nil
}

// writePreparedGeneration records the identity generation this guest carries.
func writePreparedGeneration(generation string) error {
	if err := os.MkdirAll(filepath.Dir(preparedPath), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(preparedPath), err)
	}
	return writeFileAtomic(preparedPath, []byte(generation+"\n"), 0o644)
}

// ipv4Addresses lists the interface's IPv4 addresses.
func ipv4Addresses(link netlink.Link) ([]netlink.Addr, error) {
	addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return nil, fmt.Errorf("list the addresses of %s: %w", link.Attrs().Name, err)
	}
	return addrs, nil
}

// ipv4Routes lists the interface's IPv4 routes.
func ipv4Routes(link netlink.Link) ([]netlink.Route, error) {
	routes, err := netlink.RouteList(link, netlink.FAMILY_V4)
	if err != nil {
		return nil, fmt.Errorf("list the routes of %s: %w", link.Attrs().Name, err)
	}
	return routes, nil
}

// ensureMetadataRoute makes the metadata service reachable through link.
func ensureMetadataRoute(link netlink.Link) error {
	route := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Dst:       &net.IPNet{IP: net.ParseIP(MMDSAddress).To4(), Mask: net.CIDRMask(32, 32)},
		Scope:     netlink.SCOPE_LINK,
	}
	if err := netlink.RouteReplace(route); err != nil {
		return fmt.Errorf("route %s to the metadata service through %s: %w", MMDSAddress, link.Attrs().Name, err)
	}
	return nil
}

// parseOptionalAddress parses an address in CIDR form. An empty value yields a
// nil address, which a caller reads as "the identity says nothing about it".
func parseOptionalAddress(address string) (*netlink.Addr, error) {
	if address == "" {
		return nil, nil
	}
	addr, err := netlink.ParseAddr(address)
	if err != nil {
		return nil, fmt.Errorf("identity address %q: %w", address, err)
	}
	return addr, nil
}

// sameAddress reports whether two addresses are the same address on the same
// network.
func sameAddress(a, b netlink.Addr) bool {
	return a.IPNet != nil && b.IPNet != nil && a.IPNet.String() == b.IPNet.String()
}

// anyAddress reports whether addrs holds addr.
func anyAddress(addrs []netlink.Addr, addr netlink.Addr) bool {
	for _, have := range addrs {
		if sameAddress(have, addr) {
			return true
		}
	}
	return false
}

// isDefaultRoute reports whether route is a default route.
func isDefaultRoute(route netlink.Route) bool {
	if route.Dst == nil {
		return true
	}
	ones, bits := route.Dst.Mask.Size()
	return bits == 32 && ones == 0
}

// anyRoute reports whether routes already holds the route want describes.
//
// The kernel reports the default route with a 0.0.0.0/0 destination while a caller
// asks for it with a nil one, and both mean the same route: comparing them as written
// would make every prepare "change" the default route it had already installed, which
// is exactly what idempotence must not do.
func anyRoute(routes []netlink.Route, want *netlink.Route) bool {
	for _, have := range routes {
		if want.Dst == nil {
			if isDefaultRoute(have) && have.Gw.Equal(want.Gw) {
				return true
			}
			continue
		}
		if !sameDestination(have.Dst, want.Dst) {
			continue
		}
		if have.Gw.Equal(want.Gw) {
			return true
		}
	}
	return false
}

// sameDestination compares two route destinations, where nil means the default
// route.
func sameDestination(a, b *net.IPNet) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.String() == b.String()
}

// formatRoute renders a route the way an operator expects to read it.
func formatRoute(route netlink.Route) string {
	destination := "default"
	if route.Dst != nil {
		destination = route.Dst.String()
	}

	parts := []string{destination}
	if route.Gw != nil {
		parts = append(parts, "via "+route.Gw.String())
	}
	if route.LinkIndex != 0 {
		if link, err := netlink.LinkByIndex(route.LinkIndex); err == nil {
			parts = append(parts, "dev "+link.Attrs().Name)
		}
	}
	if route.Src != nil {
		parts = append(parts, "src "+route.Src.String())
	}
	if route.Scope == netlink.SCOPE_LINK {
		parts = append(parts, "scope link")
	}
	return strings.Join(parts, " ")
}

// writeFileAtomic writes body to path through a temporary file in the same
// directory and renames it, so a reader sees the old content or the new one and
// never a half-written file. The caller names the mode, so a token lands 0400 and
// a resolved.conf 0644.
func writeFileAtomic(path string, body []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".sc-prepare-*")
	if err != nil {
		return fmt.Errorf("stage %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("stage %s: %w", path, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set the mode of %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("stage %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("install %s: %w", path, err)
	}
	return nil
}
