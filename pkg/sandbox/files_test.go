package sandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
)

// readyWithFake is a sandbox record whose guest is the in-process fake, so the manager's
// file and status paths run over a real socket speaking the real protocol.
func readyWithFake(t *testing.T, f *fixture, g *fakeGuest) Record {
	t.Helper()
	rec := testRecord("sbx-1")
	rec.VsockPath = g.socket
	require.NoError(t, f.store.Put(rec))
	return rec
}

// TestDescribeFileReportsTheShapeOfAPath holds DescribeFile to what a caller decides from:
// which operation a copy of this path takes. A directory is an archive, a file is its bytes,
// and anything else is neither — a link is not followed, because following one would copy
// something other than what the caller named.
func TestDescribeFileReportsTheShapeOfAPath(t *testing.T) {
	f := newFixture(t)
	g := newFakeGuest(t, map[string]string{
		"main.go":    "package main\n",
		"src/lib.go": "package src\n",
	})
	rec := readyWithFake(t, f, g)

	file, err := f.manager.DescribeFile(context.Background(), rec.ID, "main.go")
	require.NoError(t, err)
	assert.Equal(t, FileKindFile, file.Kind)
	assert.Equal(t, "file", file.Entry.Type)
	assert.Equal(t, int64(len("package main\n")), file.Entry.Size)

	dir, err := f.manager.DescribeFile(context.Background(), rec.ID, "src")
	require.NoError(t, err)
	assert.Equal(t, FileKindDir, dir.Kind)
	assert.Equal(t, "dir", dir.Entry.Type)

	// A symlink is reported as itself, with no attempt to resolve it: the caller is told
	// what the path is, and a copy of it is refused below.
	require.NoError(t, os.Symlink("main.go", filepath.Join(g.root, "link.go")))
	link, err := f.manager.DescribeFile(context.Background(), rec.ID, "link.go")
	require.NoError(t, err)
	assert.Equal(t, FileKindOther, link.Kind, "a link is not a file to copy")
	assert.Equal(t, "symlink", link.Entry.Type)
}

// TestSendFileStreamsAFileAndADirectory pins the operation each shape takes. The guest
// records what it served, so a test can tell a copy that used `get` from one that used
// `tar` — the two differ in what the caller receives, and only one is right per path.
func TestSendFileStreamsAFileAndADirectory(t *testing.T) {
	f := newFixture(t)
	g := newFakeGuest(t, map[string]string{
		"readme.md":  "hello from the workspace\n",
		"src/lib.go": "package src\n",
	})
	rec := readyWithFake(t, f, g)
	ctx := context.Background()

	f.clock.Advance(30 * time.Second)

	var file bytes.Buffer
	summary, err := f.manager.SendFile(ctx, rec.ID, "readme.md", FileKindFile, &file)
	require.NoError(t, err)
	assert.Equal(t, "hello from the workspace\n", file.String())
	assert.Zero(t, summary.Entries, "a file is not an archive")

	var archive bytes.Buffer
	summary, err = f.manager.SendFile(ctx, rec.ID, "src", FileKindDir, &archive)
	require.NoError(t, err)
	assert.Equal(t, 2, summary.Entries, "the archive carries the directory itself and what is in it")

	names, contents := readArchive(t, archive.Bytes())
	assert.Equal(t, []string{"src/", "src/lib.go"}, names,
		"names are relative to the file root, so extracting the archive lands the same tree")
	assert.Equal(t, "package src\n", contents["src/lib.go"])

	assert.Equal(t, []string{"get", "tar"}, g.served(),
		"a file is streamed with get and a directory with tar")

	// Both copies used the sandbox, which is what the reaper measures idleness from.
	stored, found, err := f.store.Get(rec.ID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, f.clock.Now().UTC(), stored.LastActive)
}

// TestSendFileRefusesAnEntryThatIsNeither covers the third shape: a caller that described a
// link (or a device, a socket) gets an error rather than a copy of something else, and the
// guest is not asked at all.
func TestSendFileRefusesAnEntryThatIsNeither(t *testing.T) {
	f := newFixture(t)
	g := newFakeGuest(t, map[string]string{"file": "x"})
	rec := readyWithFake(t, f, g)

	var out bytes.Buffer
	_, err := f.manager.SendFile(context.Background(), rec.ID, "link", FileKindOther, &out)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalid)
	assert.Empty(t, g.served(), "nothing was asked of the guest")
}

