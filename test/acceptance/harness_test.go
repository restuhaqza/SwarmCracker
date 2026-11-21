//go:build acceptance

// Package acceptance is the MVP's end-to-end evidence on a real KVM host: one node,
// one operator, and the whole cycle a caller performs — create, exec, cp, expose,
// checkpoint, fork, rm — plus the security rules that make handing a sandbox to an
// agent defensible.
//
// It is not a unit test and it cannot run on a machine without KVM. It drives the two
// binaries a caller actually uses — `sandboxd` and `swarmcracker` — against one
// another over the daemon's own socket and its own TLS listener, in directories the
// run creates and deletes. Every assertion is made from outside: the guest's exit
// codes, the CLI's documents, the host's own `ip`, `ss` and `/proc`.
//
// The point is the difference between "the code says it does this" and "this node did
// it, and here is the log". Each subtest names the epic acceptance criterion it
// settles (#121), and the run's log is the evidence: it holds the commands, the
// guest's own output, and the host state before and after.
//
// What this package does NOT prove, deliberately: the ≤500 ms warm path (that is a
// pool, and there is none), a second node, pause/resume, `sandbox shell`/`attach`,
// the template listing route, `activity`/`quiesce` consistency under write load, and a
// cross-node restore. Those are outside the MVP and their absence here is not an
// omission.
package acceptance

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
)

// The binaries the run drives. They are built by scripts/host-evidence/run.sh and
// named here rather than looked up on PATH: a run must exercise the tree it checked
// out, not whatever is installed on the host.
const (
	sandboxdBin     = "SANDBOXD_BIN"
	swarmcrackerBin = "SWARMCRACKER_BIN"
	defaultSandboxd = "sandboxd"
	defaultSwarmcli = "swarmcracker"
	createDeadline  = 4 * time.Second
	templateEnv     = "SWARMCRACKER_TEST_TEMPLATE"
	// olderTemplateEnv names a template on the host whose agent predates the rooted
	// capability — a stock template that was never rebuilt, which is the case that would
	// be written to without confinement if the capability gate were missing.
	olderTemplateEnv = "SWARMCRACKER_TEST_OLDER_TEMPLATE"
	defaultTemplate  = "sandbox-base"
	keepScratchEnv   = "SWARMCRACKER_TEST_KEEP"
	evidenceDirEnv   = "SWARMCRACKER_TEST_EVIDENCE"
	daemonStartLimit = 60 * time.Second
)

// host is one run's node: the daemon, the directories it owns, and the CLI that
// reaches it. Everything a run creates lives under scratch, so a node that carries
// leftovers from other work is not a reason for this run to skip or to pass: the
// leftovers are somebody else's, and this run's are counted by name.
type host struct {
	t *testing.T

	sandboxd string
	cli      string
	template string

	scratch   string // the run's own tree, deleted unless SWARMCRACKER_TEST_KEEP=1
	stateDir  string // sandbox records, instance registry, API tokens, certificates
	goldenDir string // the template store this run boots from
	rootfsDir string
	jailDir   string
	logDir    string
	socket    string
	apiAddr   string
	caFile    string

	tokenFile string // an admin token, for the TCP listener
	apiToken  string

	daemon  *exec.Cmd
	logFile *os.File
}

// newHost prepares the run's tree, its cluster certificates, its admin token and its
// daemon, and waits until the daemon answers. It fails rather than skips when the
// host cannot serve a sandbox: on this path a missing KVM, jailer or template is the
// finding, not a reason to report success.
// hostPrerequisite is what a run needs before it can start. It is a variable so the
// harness's own behaviour — one criterion failing must not hide the ones after it — can
// be tested on a machine that has no KVM, which is every machine the tests themselves
// are written on.
var hostPrerequisite = requireJailedHost

