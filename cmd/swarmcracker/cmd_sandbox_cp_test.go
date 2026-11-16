package main

// The command-level tests for `sandbox cp`, `sandbox status` and `sandbox logs`.
//
// What they hold to account is the command's own job: which argument names a sandbox, which
// route and media type each direction takes, what lands on this machine, and what the local
// extractor refuses. The guest side of a copy is tested where it lives (pkg/agent, pkg/sandbox)
// and the HTTP layer in pkg/apihttp; here the server is a stand-in.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// TestSandboxCopyOutWritesAFile runs the whole out-direction of a single file: the command
// asks the node for the path, the node answers with its shape and bytes, and the bytes land
// where the caller named them.
func TestSandboxCopyOutWritesAFile(t *testing.T) {
	resetAPIFlags(t)

	var asked string
	endpoint, caFile := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		asked = r.Method + " " + r.URL.Path + "?path=" + r.URL.Query().Get("path")
		// The client asks in both shapes and lets the node choose, which is what lets the
		// command decide where the bytes go after it learns what they are.
		assert.Contains(t, r.Header.Get("Accept"), "application/octet-stream")
		assert.Contains(t, r.Header.Get("Accept"), "application/x-tar")
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Swarmcracker-File-Kind", "file")
		_, _ = io.WriteString(w, "package main\n")
	})
	apiHost, apiCA = endpoint, caFile

	target := filepath.Join(t.TempDir(), "main.go")
	out, err := executeSandboxCommand(t, newSandboxCommand(), "cp", "sbx-1:workspace/main.go", target)
	require.NoError(t, err)

	assert.Equal(t, "GET /v1/sandboxes/sbx-1/files?path=workspace/main.go", asked)
	body, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "package main\n", string(body))
	assert.Contains(t, out, "copied 13 bytes")
	assert.Contains(t, out, target)
}

// TestSandboxCopyOutExtractsATree is the directory half: the archive carries the guest path
// that was asked for, and it lands at the local path with that prefix stripped — the local
// path names the tree, exactly as the guest path named it on the other side.
func TestSandboxCopyOutExtractsATree(t *testing.T) {
	resetAPIFlags(t)

	archive := tarBytes(t, []tarEntry{
		{name: "workspace/repo/", dir: true},
		{name: "workspace/repo/README.md", body: "read me\n"},
		{name: "workspace/repo/pkg/", dir: true},
		{name: "workspace/repo/pkg/main.go", body: "package pkg\n"},
		{name: "workspace/repo/link.go", link: "pkg/main.go"},
		// A fifo cannot be created from an archive and is skipped, counted, not written.
		{name: "workspace/repo/pipe", fifo: true},
	})

	endpoint, caFile := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-tar")
		w.Header().Set("X-Swarmcracker-File-Kind", "dir")
		_, _ = w.Write(archive)
	})
	apiHost, apiCA = endpoint, caFile

	target := filepath.Join(t.TempDir(), "repo")
	out, err := executeSandboxCommand(t, newSandboxCommand(), "cp", "sbx-1:workspace/repo", target)
	require.NoError(t, err)

	assert.Contains(t, out, "extracted 5 entries")
	assert.Contains(t, out, "skipped 1 entries")

	body, err := os.ReadFile(filepath.Join(target, "README.md"))
	require.NoError(t, err)
	assert.Equal(t, "read me\n", string(body))
	body, err = os.ReadFile(filepath.Join(target, "pkg", "main.go"))
	require.NoError(t, err)
	assert.Equal(t, "package pkg\n", string(body))

	link, err := os.Readlink(filepath.Join(target, "link.go"))
	require.NoError(t, err)
	assert.Equal(t, "pkg/main.go", link, "a link that stays inside is written as a link")

	_, err = os.Stat(filepath.Join(target, "pipe"))
	assert.ErrorIs(t, err, os.ErrNotExist, "a fifo is not created")
}

