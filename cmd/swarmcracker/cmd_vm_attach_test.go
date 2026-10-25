package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVMAttachRemoteUsesTheAPIRoute pins the -H selection: with a manager
// endpoint set, `vm attach` runs the full-duplex API session instead of dialing
// a local console socket, and the console bytes reach stdout.
func TestVMAttachRemoteUsesTheAPIRoute(t *testing.T) {
	resetAPIFlags(t)

	var seen struct {
		method      string
		path        string
		accept      string
		contentType string
	}
	endpoint, caFile := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		seen.method = r.Method
		seen.path = r.URL.Path
		seen.accept = r.Header.Get("Accept")
		seen.contentType = r.Header.Get("Content-Type")

		if err := http.NewResponseController(w).EnableFullDuplex(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		if _, err := io.WriteString(w, "console: "); err != nil {
			return
		}
		_, _ = io.Copy(w, r.Body)
	})
	apiHost, apiCA = endpoint, caFile

	cmd := newVMAttachCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetIn(strings.NewReader("hi"))
	cmd.SetArgs([]string{"web-1"})

	require.NoError(t, cmd.Execute())

	assert.Equal(t, "console: hi", out.String())
	assert.Equal(t, http.MethodPost, seen.method)
	assert.Equal(t, "/v1/tasks/web-1/attach", seen.path)
	assert.Equal(t, "application/octet-stream", seen.accept)
	assert.Equal(t, "application/octet-stream", seen.contentType)
}

// TestVMAttachWithoutHostStaysLocal pins that the local path is untouched: no
// --host means the Unix-socket console, so a socket directory with no running VM
// fails there instead of contacting a manager.
func TestVMAttachWithoutHostStaysLocal(t *testing.T) {
	resetAPIFlags(t)

	dir, err := os.MkdirTemp("/tmp", "scattach-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	cmd := newVMAttachCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"--socket-dir", dir, "web-1"})

	err = cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no running VMs found under "+dir)
	assert.Contains(t, err.Error(), filepath.Base(dir))
}

// TestVMAttachIsRemoteCapable proves --host is deliberately allowed for
// `vm attach`, so the root guard does not refuse it.
func TestVMAttachIsRemoteCapable(t *testing.T) {
	resetAPIFlags(t)
	apiHost = "https://manager:4244"

	root := newRootCommand()
	cmd, _, err := root.Find([]string{"vm", "attach"})
	require.NoError(t, err)

	assert.True(t, commandAllowsRemote(cmd))
	require.NoError(t, guardRemoteHost(cmd, nil))
}

// TestVMAttachHelpDocumentsRemoteUsage pins the help surface: the remote
// example, the route, the scope and the detach rule are all stated.
func TestVMAttachHelpDocumentsRemoteUsage(t *testing.T) {
	cmd := newVMAttachCommand()

	assert.Contains(t, cmd.Long, "/v1/tasks/{id}/attach")
	assert.Contains(t, cmd.Long, "tasks:exec")
	assert.Contains(t, cmd.Long, "Ctrl-P Ctrl-Q")
	assert.Contains(t, cmd.Example, "-H https://manager:4244")
}

// TestAttachToRemoteVMLeavesRawModeAloneOnAPipe pins that raw mode is only for
// a terminal: a pipe stdin is forwarded without a terminal ioctl, and the
// remote implementation receives it unchanged.
func TestAttachToRemoteVMLeavesRawModeAloneOnAPipe(t *testing.T) {
	fake := &fakeAttacher{}
	in := strings.NewReader("typed")
	var out, errOut bytes.Buffer

	require.NoError(t, attachToRemoteVM(context.Background(), fake, "web-1", in, &out, &errOut))

	require.Equal(t, 1, fake.calls)
	assert.Equal(t, "web-1", fake.ref)
	body, err := io.ReadAll(fake.stdin)
	require.NoError(t, err)
	assert.Equal(t, "typed", string(body))
	assert.Empty(t, errOut.String(), "no attach banner without a terminal")
}

// TestAttachToRemoteVMPropagatesTheSessionResult pins that the command returns
// the client's error untouched, so a detach stays a nil (exit 0) result.
func TestAttachToRemoteVMPropagatesTheSessionResult(t *testing.T) {
	fake := &fakeAttacher{err: errDetach}

	err := attachToRemoteVM(context.Background(), fake, "web-1", strings.NewReader(""), io.Discard, io.Discard)
	require.ErrorIs(t, err, errDetach)
}

// fakeAttacher records the one attach session it was given.
type fakeAttacher struct {
	calls int
	ref   string
	stdin io.Reader
	err   error
}

func (f *fakeAttacher) AttachTask(_ context.Context, ref string, stdin io.Reader, _ io.Writer) error {
	f.calls++
	f.ref = ref
	f.stdin = stdin
	return f.err
}