func newHost(t *testing.T) *host {
	t.Helper()
	hostPrerequisite(t)

	h := &host{
		t:         t,
		sandboxd:  binaryPath(t, sandboxdBin, defaultSandboxd),
		cli:       binaryPath(t, swarmcrackerBin, defaultSwarmcli),
		template:  envOr(templateEnv, defaultTemplate),
		scratch:   shortScratch(t),
		goldenDir: envOr("SWARMCRACKER_GOLDEN_DIR", "/var/lib/firecracker/golden"),
	}
	h.stateDir = filepath.Join(h.scratch, "state")
	h.rootfsDir = filepath.Join(h.scratch, "rootfs")
	h.jailDir = filepath.Join(h.scratch, "jails")
	h.logDir = filepath.Join(h.scratch, "logs")
	h.socket = filepath.Join(h.scratch, "sandboxd.sock")

	requireHostServesSandboxes(t, h)

	// The daemon's own shared v1 surface takes the node's cluster client, which reads
	// its certificates from <state-dir>/certificates. This run is isolated, so it
	// writes its own pair: the socket transport verifies the chain but not a host
	// name, and the sandbox routes never reach a cluster, so a self-signed pair is
	// exactly what an isolated control plane needs.
	h.writeClusterCertificates()
	h.mintAdminToken()
	h.startDaemon()

	t.Cleanup(func() {
		if h.daemon != nil {
			h.stopDaemon()
		}
	})
	return h
}

// shortScratch makes the run's tree under /var/tmp, with a short name: a jailed
// instance's API socket lives inside its chroot, so its host path is
// <scratch>/jails/firecracker/<id>/root/run/firecracker/<id>.sock and must fit in the
// 108 bytes a unix socket address holds. t.TempDir() is under /tmp with the test's own
// name in it, which is long enough to fail launch for a reason that has nothing to do
// with the criterion under test.
func shortScratch(t *testing.T) string {
	t.Helper()
	base := os.Getenv("SWARMCRACKER_TEST_SCRATCH")
	if base == "" {
		base = "/var/tmp"
	}
	dir, err := os.MkdirTemp(base, "sbxa-")
	require.NoError(t, err, "creating the run's scratch tree under %s", base)
	if os.Getenv(keepScratchEnv) == "1" {
		t.Logf("keeping the run's tree at %s (%s=1)", dir, keepScratchEnv)
		return dir
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// requireJailedHost is the hard prerequisite check: Linux, root, KVM, and the two
// binaries a jailed launch cannot do without.
func requireJailedHost(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Fatalf("this run boots jailed VMs and needs root (chroot, chown, /dev/net/tun); run it as root")
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Fatalf("/dev/kvm is not available: %v", err)
	}
	for _, name := range []string{"firecracker", "jailer", "ip", "nft", "lsns", "ss"} {
		if _, err := exec.LookPath(name); err != nil {
			if name == "nft" {
				if _, statErr := os.Stat("/usr/sbin/nft"); statErr == nil {
					continue
				}
			}
			t.Fatalf("%s is required on the host: %v", name, err)
		}
	}
}

// requireHostServesSandboxes checks the two things this run needs that are not
// binaries: a template this host can boot. A host with no template is a finding with
// one action, so the failure says the action.
func requireHostServesSandboxes(t *testing.T, h *host) {
	t.Helper()
	entries, err := os.ReadDir(h.goldenDir)
	if err != nil {
		t.Fatalf("the template store %s cannot be read: %v (build a template with `swarmcracker template build recipes/sandbox-base.yaml`, or point %s at the store)",
			h.goldenDir, err, "SWARMCRACKER_GOLDEN_DIR")
	}
	found := false
	want := h.template
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		// "sandbox-base" matches "golden-sandbox-base-1.0.0.json": the store names
		// manifests <name>-<version>.json, and a caller names the template by its own
		// name. Asking Resolve through the CLI would need the daemon, so the file name
		// is enough to say whether the store could answer.
		if strings.Contains(name, strings.TrimSuffix(want, "@"+versionOf(want))) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("the template store %s has no template named %q (set %s to the template this host should boot, or build one with `swarmcracker template build recipes/sandbox-base.yaml`)",
			h.goldenDir, want, templateEnv)
	}
}

// versionOf returns the "@version" suffix's version, or "".
func versionOf(ref string) string {
	if i := strings.LastIndex(ref, "@"); i >= 0 {
		return ref[i+1:]
	}
	return ""
}