// TestSandboxCopyOutRefusesAnArchiveThatLeavesTheDestination is the security half of the out
// direction: an archive from a sandbox is untrusted input, and an entry that would write
// outside the destination is refused rather than followed.
func TestSandboxCopyOutRefusesAnArchiveThatLeavesTheDestination(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entry   tarEntry
		archive map[string]string
	}{
		{
			name:  "a name outside the tree that was asked for",
			entry: tarEntry{name: "workspace/other/escape.txt", body: "nope\n"},
		},
		{
			name:  "a name that climbs out of the destination",
			entry: tarEntry{name: "workspace/repo/../../escape.txt", body: "nope\n"},
		},
		{
			name:  "a symbolic link out of the destination",
			entry: tarEntry{name: "workspace/repo/out.go", link: "../../../etc/passwd"},
		},
		{
			name:  "a hard link to something this archive did not write",
			entry: tarEntry{name: "workspace/repo/hard.go", hardlink: "/etc/passwd"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetAPIFlags(t)

			archive := tarBytes(t, []tarEntry{
				{name: "workspace/repo/", dir: true},
				tc.entry,
			})
			endpoint, caFile := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/x-tar")
				w.Header().Set("X-Swarmcracker-File-Kind", "dir")
				_, _ = w.Write(archive)
			})
			apiHost, apiCA = endpoint, caFile

			dir := t.TempDir()
			target := filepath.Join(dir, "repo")
			_, err := executeSandboxCommand(t, newSandboxCommand(), "cp", "sbx-1:workspace/repo", target)
			require.Error(t, err, "an archive that means \"write somewhere else\" is refused")

			// Nothing was written outside the destination: a refusal is not a partial write
			// the caller has to reason about.
			_, statErr := os.Stat(filepath.Join(dir, "escape.txt"))
			assert.ErrorIs(t, statErr, os.ErrNotExist)
		})
	}
}

// TestSandboxCopyInSendsAFile pins the in-direction of a single file: the local file's bytes
// are the request body, the mode travels as the query, and the answer is what the guest
// wrote rather than what was sent.
func TestSandboxCopyInSendsAFile(t *testing.T) {
	resetAPIFlags(t)

	var (
		asked       string
		contentType string
		received    string
	)
	endpoint, caFile := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		asked = r.Method + " " + r.URL.Path + "?mode=" + r.URL.Query().Get("mode") + "&path=" + r.URL.Query().Get("path")
		contentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		received = string(body)
		writeAPITestDocument(w, http.StatusOK, v1.NewSandboxFileWrite("workspace/main.go", int64(len(body)), 0, nil))
	})
	apiHost, apiCA = endpoint, caFile

	local := filepath.Join(t.TempDir(), "main.go")
	require.NoError(t, os.WriteFile(local, []byte("package main\n"), 0o600))

	out, err := executeSandboxCommand(t, newSandboxCommand(), "cp", local, "sbx-1:workspace/main.go")
	require.NoError(t, err)

	assert.Equal(t, "PUT /v1/sandboxes/sbx-1/files?mode=0600&path=workspace/main.go", asked,
		"the local file's mode travels with it")
	assert.Equal(t, "application/octet-stream", contentType)
	assert.Equal(t, "package main\n", received)
	assert.Contains(t, out, "wrote 13 bytes to sbx-1:workspace/main.go")
}

// TestSandboxCopyInStreamsATree holds the upload's shape: a directory becomes a tar archive
// whose names are rooted at the guest path that was asked for, which is what the guest
// requires every entry to be under.
func TestSandboxCopyInStreamsATree(t *testing.T) {
	resetAPIFlags(t)

	var (
		contentType string
		names       []string
		contents    map[string]string
	)
	endpoint, caFile := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		names, contents = readTar(t, r.Body)
		writeAPITestDocument(w, http.StatusOK, v1.NewSandboxFileWrite("workspace/repo", 20, len(names), nil))
	})
	apiHost, apiCA = endpoint, caFile

	local := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, os.MkdirAll(filepath.Join(local, "pkg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(local, "README.md"), []byte("read me\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(local, "pkg", "main.go"), []byte("package pkg\n"), 0o644))
	require.NoError(t, os.Symlink("pkg/main.go", filepath.Join(local, "link.go")))

	out, err := executeSandboxCommand(t, newSandboxCommand(), "cp", local, "sbx-1:workspace/repo")
	require.NoError(t, err)

	assert.Equal(t, "application/x-tar", contentType)
	assert.Equal(t, []string{"workspace/repo", "workspace/repo/README.md", "workspace/repo/link.go",
		"workspace/repo/pkg", "workspace/repo/pkg/main.go"}, names,
		"every name is under the guest path that was asked for")
	assert.Equal(t, "read me\n", contents["workspace/repo/README.md"])
	assert.Equal(t, "pkg/main.go", contents["workspace/repo/link.go"])
	assert.Contains(t, out, "wrote 5 entries")
}

