package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
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