// TestPutFileReportsTheBytesTheGuestReceived holds a put to the count of what actually went
// through the stream, which is what a caller reports rather than the length it intended.
func TestPutFileReportsTheBytesTheGuestReceived(t *testing.T) {
	f := newFixture(t)
	g := newFakeGuest(t, nil)
	rec := readyWithFake(t, f, g)

	body := strings.Repeat("payload-", 4096)
	written, err := f.manager.PutFile(context.Background(), rec.ID, "input.txt", 0o644, strings.NewReader(body))
	require.NoError(t, err)

	assert.Equal(t, int64(len(body)), written)
	assert.Equal(t, body, g.read(t, "input.txt"))
	assert.Equal(t, []string{"put"}, g.served())
}

// TestExtractTreeWritesAnArchiveIntoTheWorkspace is the write half of a copy: an archive a
// caller streams arrives as the tree it carries.
func TestExtractTreeWritesAnArchiveIntoTheWorkspace(t *testing.T) {
	f := newFixture(t)
	g := newFakeGuest(t, nil)
	rec := readyWithFake(t, f, g)

	archive := buildArchive(t, map[string]string{
		"repo/README.md":   "readme\n",
		"repo/pkg/main.go": "package pkg\n",
	})

	summary, err := f.manager.ExtractTree(context.Background(), rec.ID, "repo", bytes.NewReader(archive))
	require.NoError(t, err)

	assert.Equal(t, 2, summary.Entries)
	assert.Equal(t, int64(len("readme\n")+len("package pkg\n")), summary.Bytes)
	assert.Equal(t, "readme\n", g.read(t, "repo/README.md"))
	assert.Equal(t, "package pkg\n", g.read(t, "repo/pkg/main.go"))
	assert.Equal(t, []string{"untar"}, g.served())
}

// TestExtractTreeRefusesAnArchiveThatNamesAnotherPath holds the boundary to the guest's own
// rule: an archive may only write inside the directory it was sent to.
func TestExtractTreeRefusesAnArchiveThatNamesAnotherPath(t *testing.T) {
	f := newFixture(t)
	g := newFakeGuest(t, nil)
	rec := readyWithFake(t, f, g)

	archive := buildArchive(t, map[string]string{"elsewhere/escape.txt": "nope\n"})
	_, err := f.manager.ExtractTree(context.Background(), rec.ID, "repo", bytes.NewReader(archive))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not inside repo")

	_, statErr := os.Stat(filepath.Join(g.root, "elsewhere"))
	assert.ErrorIs(t, statErr, os.ErrNotExist, "nothing was written")
}

// TestExtractTreeReportsATemplateWithoutUntarAsAMissingCapability is the reason untar is an
// optional capability: a template built before it keeps working for everything else, and the
// one thing it cannot do is named as what it is rather than as a broken sandbox.
func TestExtractTreeReportsATemplateWithoutUntarAsAMissingCapability(t *testing.T) {
	f := newFixture(t)
	g := newFakeGuest(t, nil)
	g.withoutCap(agent.CapUntar)
	rec := readyWithFake(t, f, g)

	archive := buildArchive(t, map[string]string{"file.txt": "content\n"})
	_, err := f.manager.ExtractTree(context.Background(), rec.ID, ".", bytes.NewReader(archive))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMissingCapability)
	assert.Contains(t, err.Error(), agent.CapUntar)

	// The failure names the operation the template lacks, which is what an operator acts
	// on: it is the difference between "rebuild this template" and "something broke".
	var missing *MissingCapabilityError
	require.ErrorAs(t, err, &missing)
	assert.Equal(t, agent.CapUntar, missing.Capability)
	assert.Equal(t, rec.ID, missing.Sandbox)

	// The refusal happens on the client, before a frame is written, so the guest never saw
	// the operation: an old template is refused, not half-written.
	assert.Empty(t, g.served())

	// The same template still receives a single file, which is the fallback the capability
	// decision keeps: a file arrives through put whatever the agent supports.
	written, err := f.manager.PutFile(context.Background(), rec.ID, "file.txt", 0o644, strings.NewReader("content\n"))
	require.NoError(t, err)
	assert.Equal(t, int64(len("content\n")), written)
}

