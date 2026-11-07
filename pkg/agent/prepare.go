package agent

// Post-restore preparation: the guest half of the identity channel (SBX-083,
// RFC D10).
//
// A checkpoint freezes everything the guest was: its MAC, its addresses, its
// route table, its resolver, its machine-id, its SSH host keys, its clock and
// the entropy pool it had drawn from. Firecracker 1.15.1 refuses to rewrite any
// of that from the host once the image is loaded — the NIC, the drives and even
// the MMDS *config* answer `400 not supported after starting the microVM` — so
// two clones of one checkpoint would otherwise come up as one machine: same MAC,
// same address, same host keys, same random stream.
//
// The one channel that stays writable is MMDS: the node rewrites the metadata
// document per clone while the loaded guest is still paused. `prepare` is the
// operation that reads that document and applies it, because the guest is the
// only side that can — it owns its interface, its files and its clock.
//
// The operation is idempotent: applying the same identity twice changes nothing
// the second time, and the report says which parts the run actually changed. Every
// deadline in it is measured against the monotonic clock, never the wall clock,
// because the wall clock is exactly what a restore leaves wrong.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	// MMDSAddress is the address of the Firecracker metadata service. It is not
	// routable anywhere else: the VMM intercepts it on the instance's own
	// network interface.
	MMDSAddress = "169.254.169.254"

	// MMDSIdentityPath is where inside MMDS the node publishes this clone's
	// identity, as a JSON document. It is a string leaf, so the guest reads one
	// path and gets the whole document: Firecracker answers an object node with
	// a list of its keys, not with the object.
	MMDSIdentityPath = "/identity"

	// DefaultIdentityInterface is the interface `prepare` configures when the
	// identity names none.
	DefaultIdentityInterface = "eth0"

	// mmdsTokenTTLSeconds is the lifetime the guest asks for its metadata session
	// token. The token is minted and used within one `prepare`, so a short one is
	// enough and cannot outlive the read.
	mmdsTokenTTLSeconds = 60

	// mmdsTimeout bounds one metadata request. It is a monotonic budget: the
	// guest's wall clock is frozen at snapshot time, so only elapsed time can be
	// trusted across a restore.
	mmdsTimeout = 3 * time.Second

	// mmdsReadBackoff is the pause between two attempts at reading the identity.
	mmdsReadBackoff = 250 * time.Millisecond

	// maxIdentityDocument bounds the metadata document a guest will read, so a
	// hostile or corrupt service cannot make the agent allocate without limit.
	maxIdentityDocument = 64 << 10
)

// mmdsBaseURL is the metadata service's root. It is a variable so a test can
// point the client at a local server; production always reads Firecracker's own
// service.
var mmdsBaseURL = "http://" + MMDSAddress

// mmdsHTTPClient performs the metadata requests. Its timeout is a backstop behind
// the request context's.
//
// It deliberately has no proxy: the metadata service is a link-local address the
// VMM answers on, and an HTTP_PROXY inherited from the template's environment would
// send the read — and with it the clone's identity — somewhere else entirely. One
// connection per read, because a `prepare` makes two requests at most.
var mmdsHTTPClient = &http.Client{
	Timeout: mmdsTimeout,
	Transport: &http.Transport{
		Proxy:             nil,
		DisableKeepAlives: true,
		DialContext:       (&net.Dialer{Timeout: mmdsTimeout}).DialContext,
	},
}

// Route is one route an identity installs, besides its default gateway.
type Route struct {
	// Destination is the route's CIDR.
	Destination string `json:"destination"`
	// Gateway is the next hop. Empty means the route is on-link through the
	// interface.
	Gateway string `json:"gateway,omitempty"`
}

