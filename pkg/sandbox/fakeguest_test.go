package sandbox

// A fake guest for the file and status operations.
//
// The sandbox package reaches a guest only by dialing the vsock muxer (`agent.Dial`), and
// that is a production path this package must not change to make itself testable. So the
// tests fake the *other side of the socket*: a muxer handshake followed by just enough of
// the agent protocol (hello, stat, get, put, tar, untar, info, net) to answer the manager,
// serving a temporary directory that stands in for the guest's workspace.
//
// It is deliberately not the real agent: the agent's own file protocol — framing, tar, and
// the confinement that decides what an archive may write — is tested in pkg/agent against
// its own fixture root. What is under test here is the manager's use of it: which operation
// it chooses, what it reports back, and which refusals it turns into which errors.

import (
	"archive/tar"
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
)

// errFakeDesync means the fake read a frame it did not expect in the middle of a stream,
// so where the next frame starts is unknown and the connection must be dropped rather than
// answered on.
var errFakeDesync = errors.New("fake guest: stream desynchronised")

// fakeGuest is one sandbox's guest, in this process.
type fakeGuest struct {
	// socket is the muxer path a sandbox record points at.
	socket string
	// root is the directory the file operations serve.
	root string
	// caps is what the guest advertises. A test drops agent.CapUntar from it to stand in
	// for a template built before the operation existed.
	caps []string

	mu sync.Mutex
	// ops records the operations served, in order, so a test can pin which one a call
	// used rather than only what it returned.
	ops []string
}

// newFakeGuest starts a guest serving tree (slash-separated path → content) under a
// temporary root, and returns it with the socket path a sandbox record must carry.
func newFakeGuest(t *testing.T, tree map[string]string) *fakeGuest {
	t.Helper()

	g := &fakeGuest{
		socket: filepath.Join(t.TempDir(), "guest.vsock"),
		root:   t.TempDir(),
		caps:   append([]string(nil), agent.Capabilities()...),
	}
	for name, body := range tree {
		full := filepath.Join(g.root, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(body), 0o644))
	}
	g.listen(t)
	return g
}

// listen serves the muxer socket until the test ends.
func (g *fakeGuest) listen(t *testing.T) {
	t.Helper()

	listener, err := net.Listen("unix", g.socket)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go g.serveConn(conn)
		}
	}()
}

// served is the operations the guest answered, in order.
func (g *fakeGuest) served() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.ops...)
}

// read is a file's content in the guest's tree.
func (g *fakeGuest) read(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(g.root, filepath.FromSlash(name)))
	require.NoError(t, err, name)
	return string(body)
}

// withoutCap drops one operation from what the guest advertises.
func (g *fakeGuest) withoutCap(capability string) {
	kept := make([]string, 0, len(g.caps))
	for _, cap := range g.caps {
		if cap != capability {
			kept = append(kept, cap)
		}
	}
	g.caps = kept
}

// serveConn is one muxer connection: Firecracker's CONNECT line, then the agent protocol.
func (g *fakeGuest) serveConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(strings.TrimSpace(line), "CONNECT ") {
		return
	}
	if _, err := fmt.Fprintf(conn, "OK 1073741824\n"); err != nil {
		return
	}

	for {
		var req agent.Request
		if _, err := agent.ReadFrame(reader, &req); err != nil {
			return
		}
		g.note(req.Op, req.Op != agent.CapHello)
		if !g.advertises(req.Op) {
			if err := write(conn, agent.Message{ID: req.ID, Type: "error",
				ErrorCode: agent.CodeUnsupportedOp, Error: "op " + req.Op + " is not advertised"}); err != nil {
				return
			}
			continue
		}
		if err := g.serve(conn, reader, req); err != nil {
			return
		}
	}
}

