package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/client"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVMExecCommandStructure(t *testing.T) {
	cmd := newVMExecCommand()
	require.Equal(t, "exec", cmd.Name())

	for _, name := range []string{"socket-dir", "interactive", "tty", "env", "workdir"} {
		assert.NotNilf(t, cmd.Flags().Lookup(name), "flag %q not found", name)
	}
	assert.Equal(t, "i", cmd.Flags().Lookup("interactive").Shorthand)
	assert.Equal(t, "t", cmd.Flags().Lookup("tty").Shorthand)
	assert.Equal(t, "e", cmd.Flags().Lookup("env").Shorthand)
	assert.Equal(t, "w", cmd.Flags().Lookup("workdir").Shorthand)
	assert.Equal(t, defaultConsoleSocketDir, cmd.Flags().Lookup("socket-dir").DefValue)

	require.NotNil(t, cmd.Args)
	assert.NoError(t, cmd.Args(cmd, []string{"vm1"}))
	assert.NoError(t, cmd.Args(cmd, []string{"vm1", "echo"}))
	assert.Error(t, cmd.Args(cmd, []string{}))
}

func TestVMExecCommandParsesCombinedShorthands(t *testing.T) {
	cmd := newVMExecCommand()
	require.NoError(t, cmd.ParseFlags([]string{"-it"}))

	interactive, err := cmd.Flags().GetBool("interactive")
	require.NoError(t, err)
	tty, err := cmd.Flags().GetBool("tty")
	require.NoError(t, err)

	assert.True(t, interactive)
	assert.True(t, tty)
}

func TestVMExecCommandParsesEnvAndWorkdir(t *testing.T) {
	cmd := newVMExecCommand()
	require.NoError(t, cmd.ParseFlags([]string{"-e", "FOO=bar", "-e", "BAZ=qux", "-w", "/srv"}))

	env, err := cmd.Flags().GetStringArray("env")
	require.NoError(t, err)
	assert.Equal(t, []string{"FOO=bar", "BAZ=qux"}, env)

	workdir, err := cmd.Flags().GetString("workdir")
	require.NoError(t, err)
	assert.Equal(t, "/srv", workdir)
}

func TestVMExecRejectsEmptyCommand(t *testing.T) {
	cmd := newVMExecCommand()
	cmd.SetArgs([]string{"myvm"})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no command specified")
}

func TestCLIExitCode(t *testing.T) {
	assert.Equal(t, 0, cliExitCode(nil))
	assert.Equal(t, 1, cliExitCode(errors.New("boom")))
	assert.Equal(t, 7, cliExitCode(&exitCodeError{code: 7}))
	assert.Equal(t, 7, cliExitCode(fmt.Errorf("wrapped: %w", &exitCodeError{code: 7})))
	assert.Equal(t, 1, cliExitCode(&exitCodeError{code: 0}))
}

func TestVMExecExitCodeFlag(t *testing.T) {
	cmd := newVMExecCommand()
	flag := cmd.Flags().Lookup("exit-code")
	require.NotNil(t, flag, "--exit-code must exist")
	assert.Equal(t, string(exitCodeGuest), flag.DefValue)
}

func TestVMExecRejectsUnknownExitCodeMode(t *testing.T) {
	cmd := newVMExecCommand()
	cmd.SetArgs([]string{"--exit-code", "shell", "vm1", "--", "true"})

	err := cmd.Execute()
	require.Error(t, err)
	requireCode(t, err, output.CodeUsage)
}

