// This file owns the two listeners the manager API binds: a trusted Unix socket
// whose file mode is the credential, and a TLS TCP address whose requests must
// carry a bearer token. Listen binds them and Serve runs them until its context
// is canceled.
package apihttp

import (
	"context"
	"crypto/tls"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/output"
)

const (
	// unixSocketMode makes the socket owner-only: the file mode is the
	// credential for the trusted listener.
	unixSocketMode fs.FileMode = 0o600
	// unixDirMode is the mode of a socket directory Serve creates.
	unixDirMode fs.FileMode = 0o700

	// shutdownGrace bounds how long Serve waits for in-flight requests once its
	// context is canceled.
	shutdownGrace = 5 * time.Second
	// readHeaderTimeout bounds the request line and headers on both listeners,
	// so a stalled client cannot hold a connection open for free.
	readHeaderTimeout = 10 * time.Second
)

// boundServer is one listener and the server serving it.
type boundServer struct {
	server   *http.Server
	listener net.Listener
}

// Listeners is a set of bound listeners that Serve has not started yet.
//
// Binding is separate from serving so a caller can learn about a busy port or an
// occupied socket path before it announces the API as running: Listen returns
// the bind error, and Serve only starts after Listen succeeded.
type Listeners struct {
	bound []boundServer
}

// Serve binds the configured addresses and serves them until ctx is canceled.
//
// The Unix listener serves UnixHandler (no token check, mode 0600); the TCP
// listener serves Handler over TLS using tls.crt and tls.key from Config.CertDir.
// An empty address skips that listener; an empty pair is an error.
//
// Serve returns nil when ctx cancellation ended the listeners, and an error that
// names the address when either listener could not be bound. Use Listen plus
// (*Listeners).Serve when the caller needs the bind error before serving.
func (s *Server) Serve(ctx context.Context, unixPath, tcpAddr string) error {
	listeners, err := s.Listen(ctx, unixPath, tcpAddr)
	if err != nil {
		return err
	}
	return listeners.Serve(ctx)
}

// Listen binds unixPath and tcpAddr without accepting a request. It returns the
// bound listeners, which the caller must hand to Serve or close with Close.
func (s *Server) Listen(ctx context.Context, unixPath, tcpAddr string) (*Listeners, error) {
	if unixPath == "" && tcpAddr == "" {
		return nil, output.NewError(output.CodeInvalid, "apihttp: Serve needs at least one listen address")
	}

	bound, err := s.bind(ctx, unixPath, tcpAddr)
	if err != nil {
		return nil, err
	}
	return &Listeners{bound: bound}, nil
}

// Serve serves the bound listeners until ctx is canceled.
//
// Cancellation stops both listeners and returns nil. If one listener stops
// serving first, the other is shut down too and its error is returned.
func (l *Listeners) Serve(ctx context.Context) error {
	bound := l.bound
	defer l.Close()

	errCh := make(chan error, len(bound))
	for _, b := range bound {
		go func(b boundServer) { errCh <- b.server.Serve(b.listener) }(b)
	}

	var serveErr error
	pending := len(bound)
	select {
	case <-ctx.Done():
		// A normal shutdown: both listeners are stopped below.
	case err := <-errCh:
		pending--
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
	}

	// One listener failing tears the other down too, so Serve never leaves half
	// the API running. Shutdown closes both listeners and gives in-flight
	// requests up to shutdownGrace to finish; a handler that keeps working past
	// that is aborted because every request context derives from ctx
	// (BaseContext), which cancellation has already ended. Shutdown errors are
	// deliberately ignored: the listeners are going away regardless.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	for _, b := range bound {
		_ = b.server.Shutdown(shutdownCtx)
	}
	for ; pending > 0; pending-- {
		<-errCh
	}

	return serveErr
}

// Close closes every listener. It is for a caller that bound with Listen and
// then decided not to serve; Serve already closes what it was given.
func (l *Listeners) Close() error {
	var firstErr error
	for _, b := range l.bound {
		if err := b.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) && firstErr == nil {
			firstErr = err
		}
	}
	l.bound = nil
	return firstErr
}

