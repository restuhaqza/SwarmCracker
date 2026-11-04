package main

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startAgentFakeVM creates a discoverable running VM whose guest is served by
// serve, which owns the whole agent exchange including hello. It does not reuse
// startFakeVM because that helper always completes the v2 hello itself, and the
// cases here are guests whose handshake must differ.
func startAgentFakeVM(t *testing.T, taskID string, serve func(net.Conn)) *fakeVM {
	t.Helper()
	dir := shortSocketDir(t)

	// The Firecracker API socket, which is what makes the VM discoverable.
	apiLn, err := net.Listen("unix", filepath.Join(dir, taskID+".sock"))
	require.NoError(t, err)
	apiSrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})}
	go func() { _ = apiSrv.Serve(apiLn) }()
	t.Cleanup(func() { _ = apiSrv.Close() })

	vsockLn, err := net.Listen("unix", filepath.Join(dir, taskID+".vsock"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = vsockLn.Close() })

	go func() {
		for {
			conn, err := vsockLn.Accept()
			if err != nil {
				return
			}
			go serve(conn)
		}
	}()

	return &fakeVM{SocketDir: dir, TaskID: taskID}
}

// serveLegacyAgent plays the muxer handshake and then a pre-v2 agent refusing a
// v2 host: it compares the version string, fails, and answers a bare error frame
// carrying no typed code.
func serveLegacyAgent(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	line, err := readProxyLine(conn)
	if err != nil || line != fmt.Sprintf("CONNECT %d", agent.DefaultPort) {
		return
	}
	if _, err := fmt.Fprintf(conn, "OK %d\n", 1073741824); err != nil {
		return
	}
	var req agent.Request
	if _, err := agent.ReadFrame(conn, &req); err != nil {
		return
	}
	_ = agent.WriteFrame(conn, agent.Message{
		Type:  "error",
		Error: "agent version mismatch: client " + agent.Version + ", guest " + agent.VersionLegacy,
	})
}

// TestDoctorAgentProtocolCheckReportsOutdatedGuest proves the check finds the
// case that is otherwise invisible: a VM that is running and serving traffic
// while its guest agent predates this host.
func TestDoctorAgentProtocolCheckReportsOutdatedGuest(t *testing.T) {
	vm := startAgentFakeVM(t, "task-old", serveLegacyAgent)

	c := doctorAgentProtocolCheck(vm.SocketDir, doctorStateDir)

	assert.Equal(t, "warning", c.Status)
	assert.Contains(t, c.Message, "task-old", "the message must name the task an operator has to restart")
	assert.Contains(t, c.Message, "older agent protocol")
	assert.NotEmpty(t, c.FixHint)
}

// TestDoctorAgentProtocolCheckPassesCurrentGuest keeps the check quiet on a node
// whose guests speak the current protocol.
func TestDoctorAgentProtocolCheckPassesCurrentGuest(t *testing.T) {
	vm := startFakeVM(t, "task-new", nil) // nil script: the real agent serves

	c := doctorAgentProtocolCheck(vm.SocketDir, doctorStateDir)

	assert.Equal(t, "ok", c.Status)
	assert.Contains(t, c.Message, "1 of 1")
	assert.Contains(t, c.Message, "protocol "+agent.Version)
	assert.Empty(t, c.FixHint)
}

// TestDoctorAgentProtocolCheckWithoutVMs skips rather than warns when there is
// nothing to ask.
func TestDoctorAgentProtocolCheckWithoutVMs(t *testing.T) {
	c := doctorAgentProtocolCheck(shortSocketDir(t), doctorStateDir)

	assert.Equal(t, "skip", c.Status)
	assert.Contains(t, c.Message, "no running VMs")
}

// TestDoctorAgentProtocolCheckAcceptsAnAuthenticatedGuest proves a token-bearing
// guest that proves the token is reported as healthy: authentication is not, by
// itself, a finding.
func TestDoctorAgentProtocolCheckAcceptsAnAuthenticatedGuest(t *testing.T) {
	vm := startAgentFakeVM(t, "task-auth", func(conn net.Conn) {
		serveAuthenticatedGuest(t, conn, true)
	})
	publishHostToken(t, vm.SocketDir, vm.TaskID, "boot-token")

	c := doctorAgentProtocolCheck(vm.SocketDir, doctorStateDir)

	assert.Equal(t, "ok", c.Status, c.Message)
	assert.Contains(t, c.Message, "1 of 1")
}

// TestDoctorAgentProtocolCheckReportsAnUnauthenticatedGuest proves the second
// bucket: the guest is current, this node cannot prove who it is, and the report
// names that instead of calling the guest outdated or silent.
func TestDoctorAgentProtocolCheckReportsAnUnauthenticatedGuest(t *testing.T) {
	vm := startAgentFakeVM(t, "task-auth", func(conn net.Conn) {
		serveAuthenticatedGuest(t, conn, false)
	})
	publishHostToken(t, vm.SocketDir, vm.TaskID, "boot-token")

	c := doctorAgentProtocolCheck(vm.SocketDir, doctorStateDir)

	assert.Equal(t, "warning", c.Status)
	assert.Contains(t, c.Message, "task-auth")
	assert.Contains(t, c.Message, "could not be authenticated")
	assert.NotContains(t, c.Message, "older agent protocol")
	assert.Contains(t, c.FixHint, "token")
}