// TestExecExitErrorMapping pins both conventions: guest mode passes the code
// through (including 128+signal), api mode collapses any non-zero exit to 8.
func TestExecExitErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		out    execOutcome
		mode   exitCodeMode
		want   int
		signal int
	}{
		{"guest zero", execOutcome{}, exitCodeGuest, 0, 0},
		{"guest non-zero", execOutcome{code: 3}, exitCodeGuest, 3, 0},
		{"guest default mode", execOutcome{code: 3}, "", 3, 0},
		{"api non-zero", execOutcome{code: 3}, exitCodeAPI, apiFailureExitCode, 0},
		{"api zero", execOutcome{}, exitCodeAPI, 0, 0},
		{"guest signal", execOutcome{code: 137, signal: 9}, exitCodeGuest, 137, 9},
		{"api signal", execOutcome{code: 143, signal: 15}, exitCodeAPI, 143, 15},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := execExitError(tc.out, tc.mode)
			assert.Equal(t, tc.want, cliExitCode(err))
			if tc.signal == 0 {
				return
			}
			var ec *exitCodeError
			require.ErrorAs(t, err, &ec)
			assert.Equal(t, tc.signal, ec.signal)
		})
	}
}

// fakeRemoteExecer stands in for the HTTP client so the remote branch can be
// exercised without a server.
type fakeRemoteExecer struct {
	result client.ExecResult
	err    error
	stdout string
	stderr string
	gotRef string
	gotReq v1.ExecRequest
}

func (f *fakeRemoteExecer) ExecTask(_ context.Context, ref string, req v1.ExecRequest, stdout, stderr io.Writer) (client.ExecResult, error) {
	f.gotRef = ref
	f.gotReq = req
	if f.err != nil {
		return client.ExecResult{}, f.err
	}
	if stdout != nil {
		_, _ = io.WriteString(stdout, f.stdout)
	}
	if stderr != nil {
		_, _ = io.WriteString(stderr, f.stderr)
	}
	return f.result, nil
}

func TestExecInVMRemoteUsesAPI(t *testing.T) {
	remote := &fakeRemoteExecer{
		result: client.ExecResult{ExitCode: 3},
		stdout: "remote out\n",
		stderr: "remote err\n",
	}
	var stdout, stderr bytes.Buffer

	err := execInVM(context.Background(), "web-1", vmExecOptions{
		command:  []string{"echo", "hi"},
		env:      []string{"APP=prod"},
		workdir:  "/srv",
		exitMode: exitCodeGuest,
		remote:   remote,
		stdout:   &stdout,
		stderr:   &stderr,
	})

	var ec *exitCodeError
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, 3, ec.code)
	assert.Equal(t, "remote out\n", stdout.String())
	assert.Equal(t, "remote err\n", stderr.String())
	assert.Equal(t, "web-1", remote.gotRef)
	assert.Equal(t, []string{"echo", "hi"}, remote.gotReq.Command)
	assert.Equal(t, map[string]string{"APP": "prod"}, remote.gotReq.Env)
	assert.Equal(t, "/srv", remote.gotReq.WorkDir)
	assert.False(t, remote.gotReq.TTY)
}

func TestExecInVMRemoteAPIModeCollapsesNonZero(t *testing.T) {
	remote := &fakeRemoteExecer{result: client.ExecResult{ExitCode: 5}}
	err := execInVM(context.Background(), "web-1", vmExecOptions{
		command:  []string{"false"},
		exitMode: exitCodeAPI,
		remote:   remote,
		stdout:   &bytes.Buffer{},
		stderr:   &bytes.Buffer{},
	})

	var ec *exitCodeError
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, apiFailureExitCode, ec.code)
}

func TestExecInVMRemoteSignal(t *testing.T) {
	remote := &fakeRemoteExecer{result: client.ExecResult{ExitCode: 143, Signal: 15}}
	err := execInVM(context.Background(), "web-1", vmExecOptions{
		command: []string{"sleep", "100"},
		remote:  remote,
		stdout:  &bytes.Buffer{},
		stderr:  &bytes.Buffer{},
	})

	var ec *exitCodeError
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, 143, ec.code)
	assert.Equal(t, 15, ec.signal)
}