// Identity is a clone's own identity: what the node publishes to MMDS before it
// resumes the guest, and what `prepare` makes the guest carry.
//
// Every field is optional, and an empty one means "leave this alone": a template
// that gives its guests a hostname of their own keeps it when the identity says
// nothing about a hostname. The fields that matter for two clones of one
// checkpoint not being one machine are MAC, Address and Token.
type Identity struct {
	// Interface is the interface to configure. Empty means
	// DefaultIdentityInterface.
	Interface string `json:"interface,omitempty"`
	// MAC is the link address the clone carries. Two clones of one checkpoint
	// must not share it: they would be one station on the wire.
	MAC string `json:"mac,omitempty"`
	// Address is the clone's address on the interface, in CIDR form. It replaces
	// the addresses the checkpoint froze, which belong to the machine the
	// checkpoint was taken from.
	Address string `json:"address,omitempty"`
	// Gateway is the next hop of the default route.
	Gateway string `json:"gateway,omitempty"`
	// Routes are extra routes, applied after the default.
	Routes []Route `json:"routes,omitempty"`
	// DNS is the list of nameservers, written to /etc/resolv.conf.
	DNS []string `json:"dns,omitempty"`
	// Hostname is the clone's host name. Empty leaves the checkpoint's.
	Hostname string `json:"hostname,omitempty"`
	// MachineID is the 32-hex machine id (systemd's machine-id, D-Bus's copy).
	// Empty means generate a fresh one; two clones must never share one.
	MachineID string `json:"machineId,omitempty"`
	// Token is the guest agent's new per-boot token. The node mints one per
	// clone, because the token frozen in the checkpoint's disk is the token of
	// the machine the checkpoint came from: every clone that kept it would
	// authenticate with the same credential.
	Token string `json:"token,omitempty"`
	// Entropy is freshly minted bytes, hex encoded, mixed into the kernel's
	// entropy pool. A restored guest's pool is a copy, so without this every
	// clone draws the same "random" stream.
	Entropy string `json:"entropy,omitempty"`
	// ClockUnixNano is the UTC instant the clone should adopt, in Unix
	// nanoseconds. A resumed guest's wall clock is the checkpoint's, and
	// Firecracker gives the guest no RTC, so the node tells it the time.
	ClockUnixNano int64 `json:"clockUnixNano,omitempty"`
	// HostKeys asks for the SSH host keys to be regenerated. They name a host,
	// so clones that share them are indistinguishable to a client that pins
	// them.
	HostKeys bool `json:"hostKeys,omitempty"`
}

// Validate reports whether the identity can be applied at all. It is the guest's
// own check, and it runs before anything is changed, so a malformed document
// cannot leave the guest half-configured. It reports every problem it finds rather
// than stopping at the first, because an operator fixing a published identity wants
// the whole list.
func (i Identity) Validate() error {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	i.validateLink(add)
	i.validateNetwork(add)
	i.validateNames(add)
	i.validateCredentials(add)
	if i.empty() {
		add("the identity carries nothing to apply")
	}

	if len(problems) > 0 {
		return errors.New("identity: " + strings.Join(problems, "; "))
	}
	return nil
}

// validateLink checks the interface and the link address it carries.
func (i Identity) validateLink(add func(string, ...any)) {
	if strings.TrimSpace(i.Interface) != i.Interface || strings.ContainsRune(i.Interface, '/') {
		add("interface %q is not an interface name", i.Interface)
	}
	if i.MAC == "" {
		return
	}
	mac, err := net.ParseMAC(i.MAC)
	if err != nil {
		add("mac %q: %v", i.MAC, err)
		return
	}
	if len(mac) != 6 {
		add("mac %q is not a 48-bit address", i.MAC)
		return
	}
	if mac[0]&1 == 1 {
		add("mac %q is a group address: an interface cannot carry it", i.MAC)
	}
}

// validateNetwork checks the address, the gateway and the extra routes.
func (i Identity) validateNetwork(add func(string, ...any)) {
	if i.Address != "" {
		if _, _, err := net.ParseCIDR(i.Address); err != nil {
			add("address %q: %v", i.Address, err)
		}
	}
	if i.Gateway != "" && net.ParseIP(i.Gateway) == nil {
		add("gateway %q is not an address", i.Gateway)
	}
	for n, route := range i.Routes {
		if _, _, err := net.ParseCIDR(route.Destination); err != nil {
			add("routes[%d] destination %q: %v", n, route.Destination, err)
		}
		if route.Gateway != "" && net.ParseIP(route.Gateway) == nil {
			add("routes[%d] gateway %q is not an address", n, route.Gateway)
		}
	}
}

