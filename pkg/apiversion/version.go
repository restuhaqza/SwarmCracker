// Package apiversion provides gRPC metadata-based API versioning.
//
// Client side: VersionClientInterceptor injects X-SwarmCracker-Version metadata
// into every outgoing gRPC call.
//
// Server side: VersionFromContext extracts the version from incoming metadata.
//
// Version must be incremented on any breaking change to the gRPC protocol between
// swarmd-firecracker (manager/worker) and swarmcracker CLI / swarmctl.
package apiversion

import (
	"context"
	"fmt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// Current is the API version for the gRPC protocol between
// swarmd-firecracker and swarmcracker CLI / swarmctl.
//
// Version history:
//
//	1 — Initial schema (v0.8.0+): all v0.x releases share this version
const Current = "1"

// WithVersion returns a gRPC DialOption that injects the API version
// into all outgoing RPCs on the connection. Use this when dialing
// the SwarmCracker manager from CLI or swarmctl.
//
// Usage:
//
//	conn, err := grpc.Dial(addr, apiversion.WithVersion(), grpc.WithInsecure())
func WithVersion() grpc.DialOption {
	return grpc.WithUnaryInterceptor(VersionClientInterceptor())
}

// VersionClientInterceptor returns a gRPC unary client interceptor that
// injects the API version into outgoing request metadata.
func VersionClientInterceptor() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		md := metadata.Pairs("x-swarmcracker-version", Current)
		ctx = metadata.NewOutgoingContext(ctx, md)
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// VersionFromContext extracts the client API version from incoming gRPC metadata.
// Returns empty string if not present.
func VersionFromContext(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get("x-swarmcracker-version")
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

// ValidateVersion checks if the client version is compatible.
// Returns nil if compatible, or an error describing the mismatch.
// Currently accepts "1" and "" (backwards compatible — pre-versioning clients
// are assumed to be version 1 when accepted).
func ValidateVersion(ctx context.Context, minVersion string) error {
	v := VersionFromContext(ctx)
	if v == "" {
		// Pre-versioning client — assume compatible for now
		return nil
	}
	if v < minVersion {
		return fmt.Errorf("API version mismatch: client=v%s, server requires v%s. Please upgrade the client", v, minVersion)
	}
	return nil
}