// bind opens both listeners before either serves a request, so a partial bind
// never leaves a live listener behind. Both servers inherit ctx as their base
// context, so cancellation ends a request handler that outlives the drain.
func (s *Server) bind(ctx context.Context, unixPath, tcpAddr string) ([]boundServer, error) {
	bound := make([]boundServer, 0, 2)

	if unixPath != "" {
		lis, err := listenUnix(ctx, unixPath)
		if err != nil {
			return nil, err
		}
		bound = append(bound, boundServer{
			server: &http.Server{
				Handler:           s.UnixHandler(),
				ReadHeaderTimeout: readHeaderTimeout,
				BaseContext:       func(net.Listener) context.Context { return ctx },
			},
			listener: lis,
		})
	}

	if tcpAddr != "" {
		cfg, err := s.tlsConfig()
		if err != nil {
			closeListeners(bound)
			return nil, err
		}
		lis, err := listenTCP(ctx, tcpAddr, cfg)
		if err != nil {
			closeListeners(bound)
			return nil, err
		}
		bound = append(bound, boundServer{
			server: &http.Server{
				Handler:           s.Handler(),
				ReadHeaderTimeout: readHeaderTimeout,
				BaseContext:       func(net.Listener) context.Context { return ctx },
			},
			listener: lis,
		})
	}

	return bound, nil
}

// tlsConfig loads the TLS listener's certificate from Config.CertDir.
func (s *Server) tlsConfig() (*tls.Config, error) {
	dir := strings.TrimSpace(s.cfg.CertDir)
	if dir == "" {
		return nil, output.NewError(output.CodeInvalid,
			"apihttp: Config.CertDir must be set to serve TCP")
	}

	cert, err := tls.LoadX509KeyPair(
		filepath.Join(dir, tlsCertFileName),
		filepath.Join(dir, tlsKeyFileName),
	)
	if err != nil {
		return nil, output.NewErrorf(output.CodeInternal,
			"load API TLS certificate from %s: %v", dir, err)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// listenUnix binds path and narrows it to owner-only access. Every failure names
// the socket path, which is the address the caller configured.
func listenUnix(ctx context.Context, path string) (net.Listener, error) {
	lis, err := bindUnix(ctx, path)
	if err != nil {
		return nil, bindError(path, err)
	}
	return lis, nil
}

func bindUnix(ctx context.Context, path string) (net.Listener, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, unixDirMode); err != nil {
		return nil, err
	}
	if err := removeStaleSocket(path); err != nil {
		return nil, err
	}

	var lc net.ListenConfig
	lis, err := lc.Listen(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, unixSocketMode); err != nil {
		_ = lis.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return lis, nil
}

// removeStaleSocket deletes a socket left behind by a crashed daemon. Anything
// that is not a socket is refused rather than deleted: the path came from an
// operator and might be a typo over a real file.
func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return output.NewErrorf(output.CodeInvalid,
			"refusing to replace %s: it exists and is not a socket", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// listenTCP binds addr behind cfg. TLS is negotiated per connection, so a
// handshake failure never closes the listener.
func listenTCP(ctx context.Context, addr string, cfg *tls.Config) (net.Listener, error) {
	var lc net.ListenConfig
	lis, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, bindError("TCP address "+addr, err)
	}
	return tls.NewListener(lis, cfg), nil
}

// bindError names the address a bind failed on. A typed error keeps its code:
// refusing to replace a non-socket path is invalid, not internal, and rewrapping
// it would hide that from the caller.
func bindError(addr string, err error) error {
	var typed *output.Error
	if errors.As(err, &typed) {
		return typed
	}
	return output.NewErrorf(output.CodeInternal, "listen on %s: %v", addr, err)
}

// closeListeners releases every listener a failed bind opened.
func closeListeners(bound []boundServer) {
	for _, b := range bound {
		_ = b.listener.Close()
	}
}
