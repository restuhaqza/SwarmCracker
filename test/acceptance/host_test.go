//go:build acceptance

package acceptance

// The host-side questions the MVP's criteria ask, asked of the host rather than of the
// control plane's own answers: which namespaces exist, which listeners are open, which
// VMMs run, what the node's own address is, which token a sandbox's muxer requires.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/restuhaqza/swarmcracker/pkg/vminstance"
)

// contextWithTimeout is context.WithTimeout with no cancel leak at the call site.
func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// gatewayOf returns the gateway of a sandbox's own /30: the address inside its
// namespace that its default route points at. It is derived from the guest address the
// node reports, because the namespace is the only place the two are the same link.
func gatewayOf(t *testing.T, s sandbox) string {
	t.Helper()
	parts := strings.Split(s.GuestIP, ".")
	require.Len(t, parts, 4, "the guest address %q is IPv4", s.GuestIP)
	// The /30's first host is the gateway and its second is the guest: the address the
	// guest wears minus one is the address its namespace holds.
	var last int
	_, err := fmt.Sscanf(parts[3], "%d", &last)
	require.NoError(t, err)
	require.Greater(t, last, 1, "a /30's guest address has a gateway below it: %q", s.GuestIP)
	return fmt.Sprintf("%s.%s.%s.%d", parts[0], parts[1], parts[2], last-1)
}

// nodeAddress is the node's own address on its LAN, which no sandbox may reach: the
// first non-loopback IPv4 address the host has.
func nodeAddress(t *testing.T) string {
	t.Helper()
	out := hostCommand(t, "the node's own addresses", "/bin/sh", "-c",
		"ip -o -4 addr show scope global | awk '{print $4}' | cut -d/ -f1 | head -1")
	addr := strings.TrimSpace(out)
	if addr == "" {
		// A host with no global address (a container, an isolated lab) still has one
		// address a sandbox must not reach: its own default gateway.
		out = hostCommand(t, "the node's default route", "/bin/sh", "-c",
			"ip -4 route show default | awk '{print $3}' | head -1")
		addr = strings.TrimSpace(out)
	}
	require.NotEmpty(t, addr, "the node has no IPv4 address a sandbox could try to reach")
	return addr
}

// previewListeners are the preview proxy's sockets on the node's loopback, by
// address:port. A preview binds a derived loopback address, so a listener on one that
// is still there after a removal is a leaked preview.
func previewListeners(t *testing.T) []string {
	t.Helper()
	out := hostCommand(t, "loopback listeners", "/bin/sh", "-c",
		"ss -ltnH 2>/dev/null | awk '{print $4}' | grep '^127\\.' | grep -v '^127\\.0\\.0\\.1:' || true")
	return strings.Fields(out)
}

// sandboxVMMs are the Firecracker processes on the node whose argument line names a
// sandbox: a VMM is a process, so the kernel is what says it is gone.
func sandboxVMMs(t *testing.T) []string {
	t.Helper()
	out := hostCommand(t, "firecracker processes", "/bin/sh", "-c",
		"pgrep -af firecracker 2>/dev/null | grep '/sbx-' || true")
	var ids []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		// Keep the pid and the sandbox id, not the whole argument line: the line
		// carries a tmpdir that changes every run, which would make two runs' lines
		// never match.
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		ids = append(ids, fields[0]+" "+sandboxIDIn(line))
	}
	return ids
}

// sandboxIDIn finds the sbx-<id> token in a process's argument line.
func sandboxIDIn(line string) string {
	for _, field := range strings.Fields(line) {
		if i := strings.Index(field, "sbx-"); i >= 0 {
			id := field[i:]
			if j := strings.IndexFunc(id, func(r rune) bool {
				return r == '/' || r == '\n' || r == '\t'
			}); j >= 0 {
				id = id[:j]
			}
			return id
		}
	}
	return "unknown"
}

// sandboxMuxer finds the vsock muxer a sandbox's guest agent answers on, and the token
// this node minted for it, from the node's instance registry.
//
// It is the host's own registry read straight from disk rather than the API: the
// question is whether the *host's* copy of the credential is what the guest accepts,
// and a route that answered from memory would be the control plane vouching for
// itself.
func sandboxMuxer(t *testing.T, h *host, id string) (vsockPath, token string) {
	t.Helper()

	store, err := vminstance.NewStore(vminstance.Config{Dir: vminstance.Dir(h.stateDir)})
	require.NoError(t, err, "opening the node's instance registry")
	instance, found, err := store.Get(id)
	require.NoError(t, err)
	require.True(t, found, "the node has an instance record for %s", id)

	tokenFile := agent.TokenFilePath(instance.VsockPath)
	read, foundToken, err := agent.ReadTokenFile(tokenFile)
	require.NoError(t, err, "reading the token beside %s", tokenFile)
	require.True(t, foundToken, "the node published its copy of %s's agent token at %s", id, tokenFile)
	return instance.VsockPath, read
}

