package agent

// The client side of the operations added with SBX-084–086, over a real
// connection: a host asks, the guest-side handler runs, and the answer crosses the
// wire as frames. What these tests add to the handlers' own tests is the wire
// between them — the capability gate on the way out, the frame shapes on the way
// back, and what a caller holds after a stream that ended badly.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withGuestRoots points the guest-side readers at a fixture tree for the duration
// of a test: /proc and /etc for info and net, /workspace for the file operations.
func withGuestRoots(t *testing.T) string {
	t.Helper()

	root := withFileRoot(t)

	proc := t.TempDir()
	etc := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(proc, "sys", "kernel"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(proc, "net"), 0o755))
	for name, body := range map[string]string{
		"sys/kernel/osrelease": "6.1.128-sc\n",
		"sys/kernel/version":   "#1 SMP\n",
		"sys/kernel/hostname":  "sandbox-wire\n",
		"uptime":               "77.00 1.00\n",
		"meminfo":              "MemTotal:         262144 kB\n",
		"net/route":            "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\neth0\t00000000\t0102000A\t0003\t0\t0\t100\t00000000\t0\t0\t0\n",
		"net/ipv6_route":       "",
	} {
		path := filepath.Join(proc, name)
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	}
	require.NoError(t, os.WriteFile(filepath.Join(etc, "resolv.conf"),
		[]byte("nameserver 10.64.0.1\nsearch svc.local\n"), 0o644))

	originalProc, originalEtc := procRoot, etcRoot
	procRoot, etcRoot = proc, etc
	t.Cleanup(func() { procRoot, etcRoot = originalProc, originalEtc })
	return root
}