// binaryPath resolves a binary from its environment variable, then from PATH, and
// fails with the one thing the operator has to do about it.
func binaryPath(t *testing.T, env, fallback string) string {
	t.Helper()
	if path := strings.TrimSpace(os.Getenv(env)); path != "" {
		require.FileExists(t, path, "%s points at a binary that is not there", env)
		return path
	}
	path, err := exec.LookPath(fallback)
	require.NoError(t, err, "%s is not on PATH and %s is not set: build it from this checkout first (go build -o build/%s ./cmd/%s)", fallback, env, fallback, fallback)
	return path
}

// writeClusterCertificates writes the self-signed pair and CA a node-local client
// needs. It is a certificate, not a credential: nothing outside this scratch tree
// trusts it.
func (h *host) writeClusterCertificates() {
	h.t.Helper()
	dir := filepath.Join(h.stateDir, "certificates")
	require.NoError(h.t, os.MkdirAll(dir, 0o700))

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(h.t, err)
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	require.NoError(h.t, err)

	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "swarmcracker-acceptance"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(h.t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(h.t, err)

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	require.NoError(h.t, os.WriteFile(filepath.Join(dir, "swarm-node.crt"), certPEM, 0o600))
	require.NoError(h.t, os.WriteFile(filepath.Join(dir, "swarm-node.key"), keyPEM, 0o600))
	require.NoError(h.t, os.WriteFile(filepath.Join(dir, "swarm-root-ca.crt"), certPEM, 0o600))
}

// mintAdminToken mints the token the TLS listener authenticates. It goes through the
// CLI's own token store, which is the same file the daemon reads, so this is the
// operator's real path rather than a fixture written behind its back.
func (h *host) mintAdminToken() {
	h.t.Helper()
	h.tokenFile = filepath.Join(h.stateDir, "api", "tokens.json")
	require.NoError(h.t, os.MkdirAll(filepath.Dir(h.tokenFile), 0o700))

	secret, stderr, code := h.run("api", "token", "create", "--name", "acceptance", "--role", "admin",
		"--token-file", h.tokenFile)
	require.Zero(h.t, code, "minting the admin token failed:\n%s", stderr)
	h.apiToken = strings.TrimSpace(strings.SplitN(secret, "\n", 2)[0])
	require.NotEmpty(h.t, h.apiToken, "the CLI printed no token secret:\n%s", secret)
}

// startDaemon starts the node's control plane in this run's own directories, on its
// own socket and its own loopback port, and waits for the socket to answer.
func (h *host) startDaemon() {
	h.t.Helper()

	// A port the OS picks, released before the daemon takes it: the run must not
	// collide with another daemon on the host, and the address has to be in the
	// certificate it is about to be issued.
	port := freePort(h.t)
	h.apiAddr = fmt.Sprintf("127.0.0.1:%d", port)

	for _, dir := range []string{h.stateDir, h.rootfsDir, h.jailDir, h.logDir} {
		require.NoError(h.t, os.MkdirAll(dir, 0o755))
	}

	logPath := filepath.Join(h.scratch, "sandboxd.log")
	f, err := os.Create(logPath)
	require.NoError(h.t, err)
	h.logFile = f

	args := []string{
		"--state-dir", h.stateDir,
		"--golden-dir", h.goldenDir,
		"--rootfs-dir", h.rootfsDir,
		"--jailer-chroot-dir", h.jailDir,
		"--log-dir", h.logDir,
		"--socket", h.socket,
		"--api-listen", h.apiAddr,
		"--api-token-file", h.tokenFile,
		"--api-cert-dir", filepath.Join(h.stateDir, "api"),
		"--reap-interval", "10s",
		"--default-ttl", "15m",
	}
	h.daemon = exec.Command(h.sandboxd, args...)
	h.daemon.Stdout = f
	h.daemon.Stderr = f
	// The daemon's own children must not outlive the run in a way the harness cannot
	// see; a process group lets the cleanup signal all of them at once.
	h.daemon.SysProcAttr = sysProcAttr()
	require.NoError(h.t, h.daemon.Start(), "starting sandboxd")

	h.caFile = filepath.Join(h.stateDir, "api", "tls.crt")

	deadline := time.Now().Add(daemonStartLimit)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(h.socket); err == nil {
			if _, stderr, code := h.run("sandbox", "ls"); code == 0 {
				h.t.Logf("sandboxd is serving: socket=%s api=%s (log %s)", h.socket, h.apiAddr, logPath)
				return
			} else if strings.Contains(stderr, "unreachable") {
				// The socket exists; the daemon is still opening its stores.
			}
		}
		if h.daemon.ProcessState != nil && h.daemon.ProcessState.Exited() {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	h.t.Fatalf("sandboxd did not answer on %s within %s; its log:\n%s", h.socket, daemonStartLimit, h.tailDaemonLog())
}

// stopDaemon stops the daemon and waits for it, so the cleanup checks run against a
// quiescent host rather than one whose children are still exiting.
func (h *host) stopDaemon() {
	h.t.Helper()
	if h.daemon == nil || h.daemon.Process == nil {
		return
	}
	_ = signalGroup(h.daemon)
	done := make(chan error, 1)
	go func() { done <- h.daemon.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		_ = h.daemon.Process.Kill()
		<-done
	}
	if h.logFile != nil {
		_ = h.logFile.Close()
	}
	h.daemon = nil
}

// run runs the CLI and returns its stdout, stderr and exit code. Every invocation is
// logged with the sandbox socket it used, so the log is reproducible by hand.
func (h *host) run(args ...string) (string, string, int) {
	h.t.Helper()
	cmd := exec.Command(h.cli, args...)
	var out, errOut strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	cmd.Env = append(os.Environ(), "SWARM_SANDBOX_SOCKET="+h.socket)
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			code = -1
		}
	}
	h.t.Logf("[cli] swarmcracker %s\n  exit %d\n%s%s", strings.Join(args, " "), code,
		indent(out.String()), indent(errOut.String()))
	return out.String(), errOut.String(), code
}