// TestSandboxCopyNeedsExactlyOneSandbox keeps the two directions unambiguous: an argument
// pair that names no sandbox, or both, is a usage error rather than a guess.
func TestSandboxCopyNeedsExactlyOneSandbox(t *testing.T) {
	resetAPIFlags(t)

	for _, args := range [][]string{
		{"./a", "./b"},
		{"sbx-1:workspace/a", "sbx-2:workspace/b"},
	} {
		_, err := executeSandboxCommand(t, newSandboxCommand(), append([]string{"cp"}, args...)...)
		require.Error(t, err, args)
		var typed *output.Error
		require.ErrorAs(t, err, &typed)
		assert.Equal(t, output.CodeUsage, typed.Code)
	}

	// A path with a separator before the colon is a local path, not a sandbox reference:
	// the RFC 3339-looking and Windows-looking forms stay local.
	for _, arg := range []string{"./a:b", `C:\tmp\a`, "/tmp/a:b"} {
		_, _, ok := splitSandboxRef(arg)
		assert.False(t, ok, arg)
	}
	id, guestPath, ok := splitSandboxRef("sbx-1:/workspace/repo")
	require.True(t, ok)
	assert.Equal(t, "sbx-1", id)
	assert.Equal(t, "workspace/repo", guestPath, "a leading slash is the workspace root")
}

// TestSandboxStatusPrintsTheGuestsOwnAnswer holds the command to what it promises: the
// node's record plus what the guest said about itself, printed as facts rather than as a
// command's output.
func TestSandboxStatusPrintsTheGuestsOwnAnswer(t *testing.T) {
	resetAPIFlags(t)

	document := v1.NewSandboxStatus(
		v1.Sandbox{ID: "sbx-1", State: "ready", Template: "base@1", GuestIP: "10.63.0.2", MAC: "02:00:00:00:00:01"},
		v1.SandboxGuestInfo{
			Hostname: "sandbox-a", MachineID: "machine-a", KernelRelease: "6.1.0", KernelVersion: "#1 SMP",
			Machine: "x86_64", OSName: "Alpine Linux v3.20", OSID: "alpine",
			CPUs: 2, MemoryTotalBytes: 512 << 20, UptimeSeconds: 42,
			Protocol: "2", AgentVersion: "1.2.3", BootID: "abcdef0123456789", Tools: []string{"sh", "tar"},
		},
		v1.SandboxGuestNet{
			Interfaces: []v1.SandboxNetInterface{{Name: "eth0", Index: 2, MAC: "02:00:00:00:00:01", MTU: 1500, Up: true, Addresses: []string{"10.63.0.2/30"}}},
			Routes:     []v1.SandboxNetRoute{{Family: "ipv4", Destination: "default", Gateway: "10.63.0.1", Device: "eth0"}},
			Resolvers:  []string{"1.1.1.1"},
		},
	)

	endpoint, caFile := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET /v1/sandboxes/sbx-1/status", r.Method+" "+r.URL.Path)
		writeAPITestDocument(w, http.StatusOK, document)
	})
	apiHost, apiCA = endpoint, caFile

	out, err := executeSandboxCommand(t, newSandboxCommand(), "status", "sbx-1")
	require.NoError(t, err)
	for _, want := range []string{"sbx-1", "ready", "base@1", "10.63.0.2", "sandbox-a", "6.1.0",
		"Alpine Linux v3.20", "2 vCPU, 512 MiB", "42s", "sh, tar", "eth0", "default via 10.63.0.1 dev eth0",
		"1.1.1.1"} {
		assert.Contains(t, out, want)
	}
	localized := func(t *testing.T, out string) string {
		t.Helper()
		// "10.63.0.2/30" is the guest's own address; the node's /30 also appears. Keep the
		// assertion on what the guest reported.
		return out
	}
	assert.Contains(t, localized(t, out), "10.63.0.2/30")

	// --format json carries the document rather than a rendering of it, so a script reads
	// the same fields an API caller does.
	outputFormat = "json"
	out, err = executeSandboxCommand(t, newSandboxCommand(), "status", "sbx-1")
	require.NoError(t, err)
	var decoded v1.SandboxStatus
	require.NoError(t, json.Unmarshal([]byte(out), &decoded))
	assert.Equal(t, "sandbox-a", decoded.Info.Hostname)
	require.Len(t, decoded.Net.Interfaces, 1)
	assert.Equal(t, "eth0", decoded.Net.Interfaces[0].Name)
}

