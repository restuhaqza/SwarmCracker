//go:build !integration

package agent

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withProcFixture points the server's metrics reader at a fixture /proc for the
// duration of a test. It also proves the reader really goes through procRoot
// rather than a hard-coded path.
func withProcFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeProcFixture(t, dir, fixtureStat, fixtureMeminfo, fixtureNetDev)

	orig := procRoot
	procRoot = dir
	t.Cleanup(func() { procRoot = orig })
	return dir
}

// TestClient_Metrics_RoundTrip drives the metrics op over a real connection:
// request frame in, guest sample frame out, decoded on the host side.
func TestClient_Metrics_RoundTrip(t *testing.T) {
	withProcFixture(t)

	conn, _ := newPipePair(t)
	c := &Client{conn: conn}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	m, err := c.Metrics(ctx)
	require.NoError(t, err)
	assert.InDelta(t, 1.67, m.CPUSeconds, 1e-9)
	assert.Equal(t, uint64((1000000-700000)*1024), m.MemoryBytes)
	assert.Equal(t, uint64(5000000), m.NetRxBytes)
	assert.Equal(t, uint64(3000000), m.NetTxBytes)
}

// TestClient_Metrics_LeavesClientUsable pins that a metrics read is a normal
// framed request: unlike a TTY exec it does not consume the connection.
func TestClient_Metrics_LeavesClientUsable(t *testing.T) {
	withProcFixture(t)

	conn, _ := newPipePair(t)
	c := &Client{conn: conn}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := c.Metrics(ctx)
	require.NoError(t, err)
	_, err = c.Metrics(ctx)
	require.NoError(t, err, "a second sample must work on the same connection")
}

// TestClient_Metrics_ReadFailureIsAnError proves a guest that cannot read its
// own /proc reports an error rather than a zeroed sample, so the host leaves the
// series unset instead of pinning them at 0.
func TestClient_Metrics_ReadFailureIsAnError(t *testing.T) {
	dir := t.TempDir() // empty: no /proc/stat
	orig := procRoot
	procRoot = dir
	t.Cleanup(func() { procRoot = orig })

	conn, _ := newPipePair(t)
	c := &Client{conn: conn}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := c.Metrics(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "metrics:")
}

// TestClient_Metrics_RefusesConsumedClient keeps the TTY-exec rule: after a TTY
// session the framed protocol is gone, so a metrics read must fail fast.
func TestClient_Metrics_RefusesConsumedClient(t *testing.T) {
	c := &Client{consumed: true}
	_, err := c.Metrics(context.Background())
	require.ErrorIs(t, err, errConsumed)
}