// mustRun runs a CLI command that has to succeed, and returns its stdout.
func (h *host) mustRun(args ...string) string {
	h.t.Helper()
	out, stderr, code := h.run(args...)
	require.Zero(h.t, code, "swarmcracker %s failed:\n%s", strings.Join(args, " "), stderr)
	return out
}

// runJSON runs a CLI command with -o json and decodes its document into v.
func (h *host) runJSON(v any, args ...string) {
	h.t.Helper()
	out := h.mustRun(append(args, "-o", "json")...)
	require.NoError(h.t, json.Unmarshal([]byte(out), v), "decoding the CLI's document:\n%s", out)
}

// sandbox is the document one sandbox is reported as. Only the fields this run acts
// on are named: the rest of the contract is the API's, not this harness's.
type sandbox struct {
	ID             string   `json:"id"`
	State          string   `json:"state"`
	Template       string   `json:"template"`
	GuestIP        string   `json:"guestIp"`
	MAC            string   `json:"mac"`
	CreatedAt      string   `json:"createdAt"`
	ReadyAt        string   `json:"readyAt"`
	IdleTTLSeconds int64    `json:"idleTtlSeconds"`
	AgentVersion   string   `json:"agentVersion"`
	BootID         string   `json:"bootId"`
	Egress         []string `json:"egress"`
	Ports          []struct {
		GuestPort uint16 `json:"guestPort"`
		Address   string `json:"address"`
		URL       string `json:"url"`
		ExpiresAt string `json:"expiresAt"`
	} `json:"ports"`
}

// sandboxDocument is the create/read document: the envelope's fields sit beside the
// sandbox's own.
type sandboxDocument struct {
	SchemaVersion int    `json:"schemaVersion"`
	Kind          string `json:"kind"`
	sandbox
}

// sandboxList is the listing document.
type sandboxList struct {
	SchemaVersion int       `json:"schemaVersion"`
	Kind          string    `json:"kind"`
	Items         []sandbox `json:"items"`
	Checkpoint    string    `json:"checkpoint"`
}

// createFrom makes one sandbox from a named template, for a criterion that has to boot a
// template other than the run's own — an older one, above all.
func (h *host) createFrom(id, tmpl string, extra ...string) sandbox {
	h.t.Helper()
	args := append([]string{"sandbox", "create", tmpl, "--id", id}, extra...)
	var doc sandboxDocument
	h.runJSON(&doc, args...)
	require.Equal(h.t, "Sandbox", doc.Kind)
	require.Equal(h.t, "ready", doc.State, "create must return a ready sandbox, not a VMM that started")
	require.Equal(h.t, id, doc.ID)
	return doc.sandbox
}