// validateNames checks the names the guest adopts: its host name, its resolver's
// servers and its machine identity.
func (i Identity) validateNames(add func(string, ...any)) {
	for n, server := range i.DNS {
		if net.ParseIP(server) == nil {
			add("dns[%d] %q is not an address", n, server)
		}
	}
	if i.Hostname != "" && !isHostname(i.Hostname) {
		add("hostname %q is not a host name", i.Hostname)
	}
	if i.MachineID != "" && !isMachineID(i.MachineID) {
		add("machineId %q is not 32 hex characters", i.MachineID)
	}
}

// validateCredentials checks the two secrets the guest adopts: the agent token and
// the entropy it mixes into the kernel's pool.
func (i Identity) validateCredentials(add func(string, ...any)) {
	if i.Token != "" {
		if err := validateToken(i.Token); err != nil {
			add("token: %v", err)
		}
	}
	if i.Entropy != "" {
		if _, err := decodeEntropy(i.Entropy); err != nil {
			add("entropy: %v", err)
		}
	}
}

// empty reports whether the identity asks for nothing at all. An identity is
// allowed to leave parts of the guest alone, but not every part.
func (i Identity) empty() bool {
	return i.MAC == "" && i.Address == "" && i.Gateway == "" && len(i.Routes) == 0 &&
		len(i.DNS) == 0 && i.Hostname == "" && i.MachineID == "" && i.Token == "" &&
		i.Entropy == "" && i.ClockUnixNano == 0 && !i.HostKeys
}

// isMachineID reports whether s is the 32 lowercase hex characters systemd's
// machine-id file holds.
func isMachineID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// isHostname reports whether s is a name the guest's kernel will accept: a
// hostname is at most 253 characters of letters, digits, dots and hyphens, and the
// guest sets it with sethostname, which refuses anything else.
func isHostname(s string) bool {
	if len(s) > 253 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
		default:
			return false
		}
	}
	return true
}

// PrepareReport is the guest's answer to `prepare`: what it carries now, read
// back after applying, rather than what it was asked for.
//
// The host holds it to the identity it published — an instance whose MAC, address
// or token did not take is not a clone of the template, it is the template — so
// the report is the evidence the hook actually happened, not a receipt for a
// request that was accepted and ignored.
type PrepareReport struct {
	// Interface is the interface the identity was applied to.
	Interface string `json:"interface,omitempty"`
	// MAC is the link address the interface carries now.
	MAC string `json:"mac,omitempty"`
	// Addresses are the interface's IPv4 addresses, in CIDR form, as the guest
	// sees them after the hook.
	Addresses []string `json:"addresses,omitempty"`
	// Routes are the interface's routes, formatted the way a reader expects
	// ("default via 10.0.0.1 dev eth0").
	Routes []string `json:"routes,omitempty"`
	// DNS is what /etc/resolv.conf holds now.
	DNS []string `json:"dns,omitempty"`
	// Hostname is the guest's host name now.
	Hostname string `json:"hostname,omitempty"`
	// MachineID is the machine id the guest carries now.
	MachineID string `json:"machineId,omitempty"`
	// TokenRotated reports that the guest agent's token was replaced by this
	// run's identity. The new value is not in the report: the host minted it.
	TokenRotated bool `json:"tokenRotated,omitempty"`
	// EntropyBits is how many bits of fresh entropy were mixed into the kernel's
	// pool.
	EntropyBits int `json:"entropyBits,omitempty"`
	// HostKeys lists the SSH host key types regenerated by this run.
	HostKeys []string `json:"hostKeys,omitempty"`
	// ClockUnixNano is the wall clock the guest holds now, in Unix nanoseconds.
	ClockUnixNano int64 `json:"clockUnixNano,omitempty"`
	// BootID is the boot identity the guest reports after the hook. It differs
	// from the one the same guest reported before the hook and from the one the
	// checkpoint's source machine reported, which is what tells a host that a
	// session it holds describes a different instance.
	BootID string `json:"bootId,omitempty"`
	// Changed names the parts this run actually changed. A repeated prepare with
	// the same identity changes nothing and reports an empty list, which is the
	// guest's own evidence that the operation is idempotent.
	Changed []string `json:"changed,omitempty"`
}

