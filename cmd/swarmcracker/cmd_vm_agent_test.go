package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/stretchr/testify/require"
)

// shortSocketDir creates a temporary directory with a short path. Unix socket
// paths are limited to ~104 bytes, which t.TempDir() can exceed.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sccli-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// fakeVM is an in-process stand-in for a running microVM: a liveness socket
// that answers the discovery probe plus a vsock proxy that serves the guest
// agent protocol.
type fakeVM struct {
	SocketDir string
	TaskID    string
}

// startFakeVM creates a discoverable "running VM" backed by an in-process guest
// agent. When script is non-nil it replaces the agent's request loop after the
// hello exchange, which lets a test observe or script individual frames.
func startFakeVM(t *testing.T, taskID string, script func(net.Conn)) *fakeVM {
	t.Helper()
	dir := shortSocketDir(t)

	// Firecracker API socket: runtime.DiscoverRunningVMs probes
	// GET /machine-config and treats a 200 as "running".
	apiLn, err := net.Listen("unix", filepath.Join(dir, taskID+".sock"))
	require.NoError(t, err)
	apiSrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})}
	go func() { _ = apiSrv.Serve(apiLn) }()
	t.Cleanup(func() { _ = apiSrv.Close() })

	// Firecracker vsock Unix-socket proxy: agent.Dial dials "<uds>_<port>".
	vsockPath := fmt.Sprintf("%s_%d", filepath.Join(dir, taskID+".vsock"), agent.DefaultPort)
	vsockLn, err := net.Listen("unix", vsockPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = vsockLn.Close() })

	go func() {
		for {
			conn, err := vsockLn.Accept()
			if err != nil {
				return
			}
			go serveFakeVMConn(conn, script)
		}
	}()

	return &fakeVM{SocketDir: dir, TaskID: taskID}
}

// serveFakeVMConn performs the CONNECT/OK handshake and then either runs the
// real agent request loop or hands the connection to script.
func serveFakeVMConn(conn net.Conn, script func(net.Conn)) {
	defer func() { _ = conn.Close() }()

	line, err := readProxyLine(conn)
	if err != nil {
		return
	}
	if line != fmt.Sprintf("CONNECT %d", agent.DefaultPort) {
		_, _ = io.WriteString(conn, "ERR wrong port\n")
		return
	}
	if _, err := fmt.Fprintf(conn, "OK %d\n", agent.DefaultPort); err != nil {
		return
	}

	if script == nil {
		_ = agent.ServeConn(conn)
		return
	}

	// Complete the hello exchange, then run the script.
	var req agent.Request
	if _, err := agent.ReadFrame(conn, &req); err != nil {
		return
	}
	if err := agent.WriteFrame(conn, agent.Message{Type: "hello", Version: agent.Version}); err != nil {
		return
	}
	script(conn)
}

// readProxyLine reads one '\n'-terminated line from the vsock proxy handshake.
func readProxyLine(r io.Reader) (string, error) {
	var line []byte
	buf := make([]byte, 1)
	for len(line) < 64 {
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", err
		}
		if buf[0] == '\n' {
			return string(line), nil
		}
		line = append(line, buf[0])
	}
	return "", fmt.Errorf("handshake line too long")
}
