package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/broker"
	"github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"
)

// runBrokerFlags parses args through the daemon's real flag set and returns the
// resolved broker runtime.
func runBrokerFlags(t *testing.T, args ...string) brokerRuntime {
	t.Helper()

	var got brokerRuntime
	app := cli.NewApp()
	app.Flags = newAppFlags()
	app.Action = func(c *cli.Context) error {
		got = brokerRuntimeFrom(c)
		return nil
	}
	require.NoError(t, app.Run(append([]string{"swarmd-firecracker"}, args...)))
	return got
}

func TestBrokerRuntimeFrom_Defaults(t *testing.T) {
	cfg := runBrokerFlags(t)

	require.Equal(t, "0.0.0.0:4245", cfg.listenAddr)
	require.Equal(t, "/var/run/firecracker", cfg.socketDir)
	require.Equal(t, runtime.DefaultLogDir(), cfg.logDir,
		"the broker reads the same console logs the executor writes")
}

func TestBrokerRuntimeFrom_EmptyListenDisables(t *testing.T) {
	cfg := runBrokerFlags(t, "--broker-listen", "")

	require.Empty(t, cfg.listenAddr, "an empty --broker-listen disables the broker")
}

// TestRestoredStoreFollowsStateDir pins where the daemon keeps the
// restored-instance records: under the same --state-dir as the rest of the
// node's state, and a second open (a daemon restart) resolves what the first
// process wrote.
func TestRestoredStoreFollowsStateDir(t *testing.T) {
	stateDir := t.TempDir()
	socketDir := "/var/run/firecracker"
	record := runtime.RestoredInstance{
		TaskID:     "new-task",
		VMID:       "source-task",
		ArtifactID: "mig-0123456789abcdef",
		SocketPath: filepath.Join(socketDir, "new-task.sock"),
		TapName:    runtime.TapName("source-task", 0),
		VsockPath:  runtime.VsockPath(socketDir, "source-task"),
		DiskPath:   filepath.Join("/var/lib/swarmcracker/tasks", "source-task.ext4"),
		CreatedAt:  time.Now().UTC(),
	}

	first, err := restoredStore(stateDir)
	require.NoError(t, err)
	require.NoError(t, first.Put(record))

	info, err := os.Stat(filepath.Join(stateDir, runtime.RestoredInstanceDirName))
	require.NoError(t, err)
	require.True(t, info.IsDir(), "the records live in their own subdirectory of --state-dir")

	second, err := restoredStore(stateDir)
	require.NoError(t, err)

	got, found, err := second.Get(record.TaskID)
	require.NoError(t, err)
	require.True(t, found, "a restarted daemon must find the record the previous process wrote")
	require.Equal(t, record.VMID, got.VMID)
	require.Equal(t, record.VsockPath, got.VsockPath)
}

func TestRestoredStoreNeedsStateDir(t *testing.T) {
	reg, err := restoredStore("")
	require.Error(t, err)
	require.Nil(t, reg)
	require.Contains(t, err.Error(), "state-dir")
}

func TestBrokerRuntimeFrom_Overrides(t *testing.T) {
	cfg := runBrokerFlags(t,
		"--broker-listen", "127.0.0.1:5252",
		"--socket-dir", "/srv/firecracker",
	)

	require.Equal(t, "127.0.0.1:5252", cfg.listenAddr)
	require.Equal(t, "/srv/firecracker", cfg.socketDir)
}

// seedBrokerCertificates writes the cluster node certificate, key and root CA
// into <stateDir>/certificates the way the SwarmKit node does, and returns the
// pair a test client must present.
//
// The production cluster certificate is a single identity used for both server
// and client authentication, so the leaf carries both EKUs.
func seedBrokerCertificates(t *testing.T, stateDir string) (tls.Certificate, *x509.CertPool) {
	t.Helper()

	now := time.Now()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "swarmcracker-broker-test-ca"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	caCert, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "swarmcracker-broker-test-node"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
		DNSNames:    []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(leafDER)
	require.NoError(t, err)

	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	require.NoError(t, err)

	certDir := filepath.Join(stateDir, "certificates")
	require.NoError(t, os.MkdirAll(certDir, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(certDir, "swarm-node.crt"),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		0o600,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(certDir, "swarm-node.key"),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		0o600,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(certDir, "swarm-root-ca.crt"),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		0o644,
	))

	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	return tls.Certificate{
		Certificate: [][]byte{leafDER},
		PrivateKey:  leafKey,
		Leaf:        leaf,
	}, pool
}

