//go:build acceptance

package acceptance

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
)

// TestMVP is the epic's MVP acceptance run on one node: one operator, one sandbox,
// and the whole cycle — create, exec, cp, expose, checkpoint, fork, rm — with the
// security rules that make handing a sandbox to an agent defensible.
//
// Each subtest names the criterion it settles (#121) and records it in the run's
// evidence file. The subtests share one node and run in order, because the criteria
// are about one sandbox's life: a create that took four seconds and an rm that freed
// everything are the same sandbox.
func TestMVP(t *testing.T) {
	h := newHost(t)
	before := hostFootprint(t)

	// The MVP's own sandbox, and the one a checkpoint is taken of.
	primary := h.create("mvp-main", "--owner", "acceptance")

	// 1 — a cold create is ready inside four seconds, and the node reports its own
	// timings. The warm half of the criterion is a pool, which this slice does not
	// have, so it is not measured and not claimed.
	t.Run("criterion 1: cold create is ready within four seconds and reports timings", func(t *testing.T) {
		created, err := time.Parse(time.RFC3339Nano, primary.CreatedAt)
		require.NoError(t, err, "createdAt: %q", primary.CreatedAt)
		ready, err := time.Parse(time.RFC3339Nano, primary.ReadyAt)
		require.NoError(t, err, "readyAt: %q", primary.ReadyAt)
		window := ready.Sub(created)
		t.Logf("the node's own ready window: created %s, ready %s, window %s",
			primary.CreatedAt, primary.ReadyAt, window)
		assert.Positive(t, window, "ready must be after created")
		assert.LessOrEqual(t, window, createDeadline,
			"a cold create must be ready within %s; this one took %s", createDeadline, window)
		record(t, "1-cold-create", "ready in %s (node's own window, cold boot, not the warm pool)", window)
	})

	// 2 — exec returns stdout, stderr and the guest's exit code, separately.
	t.Run("criterion 2: exec returns stdout, stderr and the exit code", func(t *testing.T) {
		stdout, stderr, code := h.run("sandbox", "exec", primary.ID, "--", "/bin/sh", "-c",
			"echo out-line; echo err-line >&2; exit 7")
		assert.Equal(t, 7, code, "the guest's status is this command's status")
		assert.Contains(t, stdout, "out-line", "stdout arrives")
		assert.Contains(t, stderr, "err-line", "stderr arrives on stderr")
		assert.NotContains(t, stdout, "err-line", "and the two streams are not merged")
		record(t, "2-exec", "stdout %q, stderr %q, exit %d", strings.TrimSpace(stdout), strings.TrimSpace(stderr), code)
	})

	// 3 — a tree copied in is visible to exec, with its modes and its nesting.
	t.Run("criterion 3: cp moves a tree in and exec sees it", func(t *testing.T) {
		tree := filepath.Join(h.scratch, "repo")
		require.NoError(t, os.MkdirAll(filepath.Join(tree, "src", "deep"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(tree, "README.md"), []byte("# copied in\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(tree, "src", "main.sh"), []byte("echo from-the-tree\n"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(tree, "src", "deep", "note.txt"), []byte("nested\n"), 0o644))

		h.mustRun("sandbox", "cp", tree, primary.ID+":workspace/repo")

		// Read it back the way a workload would: the file's own bytes, and the nested
		// one, through the guest's shell.
		readme := h.mustExec(primary.ID, "/bin/sh", "-c", "cat /workspace/repo/README.md")
		assert.Contains(t, readme, "# copied in")
		nested := h.mustExec(primary.ID, "/bin/sh", "-c", "cat /workspace/repo/src/deep/note.txt")
		assert.Contains(t, nested, "nested", "a copied tree's subdirectories arrive too")
		mode := h.mustExec(primary.ID, "/bin/sh", "-c", "test -x /workspace/repo/src/main.sh && echo executable")
		assert.Contains(t, mode, "executable", "an executable's mode survives the copy")

		// And out again, so the copy is a copy and not a one-way write.
		out := filepath.Join(h.scratch, "copied-back")
		h.mustRun("sandbox", "cp", primary.ID+":workspace/repo", out)
		back, err := os.ReadFile(filepath.Join(out, "src", "deep", "note.txt"))
		require.NoError(t, err, "the tree copied out has the nested file")
		assert.Equal(t, "nested\n", string(back))
		record(t, "3-cp", "tree in and out: README.md, src/main.sh (0755), src/deep/note.txt")
	})

	// 4 — expose serves an authenticated URL, refuses it without the token, and the
	// listener is gone once the sandbox is. The last half is checked after the removal
	// in criterion 8, which is why the URL is kept on the host.
	t.Run("criterion 4: expose gives an authenticated URL and refuses it without the token", func(t *testing.T) {
		// A server in the guest. Busybox ships httpd in the reference template's
		// userspace, so this needs no package the template lacks — but a template
		// without it must fail this criterion rather than pass it by serving nothing.
		out := h.mustExec(primary.ID, "/bin/sh", "-c",
			"command -v busybox >/dev/null && busybox --list | grep -qx httpd && echo has-busybox-httpd")
		require.Contains(t, out, "has-busybox-httpd",
			"the template ships no busybox httpd, so the preview criterion cannot be served")

		h.mustExec(primary.ID, "/bin/sh", "-c",
			"mkdir -p /tmp/www && printf 'preview-ok\\n' > /tmp/www/index.html && "+
				"(busybox httpd -f -p 8080 -h /tmp/www >/tmp/httpd.log 2>&1 &) && sleep 1 && echo started")

		url := strings.TrimSpace(h.mustRun("sandbox", "expose", primary.ID, "--port", "8080", "-o", "name"))
		require.NotEmpty(t, url, "expose prints the authenticated URL")
		h.exposedURL = url
		t.Logf("the authenticated URL: %s", url)

		status, body := httpGet(t, url)
		assert.Equal(t, http.StatusOK, status, "the URL's own token is accepted: %s", body)
		assert.Contains(t, body, "preview-ok", "the guest's bytes arrive through the preview")

		// The same address, without the token: refused.
		plain := strings.TrimSpace(strings.SplitN(url, "?", 2)[0])
		h.exposedPlainURL = plain
		status, body = httpGet(t, plain)
		assert.Equal(t, http.StatusUnauthorized, status, "a request with no token is 401, not the guest's page: %s", body)
		record(t, "4-expose", "200 with the token, 401 without it, URL %s", plain)
	})

	// 5 — a sandbox with egress none cannot resolve a name, reach another sandbox, the
	// host's gateway, or the internet. Four assertions, four different destinations.
	t.Run("criterion 5: egress none reaches nothing", func(t *testing.T) {
		// The second sandbox is the "another sandbox" destination, with a listener of
		// its own that answers the control probe.
		other := h.create("mvp-other", "--owner", "acceptance")
		require.NotEqual(t, primary.GuestIP, other.GuestIP, "two sandboxes, two addresses")
		h.mustExec(other.ID, "/bin/sh", "-c",
			"mkdir -p /tmp/www && printf 'reachable\\n' > /tmp/www/index.html && "+
				"(busybox httpd -f -p 8080 -h /tmp/www >/tmp/httpd.log 2>&1 &) && sleep 1 && echo started")

		// The control: the server answers inside its own guest, so a failure from the
		// other sandbox is a policy decision rather than a service that is not there.
		control := h.mustExec(other.ID, "/bin/sh", "-c",
			"printf 'GET / HTTP/1.0\\r\\n\\r\\n' | busybox nc -w 3 127.0.0.1 8080 | head -1")
		assert.Contains(t, control, "200", "the destination serves its own loopback")

		// 5a — DNS: the sandbox's own resolver cannot answer a public name. The probe
		// needs a resolver client in the template, and a template without one would let
		// this pass by asking nothing: the run says so and fails instead, because a
		// criterion that was not measured is not a criterion that held.
		const dnsProbe = `if command -v getent >/dev/null 2>&1; then
  getent hosts example.com; echo "getent-exit=$?"
elif command -v nslookup >/dev/null 2>&1; then
  nslookup example.com 2>&1; echo "nslookup-exit=$?"
elif command -v curl >/dev/null 2>&1; then
  curl -sS -m 3 -o /dev/null http://example.com 2>&1; echo "curl-exit=$?"
else
  echo "no-resolver-tool"
fi`
		dnsOut, _ := h.exec(primary.ID, "/bin/sh", "-c", dnsProbe)
		t.Logf("the DNS probe:\n%s", dnsOut)
		require.NotContains(t, dnsOut, "no-resolver-tool",
			"the template ships no DNS client (getent, nslookup, curl), so this criterion could not be measured")
		// An answer looks like "93.184.216.34  example.com" (getent) or an "Address:"
		// line (nslookup); the traffic to a resolver is dropped, so there is neither.
		assert.NotContains(t, dnsOut, "Address:", "no name resolves through the sandbox's own resolver")
		assert.NotContains(t, dnsOut, "example.com has address", "and no answer arrives at all")
		assert.Contains(t, dnsOut, "exit=", "the probe ran and reported an outcome")
		assert.NotContains(t, dnsOut, "exit=0", "the name did not resolve: %s", tail(dnsOut))

		// 5b — another sandbox: its address and port are unreachable.
		peer := fmt.Sprintf(
			"/bin/sh -c 'printf \"GET / HTTP/1.0\\r\\n\\r\\n\" | busybox nc -w 3 %s 8080; echo \"exit=$?\"'",
			other.GuestIP)
		peerOut, _ := h.exec(primary.ID, peer)
		assert.Contains(t, peerOut, "exit=", "the attempt has an outcome to read")
		assert.NotContains(t, peerOut, "reachable", "another sandbox's service is not reachable")
		assert.NotContains(t, peerOut, "exit=0", "and no connection to it is established")

		// 5c — the host's own address: the gateway of the sandbox's /30, and the node's
		// address on the LAN, neither of which may answer.
		for _, target := range []string{gatewayOf(t, primary), nodeAddress(t)} {
			out, _ := h.exec(primary.ID, "/bin/sh", "-c",
				fmt.Sprintf("ping -c 2 -w 3 %s 2>&1; echo \"exit=$?\"", target))
			assert.Contains(t, out, "100%% packet loss", "the host at %s must not answer a ping", target)
			assert.NotContains(t, out, "bytes from", "and nothing comes back from %s", target)
		}

		// 5d — the internet: a public address, by number so no name is involved.
		netOut, _ := h.exec(primary.ID, "/bin/sh", "-c",
			"ping -c 2 -w 3 1.1.1.1 2>&1; echo \"exit=$?\"")
		assert.Contains(t, netOut, "100% packet loss", "the internet is not reachable")
		assert.NotContains(t, netOut, "bytes from")

		record(t, "5-egress-none", "no DNS answer, peer %s unreachable, host unreachable, 1.1.1.1 unreachable",
			other.GuestIP)

		// The second sandbox is the fork criterion's fuel, so it is removed here: the
		// checkpoint subtest takes its own image of the primary.
		h.remove(other.ID)
	})

	// 6 — checkpoint and fork --count 2 give three independent sandboxes: distinct
	// namespaces and addresses, and a write to the parent after the image was taken
	// does not appear in its clones.
	t.Run("criterion 6: checkpoint plus fork of two yields three independent sandboxes", func(t *testing.T) {
		h.mustExec(primary.ID, "/bin/sh", "-c", "echo before-the-image > /workspace/before.txt")
		h.mustRun("sandbox", "checkpoint", "create", primary.ID, "mvp-image")

		// Written after the image: it must be in the parent and in neither clone, which
		// is what "writes do not leak" means in both directions.
		h.mustExec(primary.ID, "/bin/sh", "-c", "echo after-the-image > /workspace/after.txt")

		// `fork --checkpoint` still takes the navigation argument the command requires;
		// with --checkpoint it is not read, so the parent's id is what names it.
		out := h.mustRun("sandbox", "fork", primary.ID, "--checkpoint", "mvp-image", "--count", "2", "-o", "name")
		clones := strings.Fields(out)
		require.Len(t, clones, 2, "fork --count 2 creates two clones, got %q", out)

		all := []string{primary.ID}
		all = append(all, clones...)
		require.Len(t, all, 3, "three sandboxes: the parent and its two clones")

		seen := map[string]string{}
		for _, id := range all {
			rec := h.get(id)
			t.Logf("sandbox %s: ip=%s mac=%s template=%s", id, rec.GuestIP, rec.MAC, rec.Template)
			seen[rec.GuestIP] = id
			assert.NotEmpty(t, rec.GuestIP, "%s has an address", id)
		}
		assert.Len(t, seen, 3, "three sandboxes, three distinct guest addresses: %v", seen)

		// Distinct namespaces on the node, read from the node rather than the records.
		namespaces := hostCommand(t, "the sandboxes' namespaces", "/bin/sh", "-c",
			"ip netns list | awk '{print $1}' | grep '^sbx-mvp-' | sort")
		for _, id := range all {
			assert.Contains(t, namespaces, "sbx-"+id, "%s has its own namespace", id)
		}

		// The image's file is in all three; the later write is only in the parent.
		for _, id := range clones {
			assert.Contains(t, h.mustExec(id, "/bin/sh", "-c", "cat /workspace/before.txt"), "before-the-image",
				"clone %s resumes the image's files", id)
			_, code := h.exec(id, "/bin/sh", "-c", "test -e /workspace/after.txt")
			assert.NotZero(t, code, "a write made after the image is not in clone %s", id)
		}
		assert.Contains(t, h.mustExec(primary.ID, "/bin/sh", "-c", "cat /workspace/after.txt"), "after-the-image",
			"and the parent still has its own later write")

		record(t, "6-checkpoint-fork", "3 sandboxes %v with distinct addresses %v, image file present in clones, later write absent",
			all, seen)

		for _, id := range clones {
			h.remove(id)
		}
		h.mustRun("sandbox", "checkpoint", "rm", "mvp-image")
	})

	// 8 — rm frees every resource the record names, and the node has none of them
	// afterwards. This runs last so it removes the primary sandbox for good.
	t.Run("criterion 8: rm frees process, jail, netns, tap, cgroup, disk, token and record", func(t *testing.T) {
		// A credential scoped to the sandbox exists before the removal, so "the tokens
		// went with it" is a fact rather than an absence.
		h.mustRun("sandbox", "token", primary.ID, "--name", "acceptance-8")

		disk := filepath.Join(h.rootfsDir, "sandboxes", primary.ID+".ext4")
		require.FileExists(t, disk, "the sandbox's disk is where the record says")

		doc := h.remove(primary.ID)
		// Everything the response names is what the teardown actually freed.
		for name, freed := range map[string]bool{
			"process stopped":      doc.Freed.ProcessStopped,
			"socket removed":       doc.Freed.SocketRemoved,
			"cgroup removed":       doc.Freed.CgroupRemoved,
			"jail removed":         doc.Freed.JailRemoved,
			"netns removed":        doc.Freed.NetNSRemoved,
			"tap removed":          doc.Freed.TAPRemoved,
			"disk removed":         doc.Freed.DiskRemoved,
			"agent token removed":  doc.Freed.AgentCredentialRemoved,
			"instance record gone": doc.Freed.InstanceRecordRemoved,
			"sandbox record gone":  doc.Freed.RecordRemoved,
		} {
			assert.True(t, freed, "%s must be reported as freed", name)
		}
		assert.Positive(t, doc.Freed.APITokensRevoked, "the token scoped to this sandbox was revoked")

		// The node's own answer, not the report's.
		assert.NotContains(t, hostCommand(t, "namespaces after the removal", "/bin/sh", "-c", "ip netns list"),
			"sbx-"+primary.ID, "the namespace is gone from the node")
		assert.NoFileExists(t, disk, "the disk is gone")
		assert.NotContains(t, hostCommand(t, "processes after the removal", "pgrep", "-af", "firecracker"),
			primary.ID, "no VMM of this sandbox is left")
		assert.NotContains(t, h.mustRun("sandbox", "ls"), primary.ID, "and the node lists no such sandbox")

		// The preview the criterion exposed is dead now, which is the rest of criterion
		// 4: an exposure dies with its sandbox, and nothing is listening on its address.
		require.NotEmpty(t, h.exposedURL, "criterion 4 ran before this one and kept the URL")
		status, _ := httpGet(t, h.exposedPlainURL)
		assert.NotEqual(t, http.StatusOK, status, "the exposure's address does not serve anything after the removal")
		assert.NotContains(t, previewListeners(t), strings.TrimPrefix(h.exposedPlainURL, "http://"),
			"and the node has no listener on it")

		record(t, "8-rm", "every freed flag true, %d tokens revoked, no netns/disk/VMM/listener left",
			doc.Freed.APITokensRevoked)
	})

	// 9 — a token bound to one sandbox may exec in it, and is refused the node's list,
	// another sandbox, and minting.
	t.Run("criterion 9: a sandbox token cannot exec elsewhere, list, or mint", func(t *testing.T) {
		mine := h.create("mvp-bound-a", "--owner", "acceptance")
		theirs := h.create("mvp-bound-b", "--owner", "acceptance")
		t.Cleanup(func() {
			h.remove(mine.ID)
			h.remove(theirs.ID)
		})

		minted := h.mustRun("sandbox", "token", mine.ID, "--name", "acceptance-9", "-o", "json")
		var token struct {
			Secret  string   `json:"secret"`
			Sandbox string   `json:"sandbox"`
			Scopes  []string `json:"scopes"`
		}
		require.NoError(t, json.Unmarshal([]byte(minted), &token), minted)
		require.Equal(t, mine.ID, token.Sandbox)
		t.Logf("minted %v for %s", token.Scopes, token.Sandbox)

		// The token goes to the CLI through a file, never on its argument list: an
		// argument is a process listing.
		tokenFile := filepath.Join(h.scratch, "sandbox-token")
		require.NoError(t, os.WriteFile(tokenFile, []byte(token.Secret+"\n"), 0o600))
		remote := func(args ...string) (string, string, int) {
			full := append([]string{}, args...)
			full = append(full, "--host", "https://"+h.apiAddr, "--ca", h.caFile, "--token-file", tokenFile)
			return h.run(full...)
		}

		// Its own sandbox: allowed.
		out, stderr, code := remote("sandbox", "exec", mine.ID, "--", "/bin/sh", "-c", "echo own-sandbox")
		require.Zero(t, code, "a token may exec in its own sandbox:\n%s%s", out, stderr)
		assert.Contains(t, out, "own-sandbox")

		// The three refusals are read as documents: the reason and the numbers are in
		// the error's details, which the CLI prints in JSON mode and a person sees as
		// prose otherwise. A criterion about details.reason has to read the details.
		refusal := func(args ...string) errorDocument {
			t.Helper()
			out, stderr, code := remote(append(args, "-o", "json")...)
			require.NotZero(t, code, "this call must be refused: %v", args)
			var doc errorDocument
			require.NoError(t, json.Unmarshal([]byte(firstJSON(stderr+out)), &doc),
				"the refusal is an Error document:\n%s%s", out, stderr)
			return doc
		}

		// Another sandbox: refused, with the reason and the id it asked about.
		doc := refusal("sandbox", "exec", theirs.ID, "--", "/bin/sh", "-c", "echo elsewhere")
		assert.Equal(t, "forbidden", doc.Error.Code)
		assert.Equal(t, "sandbox_selector_mismatch", doc.Error.Details["reason"])
		assert.Equal(t, theirs.ID, doc.Error.Details["sandboxId"], "the refusal names the sandbox it was asked about")

		// The node's inventory: refused, because it is every tenant's sandboxes.
		doc = refusal("sandbox", "ls")
		assert.Equal(t, "forbidden", doc.Error.Code)
		assert.Equal(t, "sandbox_list_not_permitted", doc.Error.Details["reason"])

		// Minting: refused for the scope it lacks.
		doc = refusal("sandbox", "token", mine.ID, "--name", "escalated")
		assert.Equal(t, "forbidden", doc.Error.Code)
		assert.Equal(t, "scope_missing", doc.Error.Details["reason"])
		assert.Equal(t, "sandboxes:token", doc.Error.Details["requiredScope"],
			"the refusal names the scope it would have needed")

		record(t, "9-scoped-token", "own exec ok; elsewhere sandbox_selector_mismatch; list sandbox_list_not_permitted; mint scope_missing")
	})

	// 11 — the template this node boots reports its agent's protocol and capabilities,
	// and a template that records no working agent is refused by this host.
	t.Run("criterion 11: the template's agent reports protocol and caps, and one without is refused", func(t *testing.T) {
		inspected := h.mustRun("template", "inspect", h.template, "--output-dir", h.goldenDir)
		t.Logf("template inspect:\n%s", inspected)
		assert.Contains(t, inspected, "protocol", "the manifest records the agent protocol")
		assert.Contains(t, inspected, "caps [", "and the capabilities it advertises")
		assert.Contains(t, inspected, "prepare", "including the post-restore hook, which this host requires")

		// The create's own answer is the guest's hello, not the manifest: the agent
		// version and boot id come from the running guest.
		one := h.create("mvp-hello", "--owner", "acceptance")
		t.Cleanup(func() { h.remove(one.ID) })
		assert.NotEmpty(t, one.AgentVersion, "the sandbox reports the agent version its guest answered with")
		assert.NotEmpty(t, one.BootID, "and this boot's identity")
		t.Logf("the guest's hello: agent %s, boot %s", one.AgentVersion, one.BootID)

		// The negative half: a manifest that records no agent capabilities, in a store
		// of its own, is refused by this host rather than booted. It is the same
		// golden.CheckAgentPin that create's template resolution runs, asked directly,
		// so a host that would boot it says "usable" here.
		broken := filepath.Join(h.scratch, "broken-store")
		require.NoError(t, os.MkdirAll(broken, 0o755))
		copyTemplateWithoutAgent(t, h.goldenDir, broken, "broken-agent")
		out, stderr, _ := h.run("template", "inspect", "broken-agent", "--output-dir", broken)
		verdict := out + stderr
		t.Logf("the verdict on a template with no agent:\n%s", verdict)
		assert.Contains(t, verdict, "NOT usable here", "a template with no agent is refused, not used")
		assert.NotContains(t, verdict, "usable on this node", "and it is not also reported as usable")

		// What this run does not exercise: `template build` refusing a recipe whose
		// guest agent never comes up. That is a build, which needs a container runtime
		// and the recipe's OCI base image, so it belongs to a build job rather than to
		// this one; the check it fails is the CheckAgentPin asked above.
		t.Logf("the build-path half of this criterion is not exercised here: it needs a container runtime")
		record(t, "11-template-agent", "manifest protocol+caps+prepare; guest hello %s/%s; a template with no agent caps is NOT usable here",
			one.AgentVersion, one.BootID)
	})

	// 12 — a non-root process in the guest cannot obtain the credential the agent
	// requires, and a host dial without it is rejected.
	t.Run("criterion 12: a non-root guest process cannot become root, and an unauthenticated dial is refused", func(t *testing.T) {
		one := h.create("mvp-auth", "--owner", "acceptance")
		t.Cleanup(func() { h.remove(one.ID) })

		// The template's non-root user, and the credential the agent authenticates with.
		// A process that cannot read it cannot present it.
		out, code := h.exec(one.ID, "/bin/sh", "-c",
			"su -s /bin/sh sandbox -c 'cat "+agent.TokenPath+"' 2>&1; echo \"exit=$?\"")
		t.Logf("a non-root read of the agent credential:\n%s", out)
		assert.Contains(t, out, "exit=", "the attempt has an outcome")
		assert.NotContains(t, out, "exit=0", "the sandbox's own user cannot read the agent credential")
		_ = code

		// The host side of the same rule: the muxer refuses a handshake with no token,
		// and accepts the one this node minted. The two halves together are what makes
		// the credential load-bearing rather than decorative.
		vsock, hostToken := sandboxMuxer(t, h, one.ID)
		if vsock == "" {
			t.Skip("this node's instance registry names no vsock path, so the host-side dial cannot be aimed")
		}

		dialContext, cancel := contextWithTimeout(10 * time.Second)
		defer cancel()
		_, err := agent.DialWithToken(dialContext, vsock, agent.DefaultPort, "")
		require.Error(t, err, "a host dial with no token must be refused")
		t.Logf("the unauthenticated dial: %v", err)
		assert.True(t, isAuthRefusal(err), "the refusal is about the credential, not the socket: %v", err)

		client, err := agent.DialWithToken(dialContext, vsock, agent.DefaultPort, hostToken)
		require.NoError(t, err, "the token this node minted is accepted")
		t.Logf("the guest's hello with the minted token: %+v", client.Hello())
		require.NoError(t, client.Close())

		record(t, "12-auth", "sandbox user cannot read %s; a tokenless host dial is refused (%v); the minted token is accepted",
			agent.TokenPath, err)
	})

	// The host is left as the run found it, plus nothing: every sandbox this run made
	// is gone, and so is everything they held. This is the run's own promise, not a
	// claim that the host has no leftovers from earlier work.
	t.Run("the run leaves nothing behind", func(t *testing.T) {
		assert.Empty(t, h.list(), "no sandbox this run created survives it")

		after := hostFootprint(t)
		for _, leftover := range after.beyond(before) {
			assert.Fail(t, "the run left something on the host", leftover)
		}
		record(t, "clean", "no sandbox, namespace, listener or VMM of this run remains")
	})
}

// hostFootprint is what the node holds of this run's kind: namespaces named after a
// sandbox, listeners of a preview, and VMM processes. It is taken before and after so
// a leftover of this run is distinguishable from a leftover of somebody else's.
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

// beyond is what appears in after that was not in before, as readable lines.
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

// errorDocument is the CLI's JSON rendering of a failed call: one Error document on
// stderr when the output format is json.
type errorDocument struct {
	SchemaVersion int    `json:"schemaVersion"`
	Kind          string `json:"kind"`
	Error         struct {
		Code      string         `json:"code"`
		Message   string         `json:"message"`
		Details   map[string]any `json:"details,omitempty"`
		Retryable bool           `json:"retryable,omitempty"`
	} `json:"error"`
}

// firstJSON finds the first complete JSON object in out: the CLI writes its document
// to stderr and a command may have written its own lines to stdout, so the document is
// located rather than assumed to be the whole text.
func firstJSON(out string) string {
	start := strings.Index(out, "{")
	if start < 0 {
		return out
	}
	depth := 0
	for i := start; i < len(out); i++ {
		switch out[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return out[start : i+1]
			}
		}
	}
	return out[start:]
}

// tail is the last few lines of a command's output, for a message that stays readable.
func tail(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) > 4 {
		lines = lines[len(lines)-4:]
	}
	return strings.Join(lines, " | ")
}