func TestExecInVMRemoteTruncatedStreamFails(t *testing.T) {
	remote := &fakeRemoteExecer{err: output.NewError(output.CodePartial, "exec stream ended without an exit frame")}
	err := execInVM(context.Background(), "web-1", vmExecOptions{
		command: []string{"tail"},
		remote:  remote,
		stdout:  &bytes.Buffer{},
		stderr:  &bytes.Buffer{},
	})

	require.Error(t, err)
	var ec *exitCodeError
	assert.False(t, errors.As(err, &ec), "a truncated stream must never look like a guest exit")
	requireCode(t, err, output.CodePartial)
}

func TestExecInVMRemoteRejectsBadEnv(t *testing.T) {
	remote := &fakeRemoteExecer{}
	err := execInVM(context.Background(), "web-1", vmExecOptions{
		command: []string{"env"},
		env:     []string{"NOEQUALS"},
		remote:  remote,
		stdout:  &bytes.Buffer{},
		stderr:  &bytes.Buffer{},
	})

	require.Error(t, err)
	requireCode(t, err, output.CodeUsage)
	assert.Empty(t, remote.gotRef, "an invalid environment must not reach the API")
}

// TestVMExecRemoteEndToEnd drives the real command against a TLS API server: the
// remote path is selected, frames are streamed, and --exit-code=api maps a
// non-zero guest exit to 8.
func TestVMExecRemoteEndToEnd(t *testing.T) {
	resetAPIFlags(t)

	type observed struct {
		path string
		body v1.ExecRequest
	}
	seen := make(chan observed, 1)

	endpoint, caFile := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req v1.ExecRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		seen <- observed{path: r.URL.Path, body: req}

		w.Header().Set("Content-Type", v1.NDJSONMediaType)
		w.WriteHeader(http.StatusOK)
		encoder := json.NewEncoder(w)
		_ = encoder.Encode(v1.StdoutFrame([]byte("remote\n")))
		_ = encoder.Encode(v1.ExitFrame(4))
	})

	apiHost, apiCA = endpoint, caFile

	cmd := newVMExecCommand()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--exit-code", "api", "-e", "APP=prod", "-w", "/srv", "web-1", "--", "echo", "hi"})

	err := cmd.Execute()
	var ec *exitCodeError
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, apiFailureExitCode, ec.code)
	assert.Equal(t, "remote\n", stdout.String())

	got := <-seen
	assert.Equal(t, "/v1/tasks/web-1/exec", got.path)
	assert.Equal(t, []string{"echo", "hi"}, got.body.Command)
	assert.Equal(t, map[string]string{"APP": "prod"}, got.body.Env)
	assert.Equal(t, "/srv", got.body.WorkDir)
}

// TestVMExecRemotePassesGuestCodeThrough pins the default mode over the wire.
func TestVMExecRemotePassesGuestCodeThrough(t *testing.T) {
	resetAPIFlags(t)

	endpoint, caFile := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", v1.NDJSONMediaType)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(v1.ExitFrame(7))
	})
	apiHost, apiCA = endpoint, caFile

	cmd := newVMExecCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"web-1", "--", "exit", "7"})

	err := cmd.Execute()
	var ec *exitCodeError
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, 7, ec.code)
}

// TestVMExecIsRemoteCapable proves --host is deliberately allowed for vm exec.
func TestVMExecIsRemoteCapable(t *testing.T) {
	resetAPIFlags(t)
	apiHost = "https://manager:4244"

	root := newRootCommand()
	cmd, _, err := root.Find([]string{"vm", "exec"})
	require.NoError(t, err)

	assert.True(t, commandAllowsRemote(cmd))
	require.NoError(t, guardRemoteHost(cmd, nil))
}

func TestExitCodeErrorMessage(t *testing.T) {
	assert.Equal(t, "exit status 3", (&exitCodeError{code: 3}).Error())
}