// dialProxy dials the guest behind a proxy that serves the real handlers.
func dialProxy(t *testing.T) *Client {
	t.Helper()

	_, uds := startProxy(t, DefaultPort)
	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestClientInfoOverTheWire is the operation from the host's side: the fields a
// scheduler decides on arrive as the guest's own values.
func TestClientInfoOverTheWire(t *testing.T) {
	withGuestRoots(t)
	c := dialProxy(t)

	info, err := c.Info(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "sandbox-wire", info.Hostname)
	assert.Equal(t, "6.1.128-sc", info.KernelRelease)
	assert.Equal(t, uint64(262144*1024), info.MemoryTotalBytes)
	assert.Equal(t, 77.0, info.UptimeSeconds)
	assert.Equal(t, Version, info.Protocol)
}

// TestClientNetOverTheWire is the report a preview or an egress check reads: the
// guest's resolver and the default route it would use.
func TestClientNetOverTheWire(t *testing.T) {
	withGuestRoots(t)
	c := dialProxy(t)

	report, err := c.Net(context.Background())
	require.NoError(t, err)

	assert.Equal(t, []string{"10.64.0.1"}, report.Resolvers)
	assert.Equal(t, []string{"svc.local"}, report.Search)
	require.NotEmpty(t, report.Routes)
	assert.Equal(t, NetRoute{
		Family: "ipv4", Destination: "0.0.0.0/0", Gateway: "10.0.2.1", Device: "eth0", Metric: 100,
	}, report.Routes[0])
}

// TestClientStatAndListOverTheWire drives both file reads through the root, and
// proves the refusal crosses the wire with its own code: a caller branches on
// unsafe_path, it does not read a message.
func TestClientStatAndListOverTheWire(t *testing.T) {
	root := withGuestRoots(t)
	writeTree(t, root, map[string]string{"repo/a.txt": "a", "repo/b/c.txt": "c"})
	require.NoError(t, os.Symlink("a.txt", filepath.Join(root, "repo/link")))
	c := dialProxy(t)

	entry, err := c.Stat(context.Background(), "repo/a.txt")
	require.NoError(t, err)
	assert.Equal(t, "a.txt", entry.Name)
	assert.Equal(t, int64(1), entry.Size)

	listing, err := c.List(context.Background(), "repo")
	require.NoError(t, err)
	assert.False(t, listing.Truncated)
	names := make([]string, 0, len(listing.Entries))
	for _, item := range listing.Entries {
		names = append(names, item.Name)
	}
	assert.Equal(t, []string{"a.txt", "b", "link"}, names)

	_, err = c.List(context.Background(), "../../etc/passwd")
	require.Error(t, err)
	assert.Equal(t, CodeUnsafePath, CodeOf(err))

	_, err = c.Stat(context.Background(), "repo/absent")
	require.Error(t, err)
	assert.Equal(t, CodeNotFound, CodeOf(err))
}

// TestClientTarOverTheWire streams a tree and reads it back: the archive is what
// the summary says it is, and it holds the tree's own content.
func TestClientTarOverTheWire(t *testing.T) {
	root := withGuestRoots(t)
	writeTree(t, root, map[string]string{"repo/a.txt": "alpha", "repo/sub/b.txt": "bravo"})
	c := dialProxy(t)

	var archive bytes.Buffer
	summary, err := c.Tar(context.Background(), "repo", &archive)
	require.NoError(t, err)

	assert.Equal(t, 4, summary.Entries, "repo/, a.txt, sub/, sub/b.txt")
	assert.Equal(t, int64(archive.Len()), summary.Bytes)

	entries := readArchive(t, archive.Bytes())
	assert.Equal(t, []string{"repo/", "repo/a.txt", "repo/sub/", "repo/sub/b.txt"}, sortedNames(entries))
	assert.Equal(t, "alpha", entries["repo/a.txt"].body)
	assert.Equal(t, "bravo", entries["repo/sub/b.txt"].body)
}

// TestClientTarKeepsTheSessionAfterTheWriterFails is the clean stop from the
// host's side: the caller's own writer fails partway, the rest of the stream is
// read and dropped, and the session is left at a frame boundary — usable, and not
// mistaken for a finished archive.
func TestClientTarKeepsTheSessionAfterTheWriterFails(t *testing.T) {
	root := withGuestRoots(t)
	// Larger than one frame, so the failure lands mid-stream.
	writeTree(t, root, map[string]string{"repo/big.bin": strings.Repeat("x", 8*fileChunkSize)})
	c := dialProxy(t)

	diskFull := errors.New("no space left on device")
	w := &failingWriter{limit: 3 * fileChunkSize, err: diskFull}

	_, err := c.Tar(context.Background(), "repo", w)
	require.Error(t, err)
	assert.ErrorIs(t, err, diskFull, "the caller reads its own failure, with the reason")

	// The session is still at a frame boundary, so the next operation works.
	entry, err := c.Stat(context.Background(), "repo/big.bin")
	require.NoError(t, err)
	assert.Equal(t, int64(8*fileChunkSize), entry.Size)
}

// failingWriter accepts limit bytes and then refuses every write with err.
type failingWriter struct {
	written int
	limit   int
	err     error
}

func (w *failingWriter) Write(p []byte) (int, error) {
	if w.written+len(p) > w.limit {
		return 0, w.err
	}
	w.written += len(p)
	return len(p), nil
}

// TestClientRefusesAnOpTheAgentDoesNotAdvertise is the old-agent case from the
// host's side, for the operations added here: a guest that speaks protocol 2
// without advertising them is refused on the handshake it already has, with the
// operation named, and no frame is ever written for it.
//
// A frame is what must not happen: an agent that predates an operation has no
// handler for it, and a host that sent one anyway would be asking a stranger to
// guess. The proxy here watches the connection after the hello and reports any
// frame that arrives.
func TestClientRefusesAnOpTheAgentDoesNotAdvertise(t *testing.T) {
	frames := make(chan string, 4)
	_, uds := startProxy(t, DefaultPort, withServe(func(conn net.Conn) {
		// The handshake of an agent built before these operations: the same
		// protocol, and only the capabilities that existed then.
		var hello Request
		if _, err := ReadFrame(conn, &hello); err != nil {
			return
		}
		reply := HelloReply([]string{CapHello, CapExec, CapPut, CapGet, CapMetrics, CapPrepare})
		reply.ID = hello.ID
		_ = WriteFrame(conn, reply)

		// Anything after the handshake is a frame the host should not have sent.
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		for {
			var req Request
			if _, err := ReadFrame(conn, &req); err != nil {
				return
			}
			frames <- req.Op
		}
	}))
	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	ctx := context.Background()
	for name, call := range map[string]func() error{
		CapInfo:     func() error { _, err := c.Info(ctx); return err },
		CapNet:      func() error { _, err := c.Net(ctx); return err },
		CapStat:     func() error { _, err := c.Stat(ctx, "x"); return err },
		CapList:     func() error { _, err := c.List(ctx, "x"); return err },
		CapTar:      func() error { _, err := c.Tar(ctx, "x", io.Discard); return err },
		CapActivity: func() error { _, err := c.Activity(ctx); return err },
	} {
		err := call()
		require.Error(t, err, name)
		assert.Equal(t, CodeUnsupportedOp, CodeOf(err), name)
		assert.Contains(t, err.Error(), name, "the refusal names the operation the guest does not advertise")
		select {
		case frame := <-frames:
			t.Fatalf("the client wrote a %q frame for an operation the guest does not advertise", frame)
		case <-time.After(30 * time.Millisecond):
		}
	}
}

// TestClientActivityOverTheWire is the idle question: an agent that has served
// nothing since the handshake is idle, and its process count is the guest's.
func TestClientActivityOverTheWire(t *testing.T) {
	withGuestRoots(t)
	c := dialProxy(t)

	activity, err := c.Activity(context.Background())
	require.NoError(t, err)
	assert.Empty(t, activity.Inflight, "an idle agent has nothing in flight")
	assert.GreaterOrEqual(t, activity.UptimeSeconds, 0.0)
	assert.GreaterOrEqual(t, activity.Processes, 0)
}

// TestClientListCollectsEveryBatch proves the client reads a listing that arrives
// in more than one frame, which is the case a bound of one frame could not carry.
func TestClientListCollectsEveryBatch(t *testing.T) {
	root := withGuestRoots(t)
	// More entries than one batch frame holds, so the answer must be assembled
	// from several.
	count := listBatchEntries + 7
	for i := 0; i < count; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(root, "f"+leftPad(i)), nil, 0o644))
	}
	c := dialProxy(t)

	listing, err := c.List(context.Background(), "")
	require.NoError(t, err)
	assert.Len(t, listing.Entries, count)
	assert.False(t, listing.Truncated)
	assert.Equal(t, "f000000", listing.Entries[0].Name)
	assert.Equal(t, "f"+leftPad(count-1), listing.Entries[count-1].Name)
}
