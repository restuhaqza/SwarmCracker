package main

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/moby/swarmkit/v2/log"
	"github.com/restuhaqza/swarmcracker/pkg/apihttp"
	"github.com/restuhaqza/swarmcracker/pkg/apiversion"
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
	enabled   bool
	tcpAddr   string
	socket    string
	tokenFile string
	certDir   string
}

// startAPI starts the manager's programmatic read API: a trusted Unix socket and
// a TLS TCP listener backed by the local control socket. It returns a stop
// function that is safe to call more than once.
//
// A disabled API returns a no-op stop and creates nothing. When the token file
// is missing the TCP listener starts with an empty store, so every TCP read
// answers 401 until an operator mints a token.
func startAPI(ctx context.Context, cfg apiRuntime, stateDir, hostname, listenControlAPI string) (func(), error) {
	if !cfg.enabled {
		return func() {}, nil
	}

	store, err := apihttp.OpenTokenStore(cfg.tokenFile)
	if err != nil {
		return nil, err
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

	srv, err := apihttp.New(apihttp.Config{
		Client:        c,
		Tokens:        store,
		CertDir:       cfg.certDir,
		ServerVersion: Version,
		GRPCVersion:   apiversion.Current,
		ClusterID:     clusterID,
		Capabilities:  []string{"read"},
	})
	if err != nil {
		_ = c.Close()
		return nil, err
	}

	log.G(ctx).Infof("Programmatic API: unix socket %s, TLS %s", cfg.socket, cfg.tcpAddr)
	log.G(ctx).Infof("Programmatic API: certificate %s (key %s)", certFile, keyFile)
	log.G(ctx).Infof("Programmatic API: TCP reads need a bearer token; create one with " +
		"`swarmcracker api token create --name <name> --role reader`")

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- srv.Serve(runCtx, cfg.socket, cfg.tcpAddr) }()

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
