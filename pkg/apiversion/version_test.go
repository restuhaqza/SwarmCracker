package apiversion

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestVersionClientInterceptor_InjectsMetadata(t *testing.T) {
	interceptor := VersionClientInterceptor()
	require.NotNil(t, interceptor)

	var captured metadata.MD
	invokerCalled := false

	// A fake invoker that reads the version metadata from the outgoing context.
	invoker := func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, opts ...grpc.CallOption) error {
		invokerCalled = true
		md, ok := metadata.FromOutgoingContext(ctx)
		require.True(t, ok, "expected outgoing metadata on context")
		captured = md
		return nil
	}

	// The interceptor does not touch the *grpc.ClientConn, so nil is fine here.
	err := interceptor(context.Background(), "/swarmcracker.Some/Service", "request", "reply", nil, invoker)
	require.NoError(t, err)

	assert.True(t, invokerCalled, "expected invoker to be called")
	require.NotNil(t, captured)
	assert.Equal(t, []string{Current}, captured.Get("x-swarmcracker-version"))
}

func TestWithVersion_ReturnsDialOption(t *testing.T) {
	opt := WithVersion()
	assert.NotNil(t, opt)
}
