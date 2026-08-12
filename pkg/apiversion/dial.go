package apiversion

import (
	"context"
	"fmt"
	"net"

	"crypto/tls"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// DialUnix connects to a SwarmKit Unix socket with API version metadata
// injected into all outgoing RPCs. This is the standard way to connect
// from swarmcracker CLI and swarmctl to the manager daemon.
func DialUnix(socketPath string, tlsConfig *tls.Config) (*grpc.ClientConn, error) {
	opts := []grpc.DialOption{
		VersionDialOption(),
	}
	if tlsConfig != nil {
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
	} else {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	opts = append(opts, grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	}))

	conn, err := grpc.Dial("unix://"+socketPath, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to swarm socket %s: %w", socketPath, err)
	}
	return conn, nil
}

// VersionDialOption returns a gRPC DialOption that injects API version
// metadata into all outgoing unary RPCs on the connection.
func VersionDialOption() grpc.DialOption {
	return grpc.WithUnaryInterceptor(VersionClientInterceptor())
}
