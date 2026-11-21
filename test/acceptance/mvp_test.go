//go:build acceptance

package acceptance

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/golden"
)

// TestMVP is the epic's MVP acceptance run on one node: one operator, one sandbox,
// and the whole cycle — create, exec, cp, expose, checkpoint, fork, rm — with the
// security rules that make handing a sandbox to an agent defensible.
//
// # Every criterion stands alone
//
// Each criterion is its own subtest, creates whatever it needs, and records its own
// outcome (PASS, FAIL, or "not evaluated" with the reason) in the run's evidence file.
// A red criterion does not stop the ones after it: the first run of this package lost
// seven criteria because an assertion in the third aborted the test, and a run that
// reports one failure and hides seven is worse than no run at all.
//
// The whole test fails if any criterion failed, and also if any criterion could not be
// evaluated — an unmeasured criterion is not a criterion that held.
func TestMVP(t *testing.T) {
	h := newHost(t)
	before := hostFootprint(t)

	// 1 — a cold create is ready inside the window the node's own timestamps measure,
	// and the response reports them. The warm half of the criterion is a pool, which
	// this slice does not have, so it is not measured and not claimed.
	criterion(t, h, "1-cold-create", "cold create is ready within four seconds and reports its timings", func(t *testing.T) {
		started := time.Now()
		rec := h.create("mvp-create")
		elapsed := time.Since(started)

		created, err := time.Parse(time.RFC3339Nano, rec.CreatedAt)
		require.NoError(t, err, "createdAt: %q", rec.CreatedAt)
		ready, err := time.Parse(time.RFC3339Nano, rec.ReadyAt)
		require.NoError(t, err, "readyAt: %q", rec.ReadyAt)
		window := ready.Sub(created)
		t.Logf("the node's own ready window: created %s, ready %s, window %s (this command waited %s)",
			rec.CreatedAt, rec.ReadyAt, window, elapsed.Round(time.Millisecond))

		// The measurement is recorded whether or not it passes the threshold: the epic
		// wrote ≤4 s for other hardware, and a host that is slower is a fact the owner
		// decides about, not something this test may raise the bar for.
		note(t, "cold create ready in %s by the node's own window (%s by this command)", window, elapsed.Round(time.Millisecond))
		assert.Positive(t, window, "ready must be after created")
		assert.LessOrEqual(t, window, createDeadline,
			"a cold create must be ready within %s; this one took %s", createDeadline, window)

		h.remove(rec.ID)
	})

	// 2 — exec returns stdout, stderr and the guest's exit code, separately.
	criterion(t, h, "2-exec", "exec returns stdout, stderr and the exit code", func(t *testing.T) {
		rec := h.create("mvp-exec")
		t.Cleanup(func() { h.remove(rec.ID) })

		stdout, stderr, code := h.run("sandbox", "exec", rec.ID, "--", "/bin/sh", "-c",
			"echo out-line; echo err-line >&2; exit 7")
		note(t, "stdout %q, stderr %q, exit %d", strings.TrimSpace(stdout), strings.TrimSpace(stderr), code)
		assert.Equal(t, 7, code, "the guest's status is this command's status")
		assert.Contains(t, stdout, "out-line", "stdout arrives")
		assert.Contains(t, stderr, "err-line", "stderr arrives on stderr")
		assert.NotContains(t, stdout, "err-line", "and the two streams are not merged")
	})

	// 3 — a tree copied in is visible to exec, with its modes and its nesting, and it
	// lands where the path says and nowhere else.
	//
	// The path rule is the one the CLI documents: the guest path is relative to the
	// workspace root and nothing is prepended, so "repo" is /workspace/repo and
	// "workspace/repo" would be /workspace/workspace/repo. Both halves are asserted,
	// because a copy that landed in the wrong one of the two would otherwise pass.
	criterion(t, h, "3-cp", "cp moves a tree into the workspace and exec sees it", func(t *testing.T) {
		rec := h.create("mvp-cp")
		t.Cleanup(func() { h.remove(rec.ID) })

		tree := filepath.Join(h.scratch, "repo")
		require.NoError(t, os.MkdirAll(filepath.Join(tree, "src", "deep"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(tree, "README.md"), []byte("# copied in\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(tree, "src", "main.sh"), []byte("echo from-the-tree\n"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(tree, "src", "deep", "note.txt"), []byte("nested\n"), 0o644))

		h.mustRun("sandbox", "cp", tree, rec.ID+":repo")

		readme := h.mustExec(rec.ID, "/bin/sh", "-c", "cat /workspace/repo/README.md")
		assert.Contains(t, readme, "# copied in", "the tree landed at /workspace/repo")
		nested := h.mustExec(rec.ID, "/bin/sh", "-c", "cat /workspace/repo/src/deep/note.txt")
		assert.Contains(t, nested, "nested", "a copied tree's subdirectories arrive too")
		mode := h.mustExec(rec.ID, "/bin/sh", "-c", "test -x /workspace/repo/src/main.sh && echo executable")
		assert.Contains(t, mode, "executable", "an executable's mode survives the copy")

		// Nowhere else: the one place a doubled path would land, and the guest's root.
		_, code := h.exec(rec.ID, "/bin/sh", "-c", "test -e /workspace/workspace")
		assert.NotZero(t, code, "the tree must not land at /workspace/workspace/<path>")
		_, code = h.exec(rec.ID, "/bin/sh", "-c", "test -e /repo")
		assert.NotZero(t, code, "nor outside the workspace")

		// And out again, so the copy is a copy and not a one-way write.
		out := filepath.Join(h.scratch, "copied-back")
		h.mustRun("sandbox", "cp", rec.ID+":repo", out)
		back, err := os.ReadFile(filepath.Join(out, "src", "deep", "note.txt"))
		require.NoError(t, err, "the tree copied out has the nested file")
		assert.Equal(t, "nested\n", string(back))

		note(t, "tree in and out at /workspace/repo: README.md, src/main.sh (0755), src/deep/note.txt; /workspace/workspace absent")
	})

	// 3b — the same copy against a template whose agent predates the rooted capability.
	//
	// This is the case that makes the capability gate load-bearing rather than tidy: an
	// older agent unmarshals `rooted` and ignores it, so without the gate the host would
	// send a confined copy to an agent that writes the path as given — the hole #210
	// closes, reopened through a template that was never rebuilt. The assertion is not
	// "an error came back": it is that the refused copy wrote nothing, checked in the
	// guest.
	criterion(t, h, "3b-cp-old-template", "a template older than the rooted capability refuses cp and writes nothing", func(t *testing.T) {
		// The subject is whichever older template this host actually has — named, or found
		// in its stores — and the run records which one it used. A host with none is a
		// finding, not a skip: the case this criterion exists for is a template nobody
		// rebuilt, and a host that has no such template has not measured it.
		older, source, err := h.olderTemplate(t)
		require.NoError(t, err, "this criterion needs a template whose agent predates the rooted capability")
		t.Logf("the older template: %s", source)
		note(t, "the older template is %s", source)

		rec := h.createFrom("mvp-old-cp", older)
		t.Cleanup(func() { h.remove(rec.ID) })

		// Its agent must really lack the capability, or the rest proves nothing about an
		// old agent: a template that has it is not this criterion's subject. (A template
		// named by the operator could be the wrong one; a found one already passed this
		// check against its manifest, but a manifest can be stale about the guest.)
		inspected := h.mustRun("template", "inspect", older, "--output-dir", h.goldenDir)
		if strings.Contains(inspected, "rooted") {
			t.Fatalf("the template %s already advertises the rooted capability, so it is not an older template", older)
		}

		local := filepath.Join(h.scratch, "old-copy.txt")
		require.NoError(t, os.WriteFile(local, []byte("must not land\n"), 0o644))

		// The refusal is the capability, by name, and it is per call: the sandbox itself
		// is fine.
		out, stderr, code := h.run("sandbox", "cp", local, rec.ID+":older-copy.txt", "-o", "json")
		require.NotZero(t, code, "a rooted copy to an agent without the capability must be refused")
		var doc errorDocument
		require.NoError(t, json.Unmarshal([]byte(firstJSON(stderr+out)), &doc),
			"the refusal is an Error document:\n%s%s", out, stderr)
		assert.Equal(t, string("not_implemented"), doc.Error.Code)
		assert.Equal(t, "template_capability_missing", doc.Error.Details["reason"])
		assert.Equal(t, "rooted", doc.Error.Details["capability"], "the refusal names the capability")

		// Nothing was written anywhere: not at the path, not at the doubled path an
		// unconfined write would have used.
		for _, path := range []string{"/workspace/older-copy.txt", "/workspace/workspace/older-copy.txt"} {
			_, execCode := h.exec(rec.ID, "/bin/sh", "-c", "test -e "+path)
			assert.NotZero(t, execCode, "%s must not exist: the refused copy wrote nothing", path)
		}

		// And the sandbox still works: the refusal is the capability, not a dead sandbox —
		// the unrooted plane reaches it.
		assert.Contains(t, h.mustExec(rec.ID, "/bin/sh", "-c", "echo alive"), "alive")

		note(t, "the older template's cp was refused as template_capability_missing/rooted and wrote nothing; the sandbox still runs")
	})

	// 4 — expose serves an authenticated URL, refuses it without the token, and the
	// listener is gone once the sandbox is.
	criterion(t, h, "4-expose", "expose gives an authenticated URL and dies with its sandbox", func(t *testing.T) {
		rec := h.create("mvp-expose")

		// A server in the guest that needs no package the template lacks: busybox's
		// `nc` answering one request at a time with a fixed response. The reference
		// template ships busybox without the `httpd` applet, so an httpd here would
		// have been a criterion that could not be served.
		h.mustExec(rec.ID, "/bin/sh", "-c", "command -v nc >/dev/null && echo has-nc")
		h.mustExec(rec.ID, "/bin/sh", "-c",
			"(while true; do printf 'HTTP/1.0 200 OK\\r\\nContent-Length: 11\\r\\n\\r\\npreview-ok\\n' | nc -l -p 8080; done) >/tmp/nc.log 2>&1 & sleep 1; echo listening")

		url := strings.TrimSpace(h.mustRun("sandbox", "expose", rec.ID, "--port", "8080", "-o", "name"))
		require.NotEmpty(t, url, "expose prints the authenticated URL")
		plain := strings.TrimSpace(strings.SplitN(url, "?", 2)[0])
		t.Logf("the authenticated URL: %s", url)

		status, body := httpGet(t, url)
		assert.Equal(t, http.StatusOK, status, "the URL's own token is accepted: %s", body)
		assert.Contains(t, body, "preview-ok", "the guest's bytes arrive through the preview")

		status, body = httpGet(t, plain)
		assert.Equal(t, http.StatusUnauthorized, status, "a request with no token is 401, not the guest's page: %s", body)

		// Dead after removal: the listener is a process in the daemon, so this is also
		// a statement about the teardown.
		h.remove(rec.ID)
		status, _ = httpGet(t, plain)
		assert.NotEqual(t, http.StatusOK, status, "the exposure's address serves nothing after the removal")
		assert.NotContains(t, previewListeners(t), strings.TrimPrefix(plain, "http://"),
			"and the node has no listener on it")

		note(t, "200 with the token, 401 without it, and the listener is gone after rm: %s", plain)
	})

	// 5 — a sandbox with egress none cannot resolve a name, reach another sandbox, the
	// host's gateway, or the internet. Four assertions, four destinations.
	criterion(t, h, "5-egress-none", "egress none cannot resolve DNS, reach a peer, the host, or the internet", func(t *testing.T) {
		mine := h.create("mvp-egress-a")
		t.Cleanup(func() { h.remove(mine.ID) })
		peer := h.create("mvp-egress-b")
		t.Cleanup(func() { h.remove(peer.ID) })
		require.NotEqual(t, mine.GuestIP, peer.GuestIP, "two sandboxes, two addresses")

		// The peer is the destination, with a listener of its own; the control proves
		// that listener answers, so a refusal from the other sandbox is policy.
		h.mustExec(peer.ID, "/bin/sh", "-c",
			"(while true; do printf 'reachable\\n' | nc -l -p 8080; done) >/tmp/nc.log 2>&1 & sleep 1; echo listening")
		control, _ := h.exec(peer.ID, "/bin/sh", "-c",
			"printf 'ping\\n' | nc -w 3 127.0.0.1 8080; echo \"exit=$?\"")
		assert.Contains(t, control, "reachable", "the destination answers inside its own guest: %s", control)

		// 5a — DNS. A template with no resolver client fails here rather than passing by
		// asking nothing.
		const dnsProbe = `if command -v getent >/dev/null 2>&1; then
  getent hosts example.com; echo "exit=$?"
elif command -v nslookup >/dev/null 2>&1; then
  nslookup example.com 2>&1; echo "exit=$?"
elif command -v curl >/dev/null 2>&1; then
  curl -sS -m 3 -o /dev/null http://example.com 2>&1; echo "exit=$?"
else
  echo "no-resolver-tool"
fi`
		dnsOut, _ := h.exec(mine.ID, "/bin/sh", "-c", dnsProbe)
		t.Logf("the DNS probe:\n%s", dnsOut)
		require.NotContains(t, dnsOut, "no-resolver-tool",
			"the template ships no DNS client (getent, nslookup, curl), so this criterion could not be measured")
		assert.Contains(t, dnsOut, "exit=", "the probe ran and reported an outcome")
		assert.NotContains(t, dnsOut, "Address:", "no name resolves through the sandbox's own resolver")
		assert.NotContains(t, dnsOut, "example.com has address", "and no answer arrives at all")
		assert.NotContains(t, dnsOut, "exit=0", "the name did not resolve: %s", tail(dnsOut))

		// 5b — another sandbox: its address and port are unreachable.
		peerOut, _ := h.exec(mine.ID, "/bin/sh", "-c",
			fmt.Sprintf("printf 'ping\\n' | nc -w 3 %s 8080; echo \"exit=$?\"", peer.GuestIP))
		assert.Contains(t, peerOut, "exit=", "the attempt has an outcome to read")
		assert.NotContains(t, peerOut, "reachable", "another sandbox's service is not reachable")
		assert.NotContains(t, peerOut, "exit=0", "and no connection to it is established")

		// 5c — the host: the gateway of the sandbox's own /30, and the node's address.
		for _, target := range []string{gatewayOf(t, mine), nodeAddress(t)} {
			out, _ := h.exec(mine.ID, "/bin/sh", "-c",
				fmt.Sprintf("ping -c 2 -w 3 %s 2>&1; echo \"exit=$?\"", target))
			assert.Contains(t, out, "100% packet loss", "the host at %s must not answer a ping", target)
			assert.NotContains(t, out, "bytes from", "and nothing comes back from %s", target)
		}

		// 5d — the internet, by address so no name is involved.
		netOut, _ := h.exec(mine.ID, "/bin/sh", "-c", "ping -c 2 -w 3 1.1.1.1 2>&1; echo \"exit=$?\"")
		assert.Contains(t, netOut, "100% packet loss", "the internet is not reachable")
		assert.NotContains(t, netOut, "bytes from")

		note(t, "no DNS answer; peer %s unreachable; host gateway and node unreachable; 1.1.1.1 unreachable", peer.GuestIP)
	})

	// 6 — checkpoint and fork --count 2 give three independent sandboxes: distinct
	// namespaces and addresses, and writes that do not leak in either direction.
	criterion(t, h, "6-checkpoint-fork", "checkpoint plus fork of two yields three independent sandboxes", func(t *testing.T) {
		parent := h.create("mvp-fork-parent")
		t.Cleanup(func() { h.remove(parent.ID) })

		h.mustExec(parent.ID, "/bin/sh", "-c", "echo before-the-image > /workspace/before.txt")
		h.mustRun("sandbox", "checkpoint", "create", parent.ID, "mvp-image")
		t.Cleanup(func() { h.run("sandbox", "checkpoint", "rm", "mvp-image") })

		// Written after the image: it must be in the parent and in neither clone, which
		// is what "writes do not leak" means in both directions.
		h.mustExec(parent.ID, "/bin/sh", "-c", "echo after-the-image > /workspace/after.txt")

		// `fork` still requires the navigation argument; with --checkpoint it is not
		// read, so the parent's id names it.
		out := h.mustRun("sandbox", "fork", parent.ID, "--checkpoint", "mvp-image", "--count", "2", "-o", "name")
		clones := strings.Fields(out)
		require.Len(t, clones, 2, "fork --count 2 creates two clones, got %q", out)
		for _, id := range clones {
			id := id
			t.Cleanup(func() { h.remove(id) })
		}

		all := append([]string{parent.ID}, clones...)
		addresses := map[string]string{}
		for _, id := range all {
			rec := h.get(id)
			t.Logf("sandbox %s: ip=%s mac=%s template=%s", id, rec.GuestIP, rec.MAC, rec.Template)
			require.NotEmpty(t, rec.GuestIP, "%s has an address", id)
			addresses[rec.GuestIP] = id
		}
		assert.Len(t, addresses, 3, "three sandboxes, three distinct guest addresses: %v", addresses)

		// Each sandbox's own namespace, found by its own name. The clones are named from
		// their own ids, not the name this run gave the parent, so a grep on
		// "sbx-mvp-fork" could only ever match one of the three.
		namespaces := hostCommand(t, "the sandboxes' namespaces", "/bin/sh", "-c",
			"ip netns list | awk '{print $1}' | sort")
		present := map[string]bool{}
		for _, line := range strings.Split(strings.TrimSpace(namespaces), "\n") {
			present[strings.TrimSpace(line)] = true
		}
		ofThisRun := 0
		for _, id := range all {
			assert.True(t, present["sbx-"+id], "%s has a namespace of its own; the host lists:\n%s", id, namespaces)
			if present["sbx-"+id] {
				ofThisRun++
			}
		}
		assert.Equal(t, 3, ofThisRun, "each of the three has a namespace of its own:\n%s", namespaces)

		for _, id := range clones {
			assert.Contains(t, h.mustExec(id, "/bin/sh", "-c", "cat /workspace/before.txt"), "before-the-image",
				"clone %s resumes the image's files", id)
			_, code := h.exec(id, "/bin/sh", "-c", "test -e /workspace/after.txt")
			assert.NotZero(t, code, "a write made after the image is not in clone %s", id)
		}
		assert.Contains(t, h.mustExec(parent.ID, "/bin/sh", "-c", "cat /workspace/after.txt"), "after-the-image",
			"and the parent still has its own later write")

		note(t, "3 sandboxes %v with distinct addresses %v; image file in both clones, later write only in the parent", all, addresses)
	})

	// 8 — rm frees every resource the record names, and the node has none of them.
	criterion(t, h, "8-rm", "rm frees process, jail, netns, tap, cgroup, disk, token and record", func(t *testing.T) {
		rec := h.create("mvp-rm")
		// A credential scoped to the sandbox exists before the removal, so "the tokens
		// went with it" is a fact rather than an absence.
		h.mustRun("sandbox", "token", rec.ID, "--name", "acceptance-8")

		disk := filepath.Join(h.rootfsDir, "sandboxes", rec.ID+".ext4")
		require.FileExists(t, disk, "the sandbox's disk is where the record says")

		doc := h.remove(rec.ID)
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

		assert.NotContains(t, hostCommand(t, "namespaces after the removal", "/bin/sh", "-c", "ip netns list"),
			"sbx-"+rec.ID, "the namespace is gone from the node")
		assert.NoFileExists(t, disk, "the disk is gone")
		assert.NotContains(t, hostCommand(t, "processes after the removal", "/bin/sh", "-c", firecrackerProcesses),
			rec.ID, "no VMM of this sandbox is left")
		assert.NotContains(t, h.mustRun("sandbox", "ls"), rec.ID, "and the node lists no such sandbox")

		note(t, "every freed flag true, %d tokens revoked, no netns/disk/VMM left", doc.Freed.APITokensRevoked)
	})

	// 9 — a token bound to one sandbox may exec in it, and is refused the node's list,
	// another sandbox, and minting.
	//
	// Every CLI flag goes before "--": after it is the guest's argv, and a flag there
	// would be run by the guest while the command itself fell back to the local admin
	// socket — which is exactly how an earlier version of this test passed while
	// proving nothing.
	criterion(t, h, "9-scoped-token", "a sandbox token cannot exec elsewhere, list, or mint", func(t *testing.T) {
		mine := h.create("mvp-bound-a")
		t.Cleanup(func() { h.remove(mine.ID) })
		theirs := h.create("mvp-bound-b")
		t.Cleanup(func() { h.remove(theirs.ID) })

		// The document is the API's own type, decoded: a local copy of its fields is how
		// this criterion came to read "secret" while the wire says "token", which made a
		// correct refusal look correct for the wrong reason (an empty bearer token is
		// refused too).
		minted := h.mustRun("sandbox", "token", mine.ID, "--name", "acceptance-9", "-o", "json")
		var token v1.SandboxToken
		require.NoError(t, json.Unmarshal([]byte(minted), &token), minted)
		require.Equal(t, mine.ID, token.Sandbox)
		require.NotEmpty(t, token.Secret, "the minted document carries its bearer secret:\n%s", minted)
		t.Logf("minted %v for %s", token.Scopes, token.Sandbox)

		// The token reaches the CLI through a file, never an argument.
		tokenFile := filepath.Join(h.scratch, "sandbox-token")
		require.NoError(t, os.WriteFile(tokenFile, []byte(token.Secret+"\n"), 0o600))
		// The token's own flags, and the one composer that places them: a call that wants a
		// document rather than prose adds the format here, so it lands on the CLI's side of
		// the separator with everything else.
		tokenFlags := []string{"--host", "https://" + h.apiAddr, "--ca", h.caFile, "--token-file", tokenFile}
		remote := func(args ...string) (string, string, int) {
			return h.run(withCLIFlags(args, tokenFlags...)...)
		}
		asDocument := func(args ...string) (string, string, int) {
			t.Helper()
			flags := append(append([]string{}, tokenFlags...), "-o", "json")
			return h.run(withCLIFlags(args, flags...)...)
		}

		// Its own sandbox: allowed.
		out, stderr, code := remote("sandbox", "exec", mine.ID, "--", "/bin/sh", "-c", "echo own-sandbox")
		require.Zero(t, code, "a token may exec in its own sandbox:\n%s%s", out, stderr)
		assert.Contains(t, out, "own-sandbox")

		// The three refusals are read as documents: a criterion about details.reason
		// has to read details. The format is requested through withCLIFlags like every
		// other flag — appending "-o json" to a command that already ends in "--" and the
		// guest's argv is exactly how this criterion passed on prose, three runs running.
		// A refusal counts as observed only as a JSON error document naming its code and
		// reason. An exit code is not that: a command that failed for an unrelated reason —
		// a flag on the wrong side of the separator, a usage error, a subcommand that does
		// not exist — has an exit code too, and reading it as a refusal is how this
		// criterion passed three times on an answer it never received.
		observedRefusals := 0
		refusal := func(args ...string) errorDocument {
			t.Helper()
			out, stderr, code := asDocument(args...)
			raw := firstJSON(stderr + out)
			require.NotZero(t, code, "this call must be refused: %v", args)
			require.NotContains(t, stderr+out, "invalid character",
				"the refusal is a document this test can read, not prose the CLI printed instead of one — "+
					"a format flag on the guest's side of \"--\" does that:\n%s%s", out, stderr)
			var doc errorDocument
			require.NoError(t, json.Unmarshal([]byte(raw), &doc), "the refusal is an Error document:\n%s%s", out, stderr)
			require.Equal(t, v1.KindError, doc.Kind, "and it is the error envelope, not another document:\n%s", raw)
			require.NotEmpty(t, doc.Error.Code, "the refusal names a stable code:\n%s", raw)
			require.NotEmpty(t, doc.Error.Details["reason"], "and a reason in details:\n%s", raw)
			observedRefusals++
			return doc
		}

		doc := refusal("sandbox", "exec", theirs.ID, "--", "/bin/sh", "-c", "echo elsewhere")
		assert.Equal(t, "forbidden", doc.Error.Code)
		assert.Equal(t, "sandbox_selector_mismatch", doc.Error.Details["reason"])
		assert.Equal(t, theirs.ID, doc.Error.Details["sandboxId"], "the refusal names the sandbox it was asked about")

		doc = refusal("sandbox", "ls")
		assert.Equal(t, "forbidden", doc.Error.Code)
		assert.Equal(t, "sandbox_list_not_permitted", doc.Error.Details["reason"])

		doc = refusal("sandbox", "token", mine.ID, "--name", "escalated")
		assert.Equal(t, "forbidden", doc.Error.Code)
		assert.Equal(t, "scope_missing", doc.Error.Details["reason"])
		assert.Equal(t, "sandboxes:token", doc.Error.Details["requiredScope"],
			"the refusal names the scope it would have needed")

		// And the boundary itself: a flag after "--" is the guest's, not the CLI's. This
		// is the ordering the test above depends on, pinned so a later edit that moves
		// the flags cannot quietly make the refusals above pass on the admin socket.
		out, stderr, code = h.run("sandbox", "exec", mine.ID, "--", "echo", "--host")
		require.Zero(t, code, "the guest runs the argv after --: %s%s", out, stderr)
		assert.Contains(t, strings.TrimSpace(out), "--host", "an argument after -- reaches the guest")

		// The criterion's claim is that all three boundaries were observed, so it is the
		// count that is asserted, not each call on its own honour.
		assert.Equal(t, 3, observedRefusals, "all three refusals arrived as error documents")
		note(t, "own exec ok; %d refusals observed as documents: elsewhere sandbox_selector_mismatch+%s, list sandbox_list_not_permitted, mint scope_missing+sandboxes:token",
			observedRefusals, theirs.ID)
	})

	// 11 — the template this node boots reports its agent's protocol and capabilities,
	// a create's answer is the guest's own hello, and a template recording no working
	// agent is refused by this host.
	criterion(t, h, "11-template-agent", "the template's agent reports protocol and caps, and one without is refused", func(t *testing.T) {
		ref, err := h.templateRef(t)
		require.NoError(t, err, "this run needs to know which template it boots")
		t.Logf("the template this run boots: %s", ref)

		inspected := h.mustRun("template", "inspect", ref, "--output-dir", h.goldenDir)
		t.Logf("template inspect:\n%s", inspected)
		assert.Contains(t, inspected, "usable on this node", "this node can boot the template it is about to boot")
		assert.Contains(t, inspected, "caps [", "and the manifest records the capabilities its agent advertised")
		assert.Contains(t, inspected, "prepare", "including the post-restore hook, which this host requires")

		// The criterion's claim is that the manifest and the guest agree, so both sides
		// are read and compared. A substring of the prose is not that: `template inspect`
		// prints the JSON keys too, and "protocol" appears in "agentProtocol", so a
		// contains() would pass on a manifest that recorded nothing at all.
		manifest := readTemplateManifest(t, h, ref)

		// The create's own answer is the guest's hello, not the manifest.
		rec := h.create("mvp-hello")
		t.Cleanup(func() { h.remove(rec.ID) })
		assert.NotEmpty(t, rec.AgentVersion, "the sandbox reports the agent version its guest answered with")
		assert.NotEmpty(t, rec.BootID, "and this boot's identity")
		t.Logf("the guest's hello: agent %s, boot %s", rec.AgentVersion, rec.BootID)

		// What the guest says about itself, asked directly rather than through a record the
		// node wrote: the node already resolved the template's agent when it created the
		// sandbox, so a record that agreed with the manifest would prove nothing about the
		// guest.
		vsock, hostToken := sandboxMuxer(t, h, rec.ID)
		dialContext, cancel := contextWithTimeout(10 * time.Second)
		defer cancel()
		client, err := agent.DialWithToken(dialContext, vsock, agent.DefaultPort, hostToken)
		require.NoError(t, err, "dialing the guest's agent")
		hello := client.Hello()
		require.NoError(t, client.Close())
		t.Logf("the guest's own hello: protocol %s, version %s, caps %v", hello.Version, hello.AgentVersion, hello.Caps)

		assert.Equal(t, manifest.Agent.Protocol, hello.Version,
			"the protocol the manifest records is the one the guest answers with")
		assert.Equal(t, manifest.Agent.Version, hello.AgentVersion,
			"and so is the agent version")
		for _, cap := range golden.RequiredAgentCaps {
			assert.Contains(t, manifest.Agent.Caps, cap,
				"the manifest records the capability this host requires (%s)", cap)
			assert.Contains(t, hello.Caps, cap,
				"and the guest advertises it", cap)
		}
		assert.Equal(t, rec.AgentVersion, hello.AgentVersion,
			"the create's record names the agent the guest answered with")

		// The negative half: a manifest that records no agent capabilities is refused by
		// this host rather than booted. It is the same golden.CheckAgentPin that a
		// create's template resolution runs, asked directly.
		broken := filepath.Join(h.scratch, "broken-store")
		require.NoError(t, os.MkdirAll(broken, 0o755))
		copyTemplateWithoutAgent(t, h.goldenDir, broken, "broken-agent")
		out, stderr, _ := h.run("template", "inspect", "broken-agent@1.0.0", "--output-dir", broken)
		verdict := out + stderr
		t.Logf("the verdict on a template with no agent:\n%s", verdict)
		assert.Contains(t, verdict, "NOT usable here", "a template with no agent is refused, not used")
		assert.NotContains(t, verdict, "usable on this node", "and it is not also reported as usable")

		// What this run does not exercise: `template build` refusing a recipe whose
		// guest agent never comes up. That is a build, which needs a container runtime
		// and the recipe's OCI base image, so it belongs to a build job; the check it
		// fails is the CheckAgentPin asked above.
		note(t, "manifest protocol+caps+prepare; guest hello %s/%s; a template with no agent caps is NOT usable here",
			rec.AgentVersion, rec.BootID)
	})

	// 12 — a non-root process in the guest cannot obtain the agent's credential, and a
	// host dial without it is rejected.
	//
	// Two things are measured, and the first has two halves: the sandbox's own user
	// cannot read the credential, and the read that *should* work does work. Without the
	// second half the criterion passes on a guest where `su` is missing or broken — a
	// 127 says nothing about permissions — so a refusal that measured nothing would be
	// recorded as a refusal that held.
	criterion(t, h, "12-auth", "a non-root guest process cannot become root, and an unauthenticated dial is refused", func(t *testing.T) {
		rec := h.create("mvp-auth")
		t.Cleanup(func() { h.remove(rec.ID) })

		// The privileged half is the control: this run's commands already run as root
		// through the agent, so if the credential cannot be read here, nothing below is
		// about permissions.
		asRoot := h.mustExec(rec.ID, "/bin/sh", "-c", "cat "+agent.TokenPath)
		assert.NotEmpty(t, strings.TrimSpace(asRoot), "root reads the credential through the agent's own exec")

		// The sandbox user's attempt. `su` must exist and must run the command: its own
		// exit code is the only thing that separates "was refused" from "never happened".
		out, _ := h.exec(rec.ID, "/bin/sh", "-c",
			"su -s /bin/sh sandbox -c 'cat "+agent.TokenPath+"' 2>&1; echo \"exit=$?\"")
		t.Logf("a non-root read of the agent credential:\n%s", out)
		assert.NotContains(t, out, "not found", "the guest has `su` and the sandbox user: a missing helper is not a refusal")
		assert.Contains(t, out, "exit=1", "the sandbox user's read is refused with a permission failure")
		assert.NotContains(t, out, "exit=0", "and it does not read the credential")
		assert.NotContains(t, out, strings.TrimSpace(asRoot),
			"the credential's value does not appear in the sandbox user's output")

		vsock, hostToken := sandboxMuxer(t, h, rec.ID)
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

		note(t, "the sandbox user cannot read %s; a tokenless host dial is refused; the minted token is accepted", agent.TokenPath)
	})

	// The host is left as the run found it, plus nothing: every sandbox this run made is
	// gone, and so is everything it held. This is the run's own promise, not a claim
	// that the host has no leftovers from earlier work.
	criterion(t, h, "run-clean", "the run leaves nothing behind", func(t *testing.T) {
		assert.Empty(t, h.list(), "no sandbox this run created survives it")

		after := hostFootprint(t)
		for _, leftover := range after.beyond(before) {
			assert.Fail(t, "the run left something on the host", leftover)
		}
		note(t, "no sandbox, namespace, listener or VMM of this run remains")
	})
}

// criterion runs one acceptance criterion, records its outcome in the evidence file,
// and — this is the point — lets the run continue whatever it decides.
//
// A criterion that returns with a failed assertion is recorded FAIL; one that could not
// be evaluated at all (a prerequisite missing) is recorded as such, in its own words;
// one that passes is recorded PASS with its measurement. The ledger is what the run is
// read from, so a criterion that never ran cannot be mistaken for one that held.
//
// Two things make that true rather than merely intended, and both were learned on a real
// host:
//
//   - h.t is pointed at this criterion's own subtest, so a helper's require.* reports
//     here. With the run's parent test in that field, a helper failure called FailNow on
//     the parent from this goroutine and ended the whole binary: criteria after it never
//     ran and no ledger line was written for the one that died — the run looked like one
//     with no ledger rather than one that stopped.
//   - the record is deferred, because FailNow ends this goroutine with runtime.Goexit and
//     skips everything after the criterion's body. Deferred calls still run, so a
//     criterion that dies inside a helper is recorded FAIL instead of vanishing.
func criterion(t *testing.T, h *host, id, title string, fn func(t *testing.T)) {
	t.Helper()
	t.Run(id+": "+title, func(t *testing.T) {
		h.t = t
		// The record is deferred: a failed assertion inside a helper ends this goroutine
		// with runtime.Goexit, and only deferred calls still run after that.
		defer func() {
			// A skipped criterion has not been measured, and "not measured" must not read
			// as a pass. The ledger's vocabulary is exactly the runner's: PASS, FAIL, and
			// NOT EVALUATED for one that could not be evaluated — the last two both fail
			// the run.
			status := "PASS"
			if t.Failed() {
				status = "FAIL"
			} else if t.Skipped() {
				status = "NOT EVALUATED"
			}
			recordStatus(t, id, status)
			// The verdict first, then what it was measured at — each line prefixed by the
			// verdict, so neither can be read as the other.
			flushNotes(t, status)
		}()

		// A panic in a criterion is that criterion's failure, not the run's end. It is
		// recovered here because the alternative — the whole binary dying — would hide
		// every criterion after it, which is the failure mode this file exists to end.
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("the criterion panicked: %v", r)
				}
			}()
			fn(t)
		}()
	})
}

// notes holds each criterion's note lines until its verdict is known. They are written
// together with the verdict, and prefixed by it, because a note written when it happens
// can end up above a FAIL and read like the result: the second run's ledger had a
// run-clean FAIL sitting on a note that said "no sandbox, namespace, listener or VMM of
// this run remains". Both lines were true; only one was the verdict.
var (
	notesMu sync.Mutex
	notes   = map[string][]string{}
)

// note records a measurement or fact inside a criterion that is already being recorded:
// the pass/fail line is written by criterion, and this is what it was measured at. It is
// emitted with the verdict, prefixed by it, so a note cannot be mistaken for one.
func note(t *testing.T, format string, args ...any) {
	t.Helper()
	message := fmt.Sprintf(format, args...)
	t.Logf("[evidence] %s", message)
	notesMu.Lock()
	notes[t.Name()] = append(notes[t.Name()], message)
	notesMu.Unlock()
}

// flushNotes writes a criterion's notes after its verdict, each prefixed with it, and
// forgets them.
func flushNotes(t *testing.T, status string) {
	notesMu.Lock()
	buffered := notes[t.Name()]
	delete(notes, t.Name())
	notesMu.Unlock()

	for _, message := range buffered {
		appendEvidence(t, "", status+": "+message)
	}
}

// errorDocument is the CLI's JSON rendering of a failed call: one Error document on
// stderr when the output format is json. It is the API's type, for the reason the
// documents in harness_test.go are: a copy of a document's fields can disagree with it.
type errorDocument = v1.ErrorDocument

// firstJSON finds the first complete JSON object in out: the CLI writes its document to
// stderr and a command may have written its own lines to stdout, so the document is
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

// mu guards the evidence file writes: a criterion's own lines and its result must not
// interleave with another's.
var mu sync.Mutex

// appendEvidence adds one line to the run's evidence file, which the workflow uploads as
// an artifact. The test log is the primary record; this is what survives a runner that
// truncates it.
func appendEvidence(t *testing.T, id, line string) {
	t.Helper()
	dir := os.Getenv(evidenceDirEnv)
	if dir == "" {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	f, err := os.OpenFile(filepath.Join(dir, "criteria.tsv"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Logf("could not write the evidence file: %v", err)
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = fmt.Fprintf(f, "%s\t%s\t%s\n", id, time.Now().UTC().Format(time.RFC3339), line)
}

// recordStatus writes one criterion's outcome. The status is the first word, so the file
// can be read by eye and by a script that counts PASS and FAIL lines.
func recordStatus(t *testing.T, id, status string) {
	t.Helper()
	appendEvidence(t, id, status)
}