// note records an operation the caller asked for. The handshake is not one: it is how every
// connection begins, so recording it would hide which operation a call actually used.
func (g *fakeGuest) note(op string, record bool) {
	if !record {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ops = append(g.ops, op)
}

func (g *fakeGuest) advertises(op string) bool {
	for _, cap := range g.caps {
		if cap == op {
			return true
		}
	}
	return false
}

// serve answers one request. A non-nil result ends the connection; every failure the
// caller can be told about is sent as an error frame instead, so the guest stays usable.
func (g *fakeGuest) serve(conn net.Conn, reader *bufio.Reader, req agent.Request) error {
	switch req.Op {
	case agent.CapHello:
		reply := agent.HelloReply(g.caps)
		reply.ID = req.ID
		return write(conn, reply)

	case agent.CapStat:
		entry, err := g.stat(req.Path)
		if err != nil {
			return g.fail(conn, req, agent.CodeNotFound, err.Error())
		}
		return write(conn, agent.Message{ID: req.ID, Type: "stat", Stat: &entry})

	case agent.CapGet:
		if err := g.send(conn, req); err != nil {
			if errors.Is(err, errFakeDesync) {
				return err
			}
			return g.fail(conn, req, agent.CodeNotFound, err.Error())
		}
		return nil

	case agent.CapPut:
		if err := g.receive(conn, reader, req, func(tree io.Reader) error {
			body, err := io.ReadAll(tree)
			if err != nil {
				return err
			}
			full := filepath.Join(g.root, filepath.FromSlash(path.Clean(req.Path)))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return err
			}
			return os.WriteFile(full, body, 0o644)
		}); err != nil {
			if errors.Is(err, errFakeDesync) {
				return err
			}
			return g.fail(conn, req, agent.CodeInternal, err.Error())
		}
		return write(conn, agent.Message{ID: req.ID, Type: "ok"})

	case agent.CapTar:
		if err := g.tarOut(conn, req); err != nil {
			if errors.Is(err, errFakeDesync) {
				return err
			}
			return g.fail(conn, req, agent.CodeInternal, err.Error())
		}
		return nil

	case agent.CapUntar:
		var summary agent.TarSummary
		if err := g.receive(conn, reader, req, func(tree io.Reader) error {
			written, err := g.tarIn(req.Path, tree)
			summary = written
			return err
		}); err != nil {
			if errors.Is(err, errFakeDesync) {
				return err
			}
			return g.fail(conn, req, agent.CodeInternal, err.Error())
		}
		return write(conn, agent.Message{ID: req.ID, Type: "file", Tar: &summary})

	case agent.CapInfo:
		info := agent.GuestInfo{
			Hostname: "sandbox-fake", MachineID: "machine-fake", KernelRelease: "6.1.0",
			KernelVersion: "#1 SMP PREEMPT", Machine: "x86_64", OSName: "Alpine Linux v3.20",
			OSID: "alpine", CPUs: 2, MemoryTotalBytes: 512 << 20, UptimeSeconds: 42,
			Protocol: agent.Version, AgentVersion: "fake-agent", BuildID: "fake-build",
			BootID: "fake-boot", Tools: []string{"sh"},
		}
		return write(conn, agent.Message{ID: req.ID, Type: "info", Info: &info})

	case agent.CapNet:
		view := agent.GuestNet{
			Interfaces: []agent.NetInterface{{Name: "eth0", Index: 2, MAC: "02:00:00:00:00:01", MTU: 1500, Up: true}},
			Routes:     []agent.NetRoute{{Family: "ipv4", Destination: "default", Gateway: "10.63.0.1", Device: "eth0"}},
			Resolvers:  []string{"1.1.1.1"},
		}
		return write(conn, agent.Message{ID: req.ID, Type: "net", Net: &view})
	}

	return g.fail(conn, req, agent.CodeUnsupportedOp, "the fake guest does not serve "+req.Op)
}

// fail writes an error frame for a request.
func (g *fakeGuest) fail(conn net.Conn, req agent.Request, code agent.ErrorCode, message string) error {
	return write(conn, agent.Message{ID: req.ID, Type: "error", ErrorCode: code, Error: message})
}

// write sends one control frame.
func write(conn net.Conn, msg agent.Message) error { return agent.WriteFrame(conn, msg) }

// stat describes one path in the tree.
func (g *fakeGuest) stat(name string) (agent.FileEntry, error) {
	full := filepath.Join(g.root, filepath.FromSlash(path.Clean(name)))
	info, err := os.Lstat(full)
	if err != nil {
		return agent.FileEntry{}, err
	}
	entry := agent.FileEntry{
		Name: filepath.Base(name), Size: info.Size(), Mode: info.Mode().String(),
		ModTime: info.ModTime(),
	}
	switch {
	case info.IsDir():
		entry.Type = "dir"
	case info.Mode()&os.ModeSymlink != 0:
		entry.Type = "symlink"
	case info.Mode().IsRegular():
		entry.Type = "file"
	default:
		entry.Type = "other"
	}
	return entry, nil
}

