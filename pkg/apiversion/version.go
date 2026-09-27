// Package apiversion provides gRPC metadata-based API versioning.
//
// Client side: WithVersion / VersionClientInterceptor inject
// X-SwarmCracker-Version metadata into every outgoing gRPC call.
//
// Version must be incremented on any breaking change to the gRPC protocol between
// swarmd-firecracker (manager/worker) and swarmcracker CLI / swarmctl.
package apiversion

import (
	"context"

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