// TestStatusReadsTheGuestInsteadOfRunningACommand is the point of structured status: what it
// reports is the guest's own answer about itself, so it needs no command to exist in the
// template and nothing has to parse a transcript.
func TestStatusReadsTheGuestInsteadOfRunningACommand(t *testing.T) {
	f := newFixture(t)
	g := newFakeGuest(t, nil)
	rec := readyWithFake(t, f, g)

	status, err := f.manager.Status(context.Background(), rec.ID)
	require.NoError(t, err)

	assert.Equal(t, rec.ID, status.Record.ID)
	assert.Equal(t, "sandbox-fake", status.Info.Hostname)
	assert.Equal(t, 2, status.Info.CPUs)
	assert.Equal(t, uint64(512<<20), status.Info.MemoryTotalBytes)
	assert.Equal(t, agent.Version, status.Info.Protocol)
	require.Len(t, status.Net.Interfaces, 1)
	assert.Equal(t, "eth0", status.Net.Interfaces[0].Name)
	assert.Equal(t, []string{"1.1.1.1"}, status.Net.Resolvers)

	assert.Equal(t, []string{"info", "net"}, g.served(),
		"status is the two reads, never an exec whose output would have to be parsed")
}

// TestStatusSaysWhyASandboxCannotBeRead covers the two answers before the guest: an ID this
// node never had, and one that is not ready.
func TestStatusSaysWhyASandboxCannotBeRead(t *testing.T) {
	f := newFixture(t)
	g := newFakeGuest(t, nil)
	rec := readyWithFake(t, f, g)

	_, err := f.manager.Status(context.Background(), "sbx-missing")
	assert.ErrorIs(t, err, ErrNotFound)

	rec.State = StateCreated
	require.NoError(t, f.store.Put(rec))
	_, err = f.manager.Status(context.Background(), rec.ID)
	assert.ErrorIs(t, err, ErrNotReady)
	assert.Empty(t, g.served(), "a sandbox that is not ready is never dialed")
}

// TestServerLogPathFindsTheConsoleOrSaysWhyNot pins the three answers a caller can get:
// the console file, no console for this node, and a sandbox whose console is not there.
func TestServerLogPathFindsTheConsoleOrSaysWhyNot(t *testing.T) {
	logDir := t.TempDir()
	f := newFixtureWith(t, func(cfg *Config) { cfg.LogDir = logDir })
	rec := testRecord("sbx-1")
	require.NoError(t, f.store.Put(rec))

	require.NoError(t, os.WriteFile(filepath.Join(logDir, "sbx-1.log"), []byte("boot\n"), 0o600))
	path, err := f.manager.ServerLogPath(rec.ID)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(logDir, "sbx-1.log"), path)

	_, err = f.manager.ServerLogPath("sbx-missing")
	assert.ErrorIs(t, err, ErrNotFound)

	other := testRecord("sbx-2")
	require.NoError(t, f.store.Put(other))
	_, err = f.manager.ServerLogPath(other.ID)
	assert.ErrorIs(t, err, ErrNotFound, "a sandbox that has written no console is not a missing sandbox")
}

// TestServerLogPathSaysWhenTheNodeRecordsNoConsole covers the node-level answer, which is not
// a failure of the sandbox: a node configured without a log directory has none for any.
func TestServerLogPathSaysWhenTheNodeRecordsNoConsole(t *testing.T) {
	f := newFixture(t)
	rec := testRecord("sbx-1")
	require.NoError(t, f.store.Put(rec))

	_, err := f.manager.ServerLogPath(rec.ID)
	assert.ErrorIs(t, err, ErrNotImplemented)
}

// TestFileOperationsRequireAGuestPath covers the one argument these operations cannot default:
// an empty path would name the file root, which is not what a caller meant.
func TestFileOperationsRequireAGuestPath(t *testing.T) {
	f := newFixture(t)
	g := newFakeGuest(t, nil)
	rec := readyWithFake(t, f, g)
	ctx := context.Background()

	_, err := f.manager.DescribeFile(ctx, rec.ID, "  ")
	assert.ErrorIs(t, err, ErrInvalid)

	_, err = f.manager.PutFile(ctx, rec.ID, "", 0o644, strings.NewReader("x"))
	assert.ErrorIs(t, err, ErrInvalid)

	_, err = f.manager.ExtractTree(ctx, rec.ID, "", strings.NewReader(""))
	assert.ErrorIs(t, err, ErrInvalid)

	assert.Empty(t, g.served())
}