// ChangedPart names one part of the guest a prepare run can change. The names are
// stable: they appear in the report, in logs and in tests.
const (
	PartMAC       = "mac"
	PartAddress   = "address"
	PartRoutes    = "routes"
	PartDNS       = "dns"
	PartHostname  = "hostname"
	PartMachineID = "machineId"
	PartToken     = "token"
	PartEntropy   = "entropy"
	PartHostKeys  = "hostKeys"
	PartClock     = "clock"
	PartBootID    = "bootId"
)

// maxEntropyBytes bounds the entropy an identity may carry. The kernel credits at
// most 8 bits per byte, so this is far below any pool limit while still being far
// more than a clone needs.
const maxEntropyBytes = 256

// fetchIdentity reads the identity this clone was given, retrying until it has one
// or the request's own budget runs out. It is the half of the hook that talks to the
// node; what it returns is applied by applyIdentity.
//
// The retry is not belt and braces: Firecracker documents that "both network and
// vsock packet loss can be expected on guests that are resumed from snapshots in
// another Firecracker process", and the metadata read is the first thing a clone does
// with its network after a resume. Measured on this host, a restore loses roughly one
// request in three, so a read that gave up on the first lost packet would fail
// launches a retry completes.
//
// The budget is the host's, not this loop's: the request carries a relative deadline,
// so the retries cannot outlive the launch's window, and the pause between them is
// measured on the monotonic clock — which a restored guest can rely on even when its
// wall clock is wrong.
var fetchIdentity = func(ctx context.Context) (Identity, error) {
	var lastErr error
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			// A request that is over before the first read has no attempt to report, and
			// there is nothing to wrap: %w on a nil error prints "%!w(<nil>)".
			if lastErr == nil {
				return Identity{}, fmt.Errorf("metadata service: the request was over before the first read: %w", err)
			}
			return Identity{}, fmt.Errorf("metadata service: gave up after %d attempts: %w: %w", attempt-1, err, lastErr)
		}
		id, err := readIdentityMMDS(ctx)
		if err == nil {
			return id, nil
		}
		lastErr = err

		select {
		case <-ctx.Done():
			return Identity{}, fmt.Errorf("metadata service: gave up after %d attempts: %w: %w", attempt, ctx.Err(), lastErr)
		case <-time.After(mmdsReadBackoff):
		}
	}
}

// applyIdentity makes the guest carry id and reports what it carries afterwards.
// It is the platform's applier (prepare_linux.go), and it is a seam so the
// contract around it — reach the channel, read, validate, apply, report — is
// testable where there is no guest to reconfigure.
var applyIdentity = applyIdentityOS

// reachMetadataService brings iface up and gives the guest a way to reach the
// metadata service on it, before anything is known about the identity. It is the
// platform's job (prepare_linux.go).
var reachMetadataService = reachMetadataServiceOS

// prepareGuest runs the whole hook: make the metadata service reachable, read the
// identity the node published there, apply it, and report what the guest carries
// now.
func prepareGuest(ctx context.Context) (PrepareReport, error) {
	if err := reachMetadataService(DefaultIdentityInterface); err != nil {
		return PrepareReport{}, fmt.Errorf("reach the metadata service on %s: %w", DefaultIdentityInterface, err)
	}

	id, err := fetchIdentity(ctx)
	if err != nil {
		return PrepareReport{}, err
	}
	if id.Interface == "" {
		id.Interface = DefaultIdentityInterface
	}
	if err := id.Validate(); err != nil {
		return PrepareReport{}, fmt.Errorf("the identity published for this clone is not usable: %w", err)
	}

	report, err := applyIdentity(ctx, id)
	if err != nil {
		return PrepareReport{}, err
	}

	// The boot identity belongs to the hook, not to the applier: a guest that has
	// just adopted a new identity must not keep reporting the one it was restored
	// with, or a host holding a session from before the restore would address the
	// clone as its source machine.
	report.BootID = RotateBootID()
	report.Changed = append(report.Changed, PartBootID)
	return report, nil
}