// isAuthRefusal reports whether a failed dial failed because of the credential. The
// guest closes the connection without a frame when the token is wrong or missing, so
// the class is what can be checked: not a socket that was not there.
func isAuthRefusal(err error) bool {
	if err == nil {
		return false
	}
	for _, sentinel := range []error{agent.ErrTokenRejected, agent.ErrTokenRequired, agent.ErrTokenUnsupported} {
		if errors.Is(err, sentinel) {
			return true
		}
	}
	// The guest hangs up mid-handshake; the host reports the read that failed. A
	// missing muxer reports a dial error instead, which is what this excludes.
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "connect") && strings.Contains(text, "no such file") {
		return false
	}
	return strings.Contains(text, "token") || strings.Contains(text, "hello") || strings.Contains(text, "eof")
}

// copyTemplateWithoutAgent writes a template manifest with no agent capabilities into
// another store, beside a copy of the artifact, so the host can be asked what it
// thinks of it. It is the negative half of the template criterion: a manifest that
// records no working agent must be refused, not booted.
func copyTemplateWithoutAgent(t *testing.T, fromDir, toDir, name string) {
	t.Helper()

	entries, err := os.ReadDir(fromDir)
	require.NoError(t, err)

	var manifest map[string]any
	var artifact string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(fromDir, entry.Name()))
		if err != nil {
			continue
		}
		var parsed map[string]any
		if err := json.Unmarshal(raw, &parsed); err != nil {
			continue
		}
		if got, _ := parsed["name"].(string); got == "" {
			continue
		}
		manifest = parsed
		if got, _ := parsed["artifact"].(string); got != "" {
			artifact = got
		}
		break
	}
	require.NotNil(t, manifest, "the template store %s holds no manifest to copy", fromDir)
	require.NotEmpty(t, artifact, "the manifest names its artifact")

	// The manifest's identity changes, and its agent loses the capabilities this host
	// requires. Everything else is kept, so the refusal is about the agent and not
	// about a manifest that cannot be read.
	manifest["name"] = name
	manifest["version"] = "1.0.0"
	manifest["agent"] = map[string]any{
		"agentProtocol": "",
		"agentVersion":  "",
		"agentCaps":     []string{},
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(toDir, "golden-"+name+"-1.0.0.json"), raw, 0o644))

	source, err := os.ReadFile(filepath.Join(fromDir, artifact))
	require.NoError(t, err, "reading the artifact %s", artifact)
	require.NoError(t, os.WriteFile(filepath.Join(toDir, artifact), source, 0o644))
}

// hostFootprint is what the node holds of this run's kind: namespaces named after a
// sandbox, listeners of a preview, and VMM processes. It is taken before and after so a
// leftover of this run is distinguishable from a leftover of somebody else's.
type footprint struct {
	namespaces []string
	listeners  []string
	vms        []string
}

func hostFootprint(t *testing.T) footprint {
	t.Helper()
	var f footprint
	out := hostCommand(t, "sandbox namespaces", "/bin/sh", "-c",
		"ip netns list 2>/dev/null | awk '{print $1}' | grep '^sbx-' || true")
	f.namespaces = strings.Fields(out)
	f.listeners = previewListeners(t)
	f.vms = sandboxVMMs(t)
	return f
}

// beyond is what appears in f that was not in before, as readable lines.
func (f footprint) beyond(before footprint) []string {
	var out []string
	for _, line := range []struct {
		what string
		now  []string
		was  []string
	}{
		{"namespace", f.namespaces, before.namespaces},
		{"preview listener", f.listeners, before.listeners},
		{"VMM process", f.vms, before.vms},
	} {
		seen := map[string]bool{}
		for _, item := range line.was {
			seen[item] = true
		}
		for _, item := range line.now {
			if !seen[item] {
				out = append(out, fmt.Sprintf("%s %s", line.what, item))
			}
		}
	}
	return out
}

// httpGet fetches a URL and returns its status and body. It is used for the preview's
// two answers, which is why it does not follow a redirect: the status is the evidence.
func httpGet(t *testing.T, url string) (int, string) {
	t.Helper()
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(url)
	if err != nil {
		// A refused connection is a status of zero: after a removal, "nothing is
		// listening" is the answer rather than a test failure.
		t.Logf("GET %s: %v", url, err)
		return 0, err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	var body strings.Builder
	buf := make([]byte, 32<<10)
	for {
		n, readErr := resp.Body.Read(buf)
		body.Write(buf[:n])
		if readErr != nil || body.Len() > 32<<10 {
			break
		}
	}
	return resp.StatusCode, body.String()
}
