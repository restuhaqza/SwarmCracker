package sandbox

// Files in and out of a sandbox, and what a sandbox is.
//
// The file operations are the guest agent's own, used exactly as the task path uses
// them: `get` streams one file out, `put` streams one file in, and `tar`/`untar` are
// the two directions of a tree. Which one a request needs is decided *before* the
// stream starts, because a response's headers are written before its body: a caller
// describes the path (DescribeFile), and the shape it reports chooses between a file
// and an archive.
//
// A sandbox's paths are the guest's: they are relative to the workspace root the agent
// confines every file operation to (`/workspace` in the template), and the manager
// passes them through without interpreting them. That is deliberate — the guest is the
// only side that knows what its workspace is, and a node that rewrote paths would be a
// second, weaker rule beside the confinement the agent already applies.
//
// Every operation here is the *rooted* form (CopyToRooted, CopyFromRooted, and an untar
// whose prefix confine enforces): the credential in front of these routes reaches one
// sandbox's workspace, and none of them may touch the guest's own files. The task plane
// keeps the unrooted form, which is what `vm cp` and the task file routes mean.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
)

// ErrMissingCapability means the sandbox's guest agent does not advertise the operation
// the request needs: its template predates it. It is a property of the template rather
// than of the node or the request, so it is reported as a named capability (an older
// template keeps working for everything it did before) and never as an internal failure.
var ErrMissingCapability = errors.New("sandbox: the template's agent does not support this operation")

// MissingCapabilityError is ErrMissingCapability with the operation named, so a caller can
// say *which* capability a template is missing instead of that it is missing one. A
// template is only ever missing the operations added after it was built, so the name is
// what an operator acts on: rebuild it, or use the operation that predates it.
type MissingCapabilityError struct {
	// Sandbox is the sandbox whose template refused, when the caller named one.
	Sandbox string
	// Capability is the guest agent operation, such as "untar".
	Capability string
}

func (e *MissingCapabilityError) Error() string {
	if e.Sandbox == "" {
		return ErrMissingCapability.Error() + ": " + e.Capability
	}
	return fmt.Sprintf("sandbox %s: %v: %s", e.Sandbox, ErrMissingCapability, e.Capability)
}

// Unwrap makes the sentinel match, so errors.Is(err, ErrMissingCapability) keeps working
// for a caller that only needs the class of failure.
func (e *MissingCapabilityError) Unwrap() error { return ErrMissingCapability }

// FileKind is what a guest path is, and therefore which operation serves it.
type FileKind string

const (
	// FileKindFile is a regular file: served by the agent's get, written by its put.
	FileKindFile FileKind = "file"
	// FileKindDir is a directory: served as a tar archive, written by untar.
	FileKindDir FileKind = "dir"
	// FileKindOther is anything else — a link, a device, a socket. A copy of one is
	// refused rather than followed: following a link would copy something other than
	// what the caller named.
	FileKindOther FileKind = "other"
)

// File is what one guest path is: the entry the guest reports, and the shape a copy of
// it takes.
type File struct {
	Entry agent.FileEntry
	Kind  FileKind
}

// Status is what a sandbox is, from the guest's own answer rather than from parsing the
// output of a command: the record the node keeps, and the two documents the agent
// reports about the running machine.
type Status struct {
	Record Record
	// Info is the guest's identity, kernel, resources and the toolchain its template
	// ships (agent.CapInfo). It is what `sandbox status` shows in place of guessing.
	Info agent.GuestInfo
	// Net is the guest's own view of its network: interfaces, addresses, routes and
	// the resolver it consults (agent.CapNet).
	Net agent.GuestNet
}

// DescribeFile reports what a guest path is, so a caller can choose the operation before
// its response starts.
func (m *Manager) DescribeFile(ctx context.Context, id, guestPath string) (File, error) {
	rec, err := m.ready(id)
	if err != nil {
		return File{}, err
	}
	if strings.TrimSpace(guestPath) == "" {
		return File{}, fmt.Errorf("%w: a guest path is required", ErrInvalid)
	}

	client, err := m.guest(ctx, rec)
	if err != nil {
		return File{}, err
	}
	defer func() { _ = client.Close() }()

	entry, err := client.Stat(ctx, guestPath)
	if err != nil {
		return File{}, fmt.Errorf("sandbox %s: describe %s: %w", rec.ID, guestPath, err)
	}

	file := File{Entry: entry, Kind: FileKindOther}
	switch entry.Type {
	case "file":
		file.Kind = FileKindFile
	case "dir":
		file.Kind = FileKindDir
	}
	return file, nil
}

// SendFile streams one guest path into w, in the shape DescribeFile reported: the file's
// bytes for a file, a tar archive for a directory. The summary is the archive's, and is
// zero for a file.
//
// The kind is the caller's, from a describe it just made: a path that changed in between
// is still served — the guest answers for what it has now — and a path that is neither a
// file nor a directory is refused rather than resolved.
func (m *Manager) SendFile(ctx context.Context, id, guestPath string, kind FileKind, w io.Writer) (agent.TarSummary, error) {
	rec, err := m.ready(id)
	if err != nil {
		return agent.TarSummary{}, err
	}

	client, err := m.guest(ctx, rec)
	if err != nil {
		return agent.TarSummary{}, err
	}
	defer func() { _ = client.Close() }()

	switch kind {
	case FileKindFile:
		if err := client.CopyFromRooted(ctx, guestPath, w); err != nil {
			return agent.TarSummary{}, fmt.Errorf("sandbox %s: send %s: %w", rec.ID, guestPath, err)
		}
		m.touch(rec.ID)
		return agent.TarSummary{}, nil

	case FileKindDir:
		summary, err := client.Tar(ctx, guestPath, w)
		if err != nil {
			return agent.TarSummary{}, fmt.Errorf("sandbox %s: send %s: %w", rec.ID, guestPath, err)
		}
		m.touch(rec.ID)
		return summary, nil

	default:
		return agent.TarSummary{}, fmt.Errorf("%w: %s is not a file or a directory, and only those can be copied", ErrInvalid, guestPath)
	}
}