// create makes one sandbox and returns its document.
func (h *host) create(id string, extra ...string) sandbox {
	h.t.Helper()
	args := append([]string{"sandbox", "create", h.template, "--id", id}, extra...)
	var doc sandboxDocument
	h.runJSON(&doc, args...)
	require.Equal(h.t, "Sandbox", doc.Kind)
	require.Equal(h.t, "ready", doc.State, "create must return a ready sandbox, not a VMM that started")
	require.Equal(h.t, id, doc.ID)
	return doc.sandbox
}

// get reads one sandbox back.
func (h *host) get(id string) sandbox {
	h.t.Helper()
	var doc sandboxDocument
	h.runJSON(&doc, "sandbox", "inspect", id)
	return doc.sandbox
}

// list returns every sandbox the node has a record of.
func (h *host) list() []sandbox {
	h.t.Helper()
	var doc sandboxList
	h.runJSON(&doc, "sandbox", "ls")
	return doc.Items
}

// exec runs a command in a sandbox and returns its exit code and combined output.
func (h *host) exec(id string, command ...string) (string, int) {
	h.t.Helper()
	args := append([]string{"sandbox", "exec", id, "--"}, command...)
	out, stderr, code := h.run(args...)
	return out + stderr, code
}

// mustExec runs a command that has to succeed and returns its output.
func (h *host) mustExec(id string, command ...string) string {
	h.t.Helper()
	out, code := h.exec(id, command...)
	require.Zero(h.t, code, "sandbox %s: %v exited %d:\n%s", id, command, code, out)
	return out
}

// remove removes a sandbox and returns the teardown report.
type removal struct {
	Sandbox string `json:"sandbox"`
	Freed   struct {
		ProcessStopped         bool `json:"processStopped"`
		SocketRemoved          bool `json:"socketRemoved"`
		CgroupRemoved          bool `json:"cgroupRemoved"`
		JailRemoved            bool `json:"jailRemoved"`
		NetNSRemoved           bool `json:"netnsRemoved"`
		TAPRemoved             bool `json:"tapRemoved"`
		DiskRemoved            bool `json:"diskRemoved"`
		PreviewsClosed         int  `json:"previewsClosed"`
		AgentCredentialRemoved bool `json:"agentCredentialRemoved"`
		InstanceRecordRemoved  bool `json:"instanceRecordRemoved"`
		RecordRemoved          bool `json:"recordRemoved"`
		APITokensRevoked       int  `json:"apiTokensRevoked"`
	} `json:"freed"`
}

func (h *host) remove(id string) removal {
	h.t.Helper()
	var doc removal
	h.runJSON(&doc, "sandbox", "rm", id)
	require.Equal(h.t, id, doc.Sandbox)
	return doc
}

// tailDaemonLog is the last of the daemon's log, for a failure that happened before
// it served anything.
func (h *host) tailDaemonLog() string {
	data, err := os.ReadFile(filepath.Join(h.scratch, "sandboxd.log"))
	if err != nil {
		return "(no log)"
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > 40 {
		lines = lines[len(lines)-40:]
	}
	return strings.Join(lines, "\n")
}

// templateRef is the template this run boots, as "name@version": `template inspect`
// takes a ref, and a bare name is a usage error rather than a lookup.
//
// The version comes from the store's manifest for that name, so the run inspects exactly
// the template a create resolves. It returns an error rather than failing the test: its
// caller is the criterion that needs the ref, and that criterion is where the failure is
// recorded.
func (h *host) templateRef(t *testing.T) (string, error) {
	t.Helper()
	if strings.Contains(h.template, "@") {
		return h.template, nil
	}

	matches, err := filepath.Glob(filepath.Join(h.goldenDir, "golden-"+h.template+"-*.json"))
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("the template store %s has no manifest named golden-%s-<version>.json",
			h.goldenDir, h.template)
	}

	// The newest one, which is the version a create resolves when it names only the
	// name: the store's rule is the newest manifest for a name.
	newest := matches[0]
	for _, match := range matches[1:] {
		a, errA := os.Stat(match)
		b, errB := os.Stat(newest)
		if errA == nil && errB == nil && a.ModTime().After(b.ModTime()) {
			newest = match
		}
	}

	// "golden-sandbox-base-1.0.0.json" is <name>-<version>: the version is what follows
	// the last hyphen of the base name.
	name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(newest), "golden-"), ".json")
	at := strings.LastIndex(name, "-")
	if at <= 0 {
		return "", fmt.Errorf("the manifest %s does not name a version", filepath.Base(newest))
	}
	return name[:at] + "@" + name[at+1:], nil
}