// brokerTestClient dials with the cluster certificate and verifies the node
// chain against the cluster CA.
func brokerTestClient(cert tls.Certificate, pool *x509.CertPool) *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			TLSClientConfig: &tls.Config{
				Certificates: []tls.Certificate{cert},
				RootCAs:      pool,
				MinVersion:   tls.VersionTLS12,
			},
		},
	}
}

func TestStartBroker_DisabledIsNoOp(t *testing.T) {
	srv, _, stop, err := startBroker(context.Background(), brokerRuntime{}, t.TempDir())
	require.NoError(t, err)
	require.Nil(t, srv)
	require.NotNil(t, stop)

	stop()
	stop() // idempotent
}

func TestStartBroker_RequiresCertificates(t *testing.T) {
	cfg := brokerRuntime{listenAddr: "127.0.0.1:0", socketDir: t.TempDir()}

	srv, _, stop, err := startBroker(context.Background(), cfg, t.TempDir())
	require.Error(t, err)
	require.Nil(t, srv)
	require.Nil(t, stop)
}

// TestStartBroker_ServesLogs proves the node registers the log operation and
// reads the log directory the executor writes: an mTLS GET returns the console
// backlog as NDJSON and ends with eof.
func TestStartBroker_ServesLogs(t *testing.T) {
	stateDir := t.TempDir()
	clientCert, pool := seedBrokerCertificates(t, stateDir)
	socketDir := t.TempDir()
	logDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(socketDir, "task-1.sock"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(logDir, "task-1.log"),
		[]byte("console line\n"), 0o600))

	cfg := brokerRuntime{listenAddr: "127.0.0.1:0", socketDir: socketDir, logDir: logDir}
	srv, _, stop, err := startBroker(context.Background(), cfg, stateDir)
	require.NoError(t, err)
	defer stop()

	client := brokerTestClient(clientCert, pool)
	defer client.CloseIdleConnections()

	resp, err := client.Get("https://" + srv.Addr() + "/v1/tasks/task-1/logs")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "application/x-ndjson", resp.Header.Get("Content-Type"))

	frames := []v1.LogFrame{}
	decoder := json.NewDecoder(resp.Body)
	for {
		var frame v1.LogFrame
		if err := decoder.Decode(&frame); err != nil {
			break
		}
		frames = append(frames, frame)
	}
	require.Len(t, frames, 2)
	require.Equal(t, v1.LogFrameTypeLog, frames[0].Type)
	require.Equal(t, []byte("console line\n"), frames[0].Data)
	require.Equal(t, v1.LogFrameTypeEOF, frames[1].Type)
}

// TestStartBroker_RegistersFiles proves the file operation is registered: a task
// with no guest muxer answers the broker's unavailable rather than the server's
// not-implemented.
func TestStartBroker_RegistersFiles(t *testing.T) {
	stateDir := t.TempDir()
	clientCert, pool := seedBrokerCertificates(t, stateDir)
	socketDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(socketDir, "task-1.sock"), nil, 0o600))

	cfg := brokerRuntime{listenAddr: "127.0.0.1:0", socketDir: socketDir, logDir: t.TempDir()}
	srv, _, stop, err := startBroker(context.Background(), cfg, stateDir)
	require.NoError(t, err)
	defer stop()

	client := brokerTestClient(clientCert, pool)
	defer client.CloseIdleConnections()

	resp, err := client.Get("https://" + srv.Addr() + "/v1/tasks/task-1/files?path=/etc/hostname")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode,
		"files must be registered, not 501")
}

// TestStartBroker_RegistersAttach proves the daemon registers the attach
// operation and derives the console socket from --socket-dir: a running task
// with no console socket answers the handler's typed 404 console_unavailable,
// not the server's 501 for an unregistered operation.
func TestStartBroker_RegistersAttach(t *testing.T) {
	stateDir := t.TempDir()
	clientCert, pool := seedBrokerCertificates(t, stateDir)
	socketDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(socketDir, "task-1.sock"), nil, 0o600))

	cfg := brokerRuntime{listenAddr: "127.0.0.1:0", socketDir: socketDir, logDir: t.TempDir()}
	srv, _, stop, err := startBroker(context.Background(), cfg, stateDir)
	require.NoError(t, err)
	defer stop()

	client := brokerTestClient(clientCert, pool)
	defer client.CloseIdleConnections()

	resp, err := client.Post("https://"+srv.Addr()+"/v1/tasks/task-1/attach",
		"application/octet-stream", strings.NewReader("x"))
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusNotFound, resp.StatusCode,
		"attach must be registered, not 501; the task has no console socket")
	var doc v1.ErrorDocument
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&doc))
	require.Equal(t, "not_found", doc.Error.Code)
	require.Equal(t, "console_unavailable", doc.Error.Details["reason"])
}