// streamFixture is one generated tree: the files it holds.
type streamFixture struct {
	files []streamFile
}

type streamFile struct {
	name   string
	size   int64
	digest string
}

// TestStreamingATreeRoundTripsAtScale copies a large tree both ways and checks every byte of
// it: the archive the guest received is exactly what was sent (the summary's byte count), and
// every file read back out of the workspace has the digest of the content that was generated.
//
// The default size is what CI should run. A larger run — the 2 GiB workspace the epic's
// acceptance criterion names — is opt-in through SBX_FILES_STREAM_BYTES, because the cost is
// disk and time rather than anything this test decides; the throughput of whichever size ran
// is reported, so a slow orb is admitted rather than hidden behind a smaller assertion.
func TestStreamingATreeRoundTripsAtScale(t *testing.T) {
	const (
		files         = 8
		defaultTotal  = int64(64 << 20)
		maxChunkBytes = 1 << 20
	)

	total := defaultTotal
	if raw := os.Getenv("SBX_FILES_STREAM_BYTES"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		require.NoError(t, err, "SBX_FILES_STREAM_BYTES")
		require.Greater(t, parsed, int64(0))
		total = parsed
	}
	size := total / files

	f := newFixture(t)
	g := newFakeGuest(t, nil)
	rec := readyWithFake(t, f, g)
	ctx := context.Background()

	// The archive is produced as it is streamed: the generator writes into a pipe the
	// manager reads from, so the test holds one chunk of the tree, not the tree.
	reader, writer := io.Pipe()
	generated := make(chan streamFixture, 1)
	failed := make(chan error, 1)
	go func() {
		fixture, err := generateTree(writer, "tree", files, size, maxChunkBytes)
		if err != nil {
			_ = writer.CloseWithError(err)
			failed <- err
			return
		}
		if err := writer.Close(); err != nil {
			failed <- err
			return
		}
		generated <- fixture
	}()

	start := time.Now()
	summary, err := f.manager.ExtractTree(ctx, rec.ID, "tree", reader)
	received := time.Since(start)
	require.NoError(t, err)

	var want streamFixture
	select {
	case want = <-generated:
	case err := <-failed:
		t.Fatalf("generate the tree: %v", err)
	}

	assert.Equal(t, files+1, summary.Entries, "every file and the directory itself")
	assert.Equal(t, total, summary.Bytes,
		"every content byte of the archive reached the guest's disk")

	// Read every file back out through the same streaming path and compare its digest with
	// what was generated. A truncated, reordered or corrupted stream changes a digest.
	var readBack int64
	start = time.Now()
	for _, file := range want.files {
		entry, err := f.manager.DescribeFile(ctx, rec.ID, file.name)
		require.NoError(t, err)
		assert.Equal(t, file.size, entry.Entry.Size, "%s has the size it was written with", file.name)

		hasher := sha256.New()
		_, err = f.manager.SendFile(ctx, rec.ID, file.name, FileKindFile, hasher)
		require.NoError(t, err)
		assert.Equal(t, file.digest, hex.EncodeToString(hasher.Sum(nil)), "%s came back byte for byte", file.name)
		readBack += entry.Entry.Size
	}
	sent := time.Since(start)

	assert.Equal(t, total, readBack, "every generated byte was read back")

	t.Logf("streamed %.1f MiB in and %.1f MiB out: %.1f MiB/s in, %.1f MiB/s out",
		float64(summary.Bytes)/(1<<20), float64(readBack)/(1<<20),
		mibPerSecond(summary.Bytes, received), mibPerSecond(readBack, sent))
}

func mibPerSecond(bytes int64, elapsed time.Duration) float64 {
	if elapsed <= 0 {
		return 0
	}
	return float64(bytes) / (1 << 20) / elapsed.Seconds()
}