// PutFile streams r into one guest path as a regular file, and reports how many bytes it
// wrote. The mode is the guest file's; zero means 0644.
func (m *Manager) PutFile(ctx context.Context, id, guestPath string, mode uint32, r io.Reader) (int64, error) {
	rec, err := m.ready(id)
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(guestPath) == "" {
		return 0, fmt.Errorf("%w: a guest path is required", ErrInvalid)
	}

	client, err := m.guest(ctx, rec)
	if err != nil {
		return 0, err
	}
	defer func() { _ = client.Close() }()

	counting := &countingReader{r: r}
	if err := client.CopyToRooted(ctx, guestPath, counting, mode); err != nil {
		return counting.n, fmt.Errorf("sandbox %s: write %s: %w", rec.ID, guestPath, err)
	}
	m.touch(rec.ID)
	return counting.n, nil
}

// ExtractTree streams the tar archive r carries into the tree guestPath names, and
// reports what the guest wrote.
//
// A guest whose agent predates the operation refuses it, and that refusal is reported as
// the missing capability it is (ErrMissingCapability) rather than as a broken sandbox: the
// template is old, the sandbox is fine, and a single file still arrives through PutFile.
func (m *Manager) ExtractTree(ctx context.Context, id, guestPath string, r io.Reader) (agent.TarSummary, error) {
	rec, err := m.ready(id)
	if err != nil {
		return agent.TarSummary{}, err
	}
	if strings.TrimSpace(guestPath) == "" {
		return agent.TarSummary{}, fmt.Errorf("%w: a guest path is required", ErrInvalid)
	}

	client, err := m.guest(ctx, rec)
	if err != nil {
		return agent.TarSummary{}, err
	}
	defer func() { _ = client.Close() }()

	summary, err := client.Untar(ctx, guestPath, r)
	if err != nil {
		if missingCapability(err, agent.CapUntar) {
			return agent.TarSummary{}, &MissingCapabilityError{Sandbox: rec.ID, Capability: agent.CapUntar}
		}
		return agent.TarSummary{}, fmt.Errorf("sandbox %s: write the tree at %s: %w", rec.ID, guestPath, err)
	}
	m.touch(rec.ID)
	return summary, nil
}

// Status reports what a sandbox is, from the guest's own answer.
//
// It is the alternative to parsing the output of a command: the guest agent reads its
// own /proc, /etc/os-release and network state (agent.CapInfo, agent.CapNet), so what a
// caller sees is the guest's view of itself rather than a transcript an exec produced.
func (m *Manager) Status(ctx context.Context, id string) (Status, error) {
	rec, err := m.ready(id)
	if err != nil {
		return Status{}, err
	}

	client, err := m.guest(ctx, rec)
	if err != nil {
		return Status{}, err
	}
	defer func() { _ = client.Close() }()

	info, err := client.Info(ctx)
	if err != nil {
		return Status{}, fmt.Errorf("sandbox %s: read the guest's info: %w", rec.ID, err)
	}
	net, err := client.Net(ctx)
	if err != nil {
		return Status{}, fmt.Errorf("sandbox %s: read the guest's network: %w", rec.ID, err)
	}
	m.touch(rec.ID)
	return Status{Record: rec, Info: info, Net: net}, nil
}

// ServerLogPath is where a sandbox's serial console is written: <log-dir>/<id>.log. It is
// the guest's own console, not a container's log, and it exists for as long as the node
// keeps its log directory — which is why a sandbox that is gone may still have one.
//
// The record is checked first, so a caller is told "no such sandbox" for an id this node
// never had rather than that a log is missing.
func (m *Manager) ServerLogPath(id string) (string, error) {
	rec, err := m.Get(id)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(m.cfg.LogDir) == "" {
		return "", fmt.Errorf("%w: this node records no sandbox console", ErrNotImplemented)
	}
	path := filepath.Join(m.cfg.LogDir, rec.ID+".log")
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%w: sandbox %s has written no console log", ErrNotFound, rec.ID)
		}
		return "", fmt.Errorf("sandbox %s: read its console log: %w", rec.ID, err)
	}
	return path, nil
}

// guest dials the agent of a sandbox that is ready. It is the one place the dial is
// written, so every operation reports an unreachable guest the same way.
func (m *Manager) guest(ctx context.Context, rec Record) (*agent.Client, error) {
	client, err := agent.Dial(ctx, rec.VsockPath, agent.DefaultPort)
	if err != nil {
		return nil, fmt.Errorf("sandbox %s: reach its guest agent: %w", rec.ID, err)
	}
	return client, nil
}

// missingCapability reports whether err is the guest refusing an operation it does not
// advertise. The agent's own error type carries the code; a guest that predates the
// operation refuses it the same way whether the caller asked or streamed.
func missingCapability(err error, op string) bool {
	var agentError *agent.Error
	if errors.As(err, &agentError) {
		return agentError.Code == agent.CodeUnsupportedOp
	}
	return false
}

// countingReader counts what a copy actually read, so a put reports the bytes the guest
// received rather than the length the caller declared.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