// TestStartBroker_LogsUnavailableTaskIs404 pins the daemon's not-found answer for
// a task whose console log was never written.
func TestStartBroker_LogsUnavailableTaskIs404(t *testing.T) {
	stateDir := t.TempDir()
	clientCert, pool := seedBrokerCertificates(t, stateDir)
	socketDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(socketDir, "task-1.sock"), nil, 0o600))

	cfg := brokerRuntime{listenAddr: "127.0.0.1:0", socketDir: socketDir, logDir: t.TempDir()}
	srv, _, stop, err := startBroker(context.Background(), cfg, stateDir)
	require.NoError(t, err)
	defer stop()

	client := brokerTestClient(clientCert, pool)
	defer client.CloseIdleConnections()

	resp, err := client.Get("https://" + srv.Addr() + "/v1/tasks/task-1/logs")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// TestStartBroker_ServesLogsForExitedTask pins the daemon wiring for a task that
// has stopped: its socket is gone, so it resolves as a log-only task. Logs stay
// readable; files needs a live guest and is a typed 404 rather than a dial.
func TestStartBroker_ServesLogsForExitedTask(t *testing.T) {
	stateDir := t.TempDir()
	clientCert, pool := seedBrokerCertificates(t, stateDir)
	socketDir := t.TempDir()
	logDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(logDir, "task-1.log"),
		[]byte("boom\n"), 0o600))

	cfg := brokerRuntime{listenAddr: "127.0.0.1:0", socketDir: socketDir, logDir: logDir}
	srv, _, stop, err := startBroker(context.Background(), cfg, stateDir)
	require.NoError(t, err)
	defer stop()

	client := brokerTestClient(clientCert, pool)
	defer client.CloseIdleConnections()

	resp, err := client.Get("https://" + srv.Addr() + "/v1/tasks/task-1/logs")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"a task's console log must stay readable after it exits")

	filesResp, err := client.Get("https://" + srv.Addr() + "/v1/tasks/task-1/files?path=/etc/hostname")
	require.NoError(t, err)
	defer filesResp.Body.Close()
	require.Equal(t, http.StatusNotFound, filesResp.StatusCode,
		"files has no guest for a task that is not running")
}

func TestStartBroker_ServesMTLSAndStops(t *testing.T) {
	stateDir := t.TempDir()
	clientCert, pool := seedBrokerCertificates(t, stateDir)
	socketDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(socketDir, "task-1.sock"), nil, 0o600))

	cfg := brokerRuntime{listenAddr: "127.0.0.1:0", socketDir: socketDir}
	srv, _, stop, err := startBroker(context.Background(), cfg, stateDir)
	require.NoError(t, err)
	require.NotNil(t, srv)
	require.NotNil(t, stop)

	// Every real operation (exec, logs, files, health) is registered by
	// startBroker, so this test registers a probe operation instead: the point is
	// that the registry dispatches to a custom handler with the resolved task
	// over mTLS.
	require.NoError(t, srv.Handle("probe", func(_ context.Context, task broker.Task, w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "task="+task.ID)
	}))

	client := brokerTestClient(clientCert, pool)
	resp, err := client.Get("https://" + srv.Addr() + "/v1/tasks/task-1/probe")
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "task=task-1", string(body))

	stop()
	stop() // idempotent

	client.CloseIdleConnections()
	_, err = client.Get("https://" + srv.Addr() + "/v1/tasks/task-1/probe")
	require.Error(t, err, "the stop function must shut the broker down")
}

// fakeVsockAgent speaks enough of the Firecracker vsock muxer and guest-agent
// protocol to answer one exec. It lets the daemon test prove the exec operation
// is registered and streams without a real microVM.
func fakeVsockAgent(t *testing.T, ln net.Listener, stdout string, code int) {
	t.Helper()

	conn, err := ln.Accept()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()

	reader := bufio.NewReader(conn)
	if _, err := reader.ReadString('\n'); err != nil {
		return
	}
	if _, err := io.WriteString(conn, "OK 1\n"); err != nil {
		return
	}

	var hello agent.Request
	if _, err := agent.ReadFrame(reader, &hello); err != nil {
		return
	}
	if err := agent.WriteFrame(conn, agent.Message{Type: "hello", Version: agent.Version}); err != nil {
		return
	}

	var req agent.Request
	if _, err := agent.ReadFrame(reader, &req); err != nil {
		return
	}
	if err := agent.WriteFrame(conn, agent.Message{Type: "stdout", Data: []byte(stdout)}); err != nil {
		return
	}
	_ = agent.WriteFrame(conn, agent.Message{Type: "exit", Code: &code})
}

