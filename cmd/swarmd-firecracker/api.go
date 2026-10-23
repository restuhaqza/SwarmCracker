package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/moby/swarmkit/v2/log"
	"github.com/moby/swarmkit/v2/node"
	"github.com/restuhaqza/swarmcracker/pkg/apihttp"
	"github.com/restuhaqza/swarmcracker/pkg/apiversion"
	"github.com/restuhaqza/swarmcracker/pkg/broker"
	"github.com/restuhaqza/swarmcracker/pkg/client"
)

const (
	// defaultAPIListen is the loopback-only TCP address: exposing the API on
	// every interface is an explicit operator decision.
	defaultAPIListen = "127.0.0.1:4244"

	// defaultAPIDirName is the per-node directory holding the API certificate
	// and token store, relative to --state-dir.
	defaultAPIDirName = "api"
	// defaultAPISocketName is the trusted Unix socket name, relative to
	// --state-dir.
	defaultAPISocketName = "api.sock"
	// defaultAPIAuditName is the audit log name, relative to --state-dir.
	defaultAPIAuditName = "audit.jsonl"
	// defaultAPIAuditRetention is the contract's 30-day default.
	defaultAPIAuditRetention = apihttp.DefaultAuditRetention

	// apiStopTimeout bounds how long stop waits for the listeners to drain. It
	// exceeds apihttp's 5s shutdown grace so a slow drain is not misreported as a
	// hang.
	apiStopTimeout = 10 * time.Second
	// apiClusterIDTimeout bounds the best-effort cluster ID lookup. A manager
	// that is still forming its cluster must not delay the listeners.
	apiClusterIDTimeout = 5 * time.Second
)

// apiRuntime holds the daemon's programmatic API settings.
type apiRuntime struct {
	enabled        bool
	tcpAddr        string
	socket         string
	tokenFile      string
	certDir        string
	auditFile      string
	auditRetention time.Duration
	rateLimit      float64
	rateBurst      int
}

// startAPIForNode starts the programmatic API when config describes a manager.
// A worker has no local cluster state to read, so it never starts one, even when
// --api-disabled was not passed.
func startAPIForNode(ctx context.Context, cfg apiRuntime, config *node.Config) (func(), error) {
	if config.JoinAddr != "" {
		return func() {}, nil
	}
	return startAPI(ctx, cfg, config.StateDir, config.Hostname, config.ListenControlAPI)
}