// generateTree streams a tar archive of one directory holding files regular files of size
// bytes each, with content from a deterministic generator, and reports each file's name,
// size and digest. It writes through w as it goes, so the whole tree is never in memory.
func generateTree(w io.Writer, root string, files int, size int64, chunkBytes int64) (streamFixture, error) {
	var fixture streamFixture
	writer := tar.NewWriter(w)

	if err := writer.WriteHeader(&tar.Header{
		Name: root + "/", Typeflag: tar.TypeDir, Mode: 0o755,
	}); err != nil {
		return streamFixture{}, err
	}

	buf := make([]byte, chunkBytes)
	for i := 0; i < files; i++ {
		name := fmt.Sprintf("%s/file-%02d.bin", root, i)
		if err := writer.WriteHeader(&tar.Header{
			Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: size,
		}); err != nil {
			return streamFixture{}, err
		}

		hasher := sha256.New()
		remaining := size
		for remaining > 0 {
			chunk := min(int64(len(buf)), remaining)
			// The chunk's own offset seeds the pattern, so consecutive chunks differ even
			// though the generator is a pure function of its arguments.
			fillPattern(buf[:chunk], uint64(i)+1, uint64(size-remaining))
			if _, err := hasher.Write(buf[:chunk]); err != nil {
				return streamFixture{}, err
			}
			if _, err := writer.Write(buf[:chunk]); err != nil {
				return streamFixture{}, err
			}
			remaining -= chunk
		}
		fixture.files = append(fixture.files, streamFile{
			name: name, size: size, digest: hex.EncodeToString(hasher.Sum(nil)),
		})
	}

	if err := writer.Close(); err != nil {
		return streamFixture{}, err
	}
	return fixture, nil
}

// fillPattern fills buf with pseudorandom bytes from a tiny xorshift, so content is
// incompressible and reproducing it needs only the seed and the offset — a digest computed
// over regenerated bytes is a real check of what crossed the wire.
func fillPattern(buf []byte, seed, offset uint64) {
	x := (seed<<32 | 0x9e3779b97f4a7c15) ^ (offset + 1)
	for i := range buf {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		buf[i] = byte(x >> 24)
	}
}

// buildArchive builds an in-memory tar archive from slash-separated names to contents.
func buildArchive(t *testing.T, entries map[string]string) []byte {
	t.Helper()

	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	// A tar archive's order is its own; sorting keeps two runs of a test comparable.
	sort.Strings(names)

	var buf bytes.Buffer
	writer := tar.NewWriter(&buf)
	for _, name := range names {
		body := entries[name]
		require.NoError(t, writer.WriteHeader(&tar.Header{
			Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body)),
		}))
		_, err := writer.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	return buf.Bytes()
}

// readArchive reads back what buildArchive wrote: the names it carries and their contents.
func readArchive(t *testing.T, archive []byte) ([]string, map[string]string) {
	t.Helper()

	var names []string
	contents := map[string]string{}
	reader := tar.NewReader(bytes.NewReader(archive))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		body, err := io.ReadAll(reader)
		require.NoError(t, err)
		names = append(names, header.Name)
		contents[header.Name] = string(body)
	}
	return names, contents
}

// TestMissingCapabilityErrorNamesTheCapabilityAndItsSandbox pins both forms of the failure:
// with the sandbox named, which is what a caller of an operation gets, and without one, which
// is what a caller that only has the error sees.
func TestMissingCapabilityErrorNamesTheCapabilityAndItsSandbox(t *testing.T) {
	named := &MissingCapabilityError{Sandbox: "sbx-1", Capability: agent.CapUntar}
	assert.Contains(t, named.Error(), "sbx-1")
	assert.Contains(t, named.Error(), agent.CapUntar)
	assert.ErrorIs(t, named, ErrMissingCapability)

	bare := &MissingCapabilityError{Capability: agent.CapUntar}
	assert.Contains(t, bare.Error(), agent.CapUntar)
	assert.NotContains(t, bare.Error(), "sandbox :")
	assert.ErrorIs(t, bare, ErrMissingCapability)

	// An error that did not come from this protocol is not a missing capability, which is
	// what keeps a guest-side failure from being reported as an old template.
	assert.False(t, missingCapability(errors.New("the connection went away"), agent.CapUntar))
}

// TestPutFileReportsAGuestThatCannotBeReached covers the write path's failure: a guest that
// does not advertise put refuses the operation, and the manager reports whose failure it was.
func TestPutFileReportsAGuestThatCannotBeReached(t *testing.T) {
	f := newFixture(t)
	g := newFakeGuest(t, nil)
	g.withoutCap(agent.CapPut)
	rec := readyWithFake(t, f, g)

	_, err := f.manager.PutFile(context.Background(), rec.ID, "x", 0o644, strings.NewReader("x"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), rec.ID)
	assert.Contains(t, err.Error(), "x")
	assert.Empty(t, g.served())
}