// serveAuthenticatedGuest answers a v2 hello the way an agent with a token
// configured does: acceptToken is what it replies after verifying the token the
// host sent, and a refusal closes the connection with no frame at all.
func serveAuthenticatedGuest(t *testing.T, conn net.Conn, acceptToken bool) {
	t.Helper()
	defer func() { _ = conn.Close() }()

	line, err := readProxyLine(conn)
	if err != nil || line != fmt.Sprintf("CONNECT %d", agent.DefaultPort) {
		return
	}
	if _, err := fmt.Fprintf(conn, "OK %d\n", 1073741824); err != nil {
		return
	}
	var req agent.Request
	if _, err := agent.ReadFrame(conn, &req); err != nil {
		return
	}
	if !acceptToken {
		return // a rejected token gets no frame
	}
	reply := agent.HelloReply(agent.Capabilities())
	reply.ID = req.ID
	reply.AuthRequired = true
	reply.TokenOK = true
	_ = agent.WriteFrame(conn, reply)
}

// publishHostToken writes the host-side token next to the muxer, which is where
// Dial looks for it.
func publishHostToken(t *testing.T, socketDir, taskID, token string) {
	t.Helper()
	require.NoError(t, agent.WriteTokenFile(agent.TokenFilePath(scruntime.VsockPath(socketDir, taskID)), token))
}

// TestSummarizeAgentProbes pins the verdict for every mix a node can present,
// including the distinction the check exists to make: an outdated agent is a
// finding, a guest that did not answer is not.
func TestSummarizeAgentProbes(t *testing.T) {
	outdatedErr := &agent.Error{Code: agent.CodeProtocolUnsupported, Message: "hello: guest speaks 1"}
	unreachableErr := errors.New("dial vsock socket: no such file or directory")

	tests := []struct {
		name        string
		probes      []agentProbe
		wantStatus  string
		wantMessage string
		wantAbsent  string
	}{
		{
			name:        "no running VMs",
			wantStatus:  "skip",
			wantMessage: "no running VMs",
		},
		{
			name:        "all current",
			probes:      []agentProbe{{taskID: "a"}, {taskID: "b"}},
			wantStatus:  "ok",
			wantMessage: "2 of 2 running guest(s) speak protocol " + agent.Version,
		},
		{
			name:        "one outdated",
			probes:      []agentProbe{{taskID: "a"}, {taskID: "b", err: outdatedErr}},
			wantStatus:  "warning",
			wantMessage: "1 of 2 running guest(s) speak an older agent protocol: b",
		},
		{
			name:        "a token problem is its own bucket",
			probes:      []agentProbe{{taskID: "a"}, {taskID: "b", err: agent.ErrTokenRequired}},
			wantStatus:  "warning",
			wantMessage: "1 of 2 running guest(s) could not be authenticated by this node: b",
			wantAbsent:  "older agent protocol",
		},
		{
			name:        "a token-unsupported agent is outdated, not unauthenticated",
			probes:      []agentProbe{{taskID: "a", err: agent.ErrTokenUnsupported}},
			wantStatus:  "warning",
			wantMessage: "1 of 1 running guest(s) speak an older agent protocol: a",
			wantAbsent:  "could not be authenticated",
		},
		{
			name: "both problems are reported together",
			probes: []agentProbe{
				{taskID: "a", err: outdatedErr},
				{taskID: "b", err: agent.ErrTokenRejected},
			},
			wantStatus:  "warning",
			wantMessage: "2 of 2 running guest(s) need attention:",
		},
		{
			name:        "unreachable guest is not an upgrade problem",
			probes:      []agentProbe{{taskID: "a"}, {taskID: "b", err: unreachableErr}},
			wantStatus:  "ok",
			wantMessage: "1 of 2 running guest(s) speak protocol " + agent.Version,
			wantAbsent:  "older agent",
		},
		{
			name:        "nothing answered",
			probes:      []agentProbe{{taskID: "a", err: unreachableErr}},
			wantStatus:  "ok",
			wantMessage: "0 of 1",
			wantAbsent:  "older agent",
		},
		{
			name: "long list is truncated",
			probes: []agentProbe{
				{taskID: "a", err: outdatedErr}, {taskID: "b", err: outdatedErr},
				{taskID: "c", err: outdatedErr}, {taskID: "d", err: outdatedErr},
				{taskID: "e", err: outdatedErr}, {taskID: "f", err: outdatedErr},
				{taskID: "g", err: outdatedErr},
			},
			wantStatus:  "warning",
			wantMessage: "7 of 7 running guest(s) speak an older agent protocol: a, b, c, d, e and 2 more",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := summarizeAgentProbes(tc.probes, "/var/run/firecracker")

			assert.Equal(t, tc.wantStatus, c.Status)
			assert.Contains(t, c.Message, tc.wantMessage)
			if tc.wantAbsent != "" {
				assert.NotContains(t, c.Message, tc.wantAbsent)
			}
			if tc.wantStatus == "warning" {
				require.NotEmpty(t, c.FixHint, "a warning must tell the operator what to do")
			}
		})
	}
}
