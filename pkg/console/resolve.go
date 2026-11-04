package console

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/restuhaqza/swarmcracker/pkg/vminstance"
)

// Errors returned by Resolve and Dial.
var (
	// ErrNoConsole is returned when a reference matches no running VM console.
	ErrNoConsole = errors.New("no VM console found")
	// ErrAmbiguous is returned when a reference matches more than one VM.
	ErrAmbiguous = errors.New("ambiguous VM console reference")
)

// SocketPath returns the console socket path for a task ID.
func SocketPath(socketDir, taskID string) string {
	return filepath.Join(socketDir, taskID+socketSuffix)
}

// Console is one running VM's console: the identity a caller types and the socket
// that serves it.
//
// Recorded says which source named it. A recorded console comes from the node's
// VM registry, which states the path outright; an unrecorded one is a socket
// file found in the console directory, i.e. a VM started before the registry
// existed, whose path the one-release derivation still answers. The distinction
// is on the value, not hidden in the resolver, because a caller that needs more
// than the path (attach checks a legacy VM's API socket, for one) must be able to
// tell the two apart.
type Console struct {
	ID       string
	Socket   string
	Recorded bool
}

// InstanceSource is the node's registry of running VMs (pkg/vminstance), as
// console resolution reads it: one record per live VM, naming the console socket
// that VM owns. A record is the only thing that says where a console is when the
// path is not a function of the instance ID — a pooled, sandboxed, restored or
// forked VM — which is why it is asked first.
//
// Its List contract is the store's: live instances sorted by ID, plus the paths
// of records that could not be used. Resolution ignores the skipped paths —
// console attach is not the place to report an unreadable record — and only a
// directory-level failure is an error. *vminstance.Store is the production
// implementation; the interface keeps the resolver testable without a filesystem.
type InstanceSource interface {
	List() ([]vminstance.Instance, []string, error)
}

// Resolver resolves a VM reference to the console of a running VM. It asks the
// node's registry first and falls back to the console directory only for a VM
// the registry does not know, which is the one-release rule for VMs started
// before records existed.
type Resolver struct {
	// SocketDir is the directory that holds a VM's console socket, and the
	// directory the derivation reads: <SocketDir>/<ID>.console.sock. It may be
	// empty when Instances answers every reference, which is what a caller with
	// no socket directory configured (a test, a remote client) has.
	SocketDir string
	// Instances, when set, is the node's VM registry. A record names the console
	// socket outright, so a VM whose console is not a function of its ID resolves
	// here, and a VM the record says has no console is not offered one.
	//
	// A typed-nil store (*vminstance.Store)(nil) is not the same as nil: it would
	// answer every listing with an error. A caller assigns this field only once
	// the registry was opened.
	Instances InstanceSource
}

// List returns the consoles of the VMs this resolver can reach, sorted by ID. A
// recorded VM is listed from its record; a VM with no record is listed from the
// console socket file the derivation finds, so a stopped VM's leftover socket is
// still listed until its file is removed — the caller that attaches owns the
// liveness check for that case (see Console.Recorded). A missing socket
// directory is an empty list, not an error.
func (r Resolver) List() ([]Console, error) {
	byID := make(map[string]Console)
	// recorded holds every ID the registry answered for, including the VMs it says
	// have no console. The derivation must not offer those a path the record
	// deliberately left out.
	recorded := make(map[string]struct{})

	if r.Instances != nil {
		instances, _, err := r.Instances.List()
		if err != nil {
			return nil, err
		}
		for _, instance := range instances {
			recorded[instance.ID] = struct{}{}
			if instance.Console == "" {
				// The record says this VM has no console. Deriving one would hand
				// a caller a path nobody serves — or worse, a stale socket a
				// previous VM of the same name left behind.
				continue
			}
			byID[instance.ID] = Console{ID: instance.ID, Socket: instance.Console, Recorded: true}
		}
	}

	derived, err := listDerived(r.SocketDir)
	if err != nil {
		return nil, err
	}
	for _, id := range derived {
		if _, known := recorded[id]; known {
			// The registry already answered for this VM, with the path it owns —
			// or with no console at all.
			continue
		}
		byID[id] = Console{ID: id, Socket: SocketPath(r.SocketDir, id)}
	}

	out := make([]Console, 0, len(byID))
	for _, entry := range byID {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Resolve maps a user-supplied reference — an exact VM ID or a unique prefix of
// one — to the console of a running VM.
func (r Resolver) Resolve(ref string) (Console, error) {
	if ref == "" {
		return Console{}, fmt.Errorf("%w: empty reference", ErrNoConsole)
	}

	consoles, err := r.List()
	if err != nil {
		return Console{}, err
	}
	if len(consoles) == 0 {
		return Console{}, fmt.Errorf("%w under %s", ErrNoConsole, r.SocketDir)
	}

	for _, c := range consoles {
		if c.ID == ref {
			return c, nil
		}
	}

	matches := make([]Console, 0, len(consoles))
	for _, c := range consoles {
		if strings.HasPrefix(c.ID, ref) {
			matches = append(matches, c)
		}
	}

	switch len(matches) {
	case 0:
		return Console{}, fmt.Errorf("%w: %q does not match any running VM (available: %s)",
			ErrNoConsole, ref, strings.Join(idsOf(consoles), ", "))
	case 1:
		return matches[0], nil
	default:
		return Console{}, fmt.Errorf("%w: %q matches multiple VMs: %s",
			ErrAmbiguous, ref, strings.Join(idsOf(matches), ", "))
	}
}

// Dial resolves ref to a running VM console and connects to it, returning the
// console it resolved. A dial failure names the VM and the socket, because the
// two ways to reach a console the resolver cannot verify — a stale socket file,
// a console that has not been created yet — both end here.
func (r Resolver) Dial(ctx context.Context, ref string) (net.Conn, Console, error) {
	resolved, err := r.Resolve(ref)
	if err != nil {
		return nil, Console{}, err
	}

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", resolved.Socket)
	if err != nil {
		return nil, resolved, fmt.Errorf("failed to connect to VM %s console (%s): %w",
			resolved.ID, resolved.Socket, err)
	}
	return conn, resolved, nil
}

// listDerived returns the IDs of the console sockets in dir, i.e. the VMs whose
// console is a function of their ID. A missing directory yields an empty list,
// not an error.
func listDerived(dir string) ([]string, error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read console directory %s: %w", dir, err)
	}

	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, socketSuffix) {
			continue
		}
		ids = append(ids, strings.TrimSuffix(name, socketSuffix))
	}

	sort.Strings(ids)
	return ids, nil
}

// idsOf returns the IDs of the given consoles, for an error message that lists
// what the caller could have typed instead.
func idsOf(consoles []Console) []string {
	ids := make([]string, 0, len(consoles))
	for _, c := range consoles {
		ids = append(ids, c.ID)
	}
	return ids
}