func TestVMExecEndToEndPropagatesExitCode(t *testing.T) {
	vm := startFakeVM(t, "vm-exec", nil)

	cmd := newVMExecCommand()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{
		"--socket-dir", vm.SocketDir,
		"vm-exec", "--", "sh", "-c", "echo hello; echo oops 1>&2; exit 3",
	})

	err := cmd.Execute()
	require.Error(t, err)

	var ec *exitCodeError
	require.True(t, errors.As(err, &ec), "expected exitCodeError, got %v", err)
	assert.Equal(t, 3, ec.code)
	assert.Equal(t, 3, cliExitCode(err))
	assert.Equal(t, "hello\n", stdout.String())
	assert.Equal(t, "oops\n", stderr.String())
}

func TestVMExecEndToEndSuccess(t *testing.T) {
	vm := startFakeVM(t, "vm-ok", nil)

	cmd := newVMExecCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--socket-dir", vm.SocketDir, "vm-ok", "--", "echo", "hi"})

	require.NoError(t, cmd.Execute())
	assert.Equal(t, "hi\n", out.String())
}

func TestVMExecForwardsCommandEnvAndWorkdir(t *testing.T) {
	reqs := make(chan agent.Request, 1)
	vm := startFakeVM(t, "vm-req", func(conn net.Conn) {
		var req agent.Request
		_, _ = agent.ReadFrame(conn, &req)
		reqs <- req
		code := 0
		_ = agent.WriteFrame(conn, agent.Message{Type: "exit", Code: &code})
	})

	err := execInVM(context.Background(), "vm-req", vmExecOptions{
		socketDir: vm.SocketDir,
		command:   []string{"env"},
		env:       []string{"FOO=bar"},
		workdir:   "/srv",
		stdout:    &bytes.Buffer{},
		stderr:    &bytes.Buffer{},
		rawFD:     -1,
	})
	require.NoError(t, err)

	req := <-reqs
	assert.Equal(t, []string{"env"}, req.Command)
	assert.Equal(t, []string{"FOO=bar"}, req.Env)
	assert.Equal(t, "/srv", req.WorkDir)
	assert.False(t, req.TTY)
}

func TestVMExecTTYForwardsStdin(t *testing.T) {
	got := make(chan []byte, 1)
	vm := startFakeVM(t, "vm-tty", func(conn net.Conn) {
		var req agent.Request
		_, _ = agent.ReadFrame(conn, &req)
		// A TTY session carries stdin as raw bytes after the request frame.
		buf := make([]byte, len("hello"))
		_, _ = io.ReadFull(conn, buf)
		got <- buf
		code := 0
		_ = agent.WriteFrame(conn, agent.Message{Type: "exit", Code: &code})
	})

	err := execInVM(context.Background(), "vm-tty", vmExecOptions{
		socketDir: vm.SocketDir,
		command:   []string{"cat"},
		tty:       true,
		stdin:     strings.NewReader("hello"),
		stdout:    &bytes.Buffer{},
		stderr:    &bytes.Buffer{},
		rawFD:     -1,
	})
	require.NoError(t, err)
	assert.Equal(t, "hello", string(<-got))
}

// TestVMExecInteractiveWithoutTTYDoesNotForwardStdin asserts that -i alone
// leaves Stdin unset: a non-TTY exec with stdin set is rejected by the client,
// so a successful run proves stdin was not wired up.
func TestVMExecInteractiveWithoutTTYDoesNotForwardStdin(t *testing.T) {
	vm := startFakeVM(t, "vm-i", nil)

	cmd := newVMExecCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader("ignored input"))
	cmd.SetArgs([]string{"--socket-dir", vm.SocketDir, "-i", "vm-i", "--", "true"})

	require.NoError(t, cmd.Execute())
}

func TestVMExecUnknownVMFails(t *testing.T) {
	vm := startFakeVM(t, "vm-known", nil)

	err := execInVM(context.Background(), "nope", vmExecOptions{
		socketDir: vm.SocketDir,
		command:   []string{"true"},
		stdout:    &bytes.Buffer{},
		stderr:    &bytes.Buffer{},
		rawFD:     -1,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match any running VM")
}