// TestSandboxLogsPrintsTheConsole pins the console read: text/plain in, and the bytes out
// unchanged, because a console is what the guest printed.
func TestSandboxLogsPrintsTheConsole(t *testing.T) {
	resetAPIFlags(t)

	endpoint, caFile := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET /v1/sandboxes/sbx-1/logs", r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "[    0.000000] Linux version 6.1.0\n[    0.1] booting\n")
	})
	apiHost, apiCA = endpoint, caFile

	out, err := executeSandboxCommand(t, newSandboxCommand(), "logs", "sbx-1")
	require.NoError(t, err)
	assert.Equal(t, "[    0.000000] Linux version 6.1.0\n[    0.1] booting\n", out)
}

// tarEntry is one entry of a test archive: a directory, a regular file with content, a
// symbolic link, a hard link, or a fifo.
type tarEntry struct {
	name     string
	body     string
	dir      bool
	link     string
	hardlink string
	fifo     bool
}

// tarBytes builds an archive from entries, in the order given.
func tarBytes(t *testing.T, entries []tarEntry) []byte {
	t.Helper()

	var buf bytes.Buffer
	writer := tar.NewWriter(&buf)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Mode: 0o644}
		switch {
		case entry.dir:
			header.Typeflag, header.Mode = tar.TypeDir, 0o755
		case entry.link != "":
			header.Typeflag, header.Linkname = tar.TypeSymlink, entry.link
		case entry.hardlink != "":
			header.Typeflag, header.Linkname = tar.TypeLink, entry.hardlink
		case entry.fifo:
			header.Typeflag = tar.TypeFifo
		default:
			header.Typeflag, header.Size = tar.TypeReg, int64(len(entry.body))
		}
		require.NoError(t, writer.WriteHeader(header))
		if header.Typeflag == tar.TypeReg {
			_, err := io.WriteString(writer, entry.body)
			require.NoError(t, err)
		}
	}
	require.NoError(t, writer.Close())
	return buf.Bytes()
}

// readTar is what a test server does with an uploaded tree: the names and contents the guest
// would have received.
func readTar(t *testing.T, r io.Reader) ([]string, map[string]string) {
	t.Helper()

	var names []string
	contents := map[string]string{}
	reader := tar.NewReader(r)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		name := strings.TrimSuffix(header.Name, "/")
		names = append(names, name)
		switch header.Typeflag {
		case tar.TypeReg:
			body, err := io.ReadAll(reader)
			require.NoError(t, err)
			contents[name] = string(body)
		case tar.TypeSymlink:
			contents[name] = header.Linkname
		}
	}
	return names, contents
}

// TestLocalExtractorRefusesAnAbsoluteNameAndAnEscapingLink covers the two rules a directory
// extraction keeps that a name check cannot: an absolute entry, and a link whose target
// leaves the destination.
func TestLocalExtractorRefusesAnAbsoluteNameAndAnEscapingLink(t *testing.T) {
	dest := t.TempDir()

	// An absolute name is refused: os.Root would refuse it too, and the error is the point.
	_, err := extractLocalTree(bytes.NewReader(tarBytes(t, []tarEntry{{name: "/etc/passwd", body: "x"}})), dest, "")
	require.Error(t, err)

	// A link that points out of the destination is refused, and the refusal happens before
	// anything is written through it.
	archive := tarBytes(t, []tarEntry{
		{name: "repo/", dir: true},
		{name: "repo/out", link: "../../outside"},
	})
	_, err = extractLocalTree(bytes.NewReader(archive), dest, "repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "leaves")
}

// TestStreamingATreeCompressesNothing pins the shape of what the upload builds: a plain tar
// archive, not a compressed one, because the guest reads it as it arrives.
func TestStreamingATreeCompressesNothing(t *testing.T) {
	local := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(local, "a.txt"), []byte("hello\n"), 0o644))

	var buf bytes.Buffer
	skipped, err := streamLocalTree(&buf, local, "workspace/x")
	require.NoError(t, err)
	assert.Zero(t, skipped)

	names, _ := readTar(t, bytes.NewReader(buf.Bytes()))
	assert.Equal(t, []string{"workspace/x", "workspace/x/a.txt"}, names)

	// The archive is not gzip: a gzip reader would fail on it, which is the observable
	// difference for a guest that reads frames as they arrive.
	_, err = gzip.NewReader(bytes.NewReader(buf.Bytes()))
	assert.Error(t, err)
}