// send streams one file out, as get does: data frames, then an empty one.
func (g *fakeGuest) send(conn net.Conn, req agent.Request) error {
	file, err := os.Open(filepath.Join(g.root, filepath.FromSlash(path.Clean(req.Path))))
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	buf := make([]byte, 64<<10)
	for {
		n, err := file.Read(buf)
		if n > 0 {
			if werr := write(conn, agent.Message{ID: req.ID, Type: "file", Data: buf[:n]}); werr != nil {
				return werr
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
	}
	return write(conn, agent.Message{ID: req.ID, Type: "file"})
}

// receive reads a request's streamed payload and hands it to consume as a reader. It
// returns the consumer's failure, or errFakeDesync when the stream is not where the
// protocol says it should be.
func (g *fakeGuest) receive(conn net.Conn, reader *bufio.Reader, req agent.Request, consume func(io.Reader) error) error {
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := consume(pr)
		_ = pr.CloseWithError(err)
		done <- err
	}()

	for {
		var msg agent.Message
		if _, err := agent.ReadStreamFrame(reader, &msg); err != nil {
			_ = pw.CloseWithError(err)
			return err
		}
		if msg.Type != "file" {
			_ = pw.CloseWithError(errFakeDesync)
			return errFakeDesync
		}
		if len(msg.Data) == 0 {
			break
		}
		if _, err := pw.Write(msg.Data); err != nil {
			return err
		}
	}
	_ = pw.Close()

	return <-done
}

// tarOut streams a tree as tar does: data frames, then a frame carrying the summary. The
// archive is written to the socket as the walk produces it, so a large tree costs a
// chunk's worth of memory rather than its own size.
func (g *fakeGuest) tarOut(conn net.Conn, req agent.Request) error {
	base := filepath.Join(g.root, filepath.FromSlash(path.Clean(req.Path)))

	stream := &tarStream{conn: conn, id: req.ID}
	writer := tar.NewWriter(stream)
	summary := agent.TarSummary{}
	err := filepath.Walk(base, func(full string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(g.root, full)
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			header.Name += "/"
		}
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			file, err := os.Open(full)
			if err != nil {
				return err
			}
			if _, err := io.Copy(writer, file); err != nil {
				_ = file.Close()
				return err
			}
			if err := file.Close(); err != nil {
				return err
			}
		}
		summary.Entries++
		return nil
	})
	if err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if err := stream.close(); err != nil {
		return err
	}

	summary.Bytes = stream.n
	return write(conn, agent.Message{ID: req.ID, Type: "file", Tar: &summary})
}

// tarStream writes an archive to the connection in frames.
type tarStream struct {
	conn net.Conn
	id   string
	buf  []byte
	// n is how many archive bytes went out, which is what a tar summary reports.
	n int64
}

func (s *tarStream) Write(p []byte) (int, error) {
	s.buf = append(s.buf, p...)
	for len(s.buf) >= 64<<10 {
		if err := s.flush(64 << 10); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (s *tarStream) flush(size int) error {
	if err := write(s.conn, agent.Message{ID: s.id, Type: "file", Data: s.buf[:size]}); err != nil {
		return err
	}
	s.n += int64(size)
	s.buf = append(s.buf[:0], s.buf[size:]...)
	return nil
}

func (s *tarStream) close() error {
	for len(s.buf) > 0 {
		size := min(len(s.buf), 64<<10)
		if err := s.flush(size); err != nil {
			return err
		}
	}
	return nil
}

// tarIn writes an archive into the tree and reports what it wrote. Like the agent's own
// untar, every entry must be the named directory or something under it: an archive that
// names another part of the file root is refused rather than written.
func (g *fakeGuest) tarIn(prefix string, r io.Reader) (agent.TarSummary, error) {
	var summary agent.TarSummary
	reader := tar.NewReader(r)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return summary, nil
		}
		if err != nil {
			return summary, err
		}

		name := path.Clean(strings.TrimSuffix(header.Name, "/"))
		root := path.Clean(prefix)
		if name != root && !strings.HasPrefix(name, root+"/") {
			return summary, fmt.Errorf("%s is not inside %s", header.Name, prefix)
		}

		full := filepath.Join(g.root, filepath.FromSlash(name))
		if header.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(full, os.FileMode(header.Mode).Perm()); err != nil {
				return summary, err
			}
			summary.Entries++
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return summary, err
		}
		file, err := os.OpenFile(full, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(header.Mode).Perm())
		if err != nil {
			return summary, err
		}
		written, err := io.Copy(file, reader)
		if cerr := file.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return summary, err
		}
		summary.Entries++
		summary.Bytes += written
	}
}