// TestStartBroker_ExecStreamsFromTheGuest proves the node registers the exec
// operation: an mTLS POST reaches the guest agent and comes back as NDJSON.
func TestStartBroker_ExecStreamsFromTheGuest(t *testing.T) {
	stateDir := t.TempDir()
	clientCert, pool := seedBrokerCertificates(t, stateDir)
	socketDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(socketDir, "task-1.sock"), nil, 0o600))

	vsockPath := filepath.Join(socketDir, "task-1.vsock")
	ln, err := net.Listen("unix", vsockPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go fakeVsockAgent(t, ln, "hello from guest\n", 0)

	cfg := brokerRuntime{listenAddr: "127.0.0.1:0", socketDir: socketDir}
	srv, _, stop, err := startBroker(context.Background(), cfg, stateDir)
	require.NoError(t, err)
	defer stop()

	client := brokerTestClient(clientCert, pool)
	defer client.CloseIdleConnections()

	resp, err := client.Post("https://"+srv.Addr()+"/v1/tasks/task-1/exec",
		"application/json", strings.NewReader(`{"command":["echo","hi"]}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "application/x-ndjson", resp.Header.Get("Content-Type"))

	decoder := json.NewDecoder(resp.Body)
	var frames []v1.ExecFrame
	for {
		var frame v1.ExecFrame
		if err := decoder.Decode(&frame); err == io.EOF {
			break
		} else {
			require.NoError(t, err)
		}
		frames = append(frames, frame)
	}

	require.Len(t, frames, 2)
	require.Equal(t, v1.FrameStdout, frames[0].Type)
	require.Equal(t, []byte("hello from guest\n"), frames[0].Data)
	require.Equal(t, v1.FrameExit, frames[1].Type)
	require.NotNil(t, frames[1].ExitCode)
	require.Equal(t, 0, *frames[1].ExitCode)
}

// TestStartBroker_RegistersHealth proves the health operation is registered and
// reads the sidecar the executor publishes next to the VM socket: the node
// answers live health, not 501.
func TestStartBroker_RegistersHealth(t *testing.T) {
	stateDir := t.TempDir()
	clientCert, pool := seedBrokerCertificates(t, stateDir)
	socketDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(socketDir, "task-1.sock"), nil, 0o600))
	require.NoError(t, runtime.WriteVMMetadata(socketDir, &runtime.VMMetadata{
		ID: "task-1",
		Health: &runtime.VMHealth{
			State:         runtime.HealthFailing,
			FailingStreak: 2,
			Output:        "connection refused",
			Command:       "curl -fsS http://127.0.0.1/",
			UpdatedAt:     time.Now().UTC(),
		},
	}))

	cfg := brokerRuntime{listenAddr: "127.0.0.1:0", socketDir: socketDir, logDir: t.TempDir()}
	srv, _, stop, err := startBroker(context.Background(), cfg, stateDir)
	require.NoError(t, err)
	defer stop()

	client := brokerTestClient(clientCert, pool)
	defer client.CloseIdleConnections()

	resp, err := client.Get("https://" + srv.Addr() + "/v1/tasks/task-1/health")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "health must be registered, not 501")

	var got v1.TaskHealth
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.Equal(t, "failing", got.Status)
	require.Equal(t, 2, got.FailingStreak)
	require.Equal(t, "connection refused", got.Output)
}

// TestStartBroker_HealthWithoutSidecarIs204 proves a task that publishes no
// healthcheck is 204, not an error: the metadata file is simply absent.
func TestStartBroker_HealthWithoutSidecarIs204(t *testing.T) {
	stateDir := t.TempDir()
	clientCert, pool := seedBrokerCertificates(t, stateDir)
	socketDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(socketDir, "task-1.sock"), nil, 0o600))

	cfg := brokerRuntime{listenAddr: "127.0.0.1:0", socketDir: socketDir, logDir: t.TempDir()}
	srv, _, stop, err := startBroker(context.Background(), cfg, stateDir)
	require.NoError(t, err)
	defer stop()

	client := brokerTestClient(clientCert, pool)
	defer client.CloseIdleConnections()

	resp, err := client.Get("https://" + srv.Addr() + "/v1/tasks/task-1/health")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
}
