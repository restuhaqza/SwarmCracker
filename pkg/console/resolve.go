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

// List returns the task IDs of all VMs that currently expose a console socket
// under socketDir, sorted for stable output. A missing directory yields an
// empty list, not an error.
func List(socketDir string) ([]string, error) {
	entries, err := os.ReadDir(socketDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read console directory %s: %w", socketDir, err)
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

// Resolve maps a user-supplied reference — an exact task ID or a unique prefix
// of one — to the task ID and socket path of a running VM console.
func Resolve(socketDir, ref string) (taskID, socketPath string, err error) {
	if ref == "" {
		return "", "", fmt.Errorf("%w: empty reference", ErrNoConsole)
	}

	ids, err := List(socketDir)
	if err != nil {
		return "", "", err
	}
	if len(ids) == 0 {
		return "", "", fmt.Errorf("%w under %s", ErrNoConsole, socketDir)
	}

	for _, id := range ids {
		if id == ref {
			return id, SocketPath(socketDir, id), nil
		}
	}

	matches := make([]string, 0, len(ids))
	for _, id := range ids {
		if strings.HasPrefix(id, ref) {
			matches = append(matches, id)
		}
	}

	switch len(matches) {
	case 0:
		return "", "", fmt.Errorf("%w: %q does not match any running VM (available: %s)",
			ErrNoConsole, ref, strings.Join(ids, ", "))
	case 1:
		return matches[0], SocketPath(socketDir, matches[0]), nil
	default:
		return "", "", fmt.Errorf("%w: %q matches multiple VMs: %s",
			ErrAmbiguous, ref, strings.Join(matches, ", "))
	}
}

// Dial resolves ref to a running VM console and connects to it, returning the
// resolved task ID.
func Dial(ctx context.Context, socketDir, ref string) (net.Conn, string, error) {
	taskID, socketPath, err := Resolve(socketDir, ref)
	if err != nil {
		return nil, "", err
	}

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, taskID, fmt.Errorf("failed to connect to VM %s console (%s): %w", taskID, socketPath, err)
	}
	return conn, taskID, nil
}