// startAPI starts the manager's programmatic read API: a trusted Unix socket and
// a TLS TCP listener backed by the local control socket. It returns a stop
// function that is safe to call more than once.
//
// A disabled API returns a no-op stop and creates nothing. When the token file
// is missing the TCP listener starts with an empty store, so every TCP read
// answers 401 until an operator mints a token.
//
// The listeners are bound before startAPI returns, so a busy port or an occupied
// socket path is reported to the caller as a startup error rather than
// discovered later by a client or by the shutdown path.
func startAPI(ctx context.Context, cfg apiRuntime, stateDir, hostname, listenControlAPI string) (func(), error) {
	if !cfg.enabled {
		return func() {}, nil
	}

	store, err := apihttp.OpenTokenStore(cfg.tokenFile)
	if err != nil {
		return nil, err
	}

	// The audit log is opened before the certificate and the listeners: a bad
	// audit path must fail startup, not a write that arrives later.
	retention := cfg.auditRetention
	if retention <= 0 {
		retention = defaultAPIAuditRetention
	}
	audit, err := apihttp.OpenAuditStore(cfg.auditFile, retention)
	if err != nil {
		return nil, fmt.Errorf("open programmatic API audit log %s: %w", cfg.auditFile, err)
	}

	certFile, keyFile, err := apihttp.EnsureCertificate(cfg.certDir, hostname, apiCertHosts(cfg.tcpAddr))
	if err != nil {
		return nil, err
	}

	// The daemon runs on the manager, so it reads through the same local
	// control socket the CLI uses.
	c, err := client.New(client.Config{StateDir: stateDir, SocketPath: listenControlAPI})
	if err != nil {
		return nil, fmt.Errorf("dial manager control socket %s: %w", listenControlAPI, err)
	}

	// Best effort: a cluster that is not ready has no ID yet, and the version
	// probe must still answer.
	clusterID := ""
	idCtx, idCancel := context.WithTimeout(ctx, apiClusterIDTimeout)
	if id, idErr := c.ClusterID(idCtx); idErr == nil {
		clusterID = id
	}
	idCancel()

	// Exec is relayed through the manager-side broker. It reuses the node's
	// cluster certificate, the same identity the ingress mesh uses, so no user
	// token ever travels to a node. When the material is unavailable the API
	// still starts; exec then answers a typed 501 instead of failing startup.
	var apiBroker apihttp.Broker
	if mgr, brokerErr := newBrokerManager(stateDir, c, c); brokerErr != nil {
		log.G(ctx).WithError(brokerErr).Warn("Programmatic API: exec disabled (node broker manager unavailable)")
	} else {
		apiBroker = mgr
	}

	srv, err := apihttp.New(apihttp.Config{
		Client:        c,
		Broker:        apiBroker,
		Tokens:        store,
		Audit:         audit,
		CertDir:       cfg.certDir,
		ServerVersion: Version,
		GRPCVersion:   apiversion.Current,
		ClusterID:     clusterID,
		Capabilities:  apiCapabilities(apiBroker != nil),
		RateLimit:     apihttp.NewRateLimiter(cfg.rateLimit, cfg.rateBurst),
	})
	if err != nil {
		_ = c.Close()
		return nil, err
	}

	runCtx, cancel := context.WithCancel(ctx)
	listeners, err := srv.Listen(runCtx, cfg.socket, cfg.tcpAddr)
	if err != nil {
		cancel()
		_ = c.Close()
		return nil, err
	}

	// Log only what is actually bound, and only after the bind succeeded: the
	// caller reports the error above, and a log line must never claim a listener
	// that a client cannot reach.
	switch {
	case cfg.socket != "" && cfg.tcpAddr != "":
		log.G(ctx).Infof("Programmatic API: unix socket %s, TLS %s", cfg.socket, cfg.tcpAddr)
	case cfg.socket != "":
		log.G(ctx).Infof("Programmatic API: unix socket %s", cfg.socket)
	case cfg.tcpAddr != "":
		log.G(ctx).Infof("Programmatic API: TLS %s", cfg.tcpAddr)
	}
	if cfg.tcpAddr != "" {
		log.G(ctx).Infof("Programmatic API: certificate %s (key %s)", certFile, keyFile)
		log.G(ctx).Infof("Programmatic API: TCP reads need a bearer token; create one with " +
			"`swarmcracker api token create --name <name> --role reader`")
	}
	log.G(ctx).Infof("Programmatic API: audit log %s (retention %s)", cfg.auditFile, retention)

	done := make(chan error, 1)
	go func() { done <- listeners.Serve(runCtx) }()

	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					log.G(ctx).WithError(err).Warn("Programmatic API stopped with an error")
				}
			case <-time.After(apiStopTimeout):
				log.G(ctx).Warn("Programmatic API did not stop in time")
			}
			if err := c.Close(); err != nil {
				log.G(ctx).WithError(err).Debug("Failed to close the API control client")
			}
		})
	}

	return stop, nil
}

// apiCertHosts returns the host part of tcpAddr for the certificate SANs. A
// wildcard bind has no name a client can verify, so it adds nothing; 127.0.0.1
// and ::1 are always in the certificate anyway.
func apiCertHosts(tcpAddr string) []string {
	tcpAddr = strings.TrimSpace(tcpAddr)
	if tcpAddr == "" {
		return nil
	}

	host := tcpAddr
	if h, _, err := net.SplitHostPort(tcpAddr); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "" || host == "0.0.0.0" || host == "::" {
		return nil
	}
	return []string{host}
}

// newBrokerManager builds the manager side of the node broker: the transport
// that carries exec from the public API to the node running a task.
//
// It authenticates with the node's cluster certificate, so the node broker sees
// the manager's cluster identity and never a public bearer token. The public
// error hook is apihttp.WriteError, which is the only writer that knows the
// caller's request ID and the v1 media type.
func newBrokerManager(stateDir string, tasks broker.TaskReader, nodes broker.NodeReader) (*broker.Manager, error) {
	cert, caPool, err := loadIngressTLS(stateDir)
	if err != nil {
		return nil, fmt.Errorf("node broker: %w", err)
	}

	transport := &http.Transport{
		TLSClientConfig:     clientTLSConfig(cert, caPool),
		MaxIdleConns:        16,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}

	return broker.NewManager(broker.ManagerConfig{
		Tasks:         tasks,
		Nodes:         nodes,
		Client:        &http.Client{Transport: transport},
		ErrorWriter:   apihttp.WriteError,
		RequestIDFrom: apihttp.RequestIDFromContext,
	})
}

// apiCapabilities is the capability list the version endpoint advertises. Health
// is advertised only when the node broker manager is wired: the fan-out is the
// only path a health read takes, so a manager without it would promise a
// capability every health request answers 501 for.
func apiCapabilities(hasBroker bool) []string {
	capabilities := []string{"read", "write", "events", "exec", "logs", "files"}
	if hasBroker {
		capabilities = append(capabilities, "health")
	}
	return capabilities
}