// host run helpers.

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

// freePort asks the kernel for an unused port and gives it back. The window between
// the two is the reason a bind failure is retried in the caller, not assumed away.
func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	return port
}

// indent lays a command's output out under its command line.
func indent(out string) string {
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return ""
	}
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		lines[i] = "  | " + line
	}
	return strings.Join(lines, "\n") + "\n"
}

// hostCommand runs one host command and returns its combined output, logging it.
func hostCommand(t *testing.T, label, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			code = -1
		}
	}
	t.Logf("[host] %s\n  $ %s %s\n  exit %d\n%s", label, name, strings.Join(args, " "), code, indent(string(out)))
	return string(out)
}

// olderTemplate finds a template on this host whose agent predates the rooted capability.
//
// It is what makes the capability criterion a criterion rather than a fixture: the case it
// tests is a template that was never rebuilt, and the most useful one is whichever the host
// actually has. The configured name wins; failing that, the store is searched for a
// manifest whose recorded agent does not advertise `rooted` and which this host says it
// can boot, and the path it picked is returned so the run can say which one it used.
//
// It returns an error, not a skip: a host with no older template has not measured the
// criterion, and this run is the place that says so.
func (h *host) olderTemplate(t *testing.T) (string, string, error) {
	t.Helper()

	if configured := strings.TrimSpace(os.Getenv(olderTemplateEnv)); configured != "" {
		return configured, "configured by " + olderTemplateEnv, nil
	}

	// The stores a host may keep templates in: the one this run boots from, and the one
	// `setup install` writes to.
	stores := []string{h.goldenDir, "/var/lib/swarmcracker/templates"}
	var tried []string
	for _, store := range stores {
		entries, err := os.ReadDir(store)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			path := filepath.Join(store, entry.Name())
			ref, lacksRooted, err := manifestLacksRooted(path)
			if err != nil || !lacksRooted {
				continue
			}
			tried = append(tried, ref)

			// A manifest this host cannot boot is not this criterion's subject: create
			// would fail for the pin, which says nothing about the capability. `template
			// inspect` is the same check a create makes, asked without booting anything.
			out, stderr, _ := h.run("template", "inspect", ref, "--output-dir", store)
			if !strings.Contains(out+stderr, "usable on this node") {
				continue
			}
			return ref, fmt.Sprintf("%s (found in %s, not named by %s)", ref, store, olderTemplateEnv), nil
		}
	}

	if len(tried) > 0 {
		return "", "", fmt.Errorf("the host has %d template(s) without the rooted capability (%s) but none this node can boot",
			len(tried), strings.Join(tried, ", "))
	}
	return "", "", fmt.Errorf("no template without the rooted capability in %s or /var/lib/swarmcracker/templates: a stock template that predates the capability is the case this criterion exists for",
		h.goldenDir)
}

// manifestLacksRooted reports whether a template manifest's recorded agent does not
// advertise the rooted capability, and the ref the manifest names.
func manifestLacksRooted(path string) (string, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	var manifest struct {
		Name  string `json:"name"`
		Vert  string `json:"version"`
		Agent struct {
			Caps []string `json:"agentCaps"`
		} `json:"agent"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return "", false, err
	}
	if manifest.Name == "" || manifest.Vert == "" {
		return "", false, fmt.Errorf("%s names no template", path)
	}
	for _, capability := range manifest.Agent.Caps {
		if capability == agent.CapRooted {
			return "", false, nil
		}
	}
	return manifest.Name + "@" + manifest.Vert, true, nil
}