// GenerationID identifies this application of the identity: the parts that make
// one clone a different machine from another, and none of the parts that change
// every time a node prepares the same clone.
//
// The token is deliberately outside it — a node rotates the token on every resume
// — and so are the clock and the entropy, which are supplied per boot. What is
// inside is the machine's own identity: its interface, MAC, address, routes,
// resolver, host name and machine id. A guest prepared twice with the same
// identity has the same generation, which is what lets the parts only a machine
// may mint (its machine id, its SSH host keys) be minted once.
func (i Identity) GenerationID() string {
	routes := make([]string, 0, len(i.Routes))
	for _, route := range i.Routes {
		routes = append(routes, route.Destination+" via "+route.Gateway)
	}
	h := sha256.New()
	for _, part := range []string{
		i.Interface, i.MAC, i.Address, i.Gateway,
		strings.Join(routes, ","),
		strings.Join(i.DNS, ","),
		i.Hostname, i.MachineID,
	} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// decodeEntropy decodes the hex entropy an identity carries.
func decodeEntropy(encoded string) ([]byte, error) {
	entropy, err := hex.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("not hex-encoded: %w", err)
	}
	if len(entropy) == 0 {
		return nil, errors.New("is empty")
	}
	if len(entropy) > maxEntropyBytes {
		return nil, fmt.Errorf("carries %d bytes, more than the %d allowed", len(entropy), maxEntropyBytes)
	}
	return entropy, nil
}

// readIdentityMMDS reads the identity document the node published at
// MMDSIdentityPath, once.
//
// The metadata service speaks version 2, which requires a session token: the
// guest mints one with a PUT and presents it on the read. A service that answers
// without a document, or with a document this agent cannot read, is an error —
// never an empty identity, which would look like "nothing to do".
func readIdentityMMDS(ctx context.Context) (Identity, error) {
	ctx, cancel := context.WithTimeout(ctx, mmdsTimeout)
	defer cancel()

	token, err := mmdsSessionToken(ctx)
	if err != nil {
		return Identity{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mmdsBaseURL+MMDSIdentityPath, nil)
	if err != nil {
		return Identity{}, fmt.Errorf("metadata service: build the %s request: %w", MMDSIdentityPath, err)
	}
	req.Header.Set("X-metadata-token", token)

	resp, err := mmdsHTTPClient.Do(req)
	if err != nil {
		return Identity{}, fmt.Errorf("metadata service: read %s: %w", MMDSIdentityPath, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxIdentityDocument))
	if err != nil {
		return Identity{}, fmt.Errorf("metadata service: read %s: %w", MMDSIdentityPath, err)
	}
	if resp.StatusCode != http.StatusOK {
		return Identity{}, fmt.Errorf("metadata service: %s answered %s: %s",
			MMDSIdentityPath, resp.Status, strings.TrimSpace(string(body)))
	}

	var id Identity
	if err := json.Unmarshal(body, &id); err != nil {
		return Identity{}, fmt.Errorf("metadata service: parse the identity published at %s: %w", MMDSIdentityPath, err)
	}
	return id, nil
}

// mmdsSessionToken mints a metadata session token.
func mmdsSessionToken(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, mmdsBaseURL+"/latest/api/token", nil)
	if err != nil {
		return "", fmt.Errorf("metadata service: build the session token request: %w", err)
	}
	req.Header.Set("X-metadata-token-ttl-seconds", fmt.Sprint(mmdsTokenTTLSeconds))

	resp, err := mmdsHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("metadata service: mint a session token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil {
		return "", fmt.Errorf("metadata service: read the session token: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("metadata service: minting a session token answered %s: %s",
			resp.Status, strings.TrimSpace(string(body)))
	}
	token := strings.TrimSpace(string(body))
	if token == "" {
		return "", errors.New("metadata service: answered an empty session token")
	}
	return token, nil
}
