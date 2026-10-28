//go:build !integration

package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeProcFixture lays out a minimal guest /proc under dir.
func writeProcFixture(t *testing.T, dir, stat, meminfo, netdev string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "net"), 0o755))
	if stat != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644))
	}
	if meminfo != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "meminfo"), []byte(meminfo), 0o644))
	}
	if netdev != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "net", "dev"), []byte(netdev), 0o644))
	}
}

const fixtureStat = `cpu  100 20 30 400 50 10 5 2 0 0
cpu0 50 10 15 200 25 5 2 1 0 0
cpu1 50 10 15 200 25 5 3 1 0 0
intr 12345 0 0
ctxt 999
`

const fixtureMeminfo = `MemTotal:        1000000 kB
MemFree:          100000 kB
MemAvailable:     700000 kB
Buffers:           50000 kB
Cached:           150000 kB
`

const fixtureNetDev = `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo:    1000      10    0    0    0     0          0         0     1000      10    0    0    0     0       0          0
  eth0: 5000000    4000    0    0    0     0          0         0  3000000    3000    0    0    0     0       0          0
`

func TestReadGuestMetrics(t *testing.T) {
	dir := t.TempDir()
	writeProcFixture(t, dir, fixtureStat, fixtureMeminfo, fixtureNetDev)

	m, err := ReadGuestMetrics(dir)
	require.NoError(t, err)

	// user+nice+system+irq+softirq+steal = 100+20+30+10+5+2 = 167 ticks at
	// USER_HZ=100. Idle (400) and iowait (50) are excluded, and guest/guest_nice
	// are not added twice.
	assert.InDelta(t, 1.67, m.CPUSeconds, 1e-9)

	// MemTotal - MemAvailable, in kB converted to bytes.
	assert.Equal(t, uint64((1000000-700000)*1024), m.MemoryBytes)

	// Loopback is excluded: only eth0's counters count.
	assert.Equal(t, uint64(5000000), m.NetRxBytes)
	assert.Equal(t, uint64(3000000), m.NetTxBytes)
}

// TestReadGuestMetrics_MemoryFallback covers kernels older than 3.14, which have
// no MemAvailable.
func TestReadGuestMetrics_MemoryFallback(t *testing.T) {
	meminfo := `MemTotal:        1000000 kB
MemFree:          100000 kB
Buffers:           50000 kB
Cached:           150000 kB
`
	dir := t.TempDir()
	writeProcFixture(t, dir, fixtureStat, meminfo, fixtureNetDev)

	m, err := ReadGuestMetrics(dir)
	require.NoError(t, err)
	assert.Equal(t, uint64((1000000-300000)*1024), m.MemoryBytes)
}

// TestReadGuestMetrics_TruncatedStatLine covers a kernel that reports fewer
// counters: the ones present are still summed.
func TestReadGuestMetrics_TruncatedStatLine(t *testing.T) {
	dir := t.TempDir()
	writeProcFixture(t, dir, "cpu  100 20 30 400\n", fixtureMeminfo, fixtureNetDev)

	m, err := ReadGuestMetrics(dir)
	require.NoError(t, err)
	assert.InDelta(t, 1.5, m.CPUSeconds, 1e-9)
}

// TestReadGuestMetrics_ZeroTrafficIsValid pins that an idle guest is reported as
// zero, not as an error: no packets is a real reading.
func TestReadGuestMetrics_ZeroTrafficIsValid(t *testing.T) {
	netdev := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo:    1000      10    0    0    0     0          0         0     1000      10    0    0    0     0       0          0
  eth0:       0       0    0    0    0     0          0         0        0       0    0    0    0     0       0          0
`
	dir := t.TempDir()
	writeProcFixture(t, dir, fixtureStat, fixtureMeminfo, netdev)

	m, err := ReadGuestMetrics(dir)
	require.NoError(t, err)
	assert.Zero(t, m.NetRxBytes)
	assert.Zero(t, m.NetTxBytes)
}

func TestReadGuestMetrics_Errors(t *testing.T) {
	tests := []struct {
		name    string
		stat    string
		meminfo string
		netdev  string
	}{
		{name: "no proc stat", stat: "", meminfo: fixtureMeminfo, netdev: fixtureNetDev},
		{name: "no cpu line", stat: "intr 1\n", meminfo: fixtureMeminfo, netdev: fixtureNetDev},
		{name: "no meminfo", stat: fixtureStat, meminfo: "", netdev: fixtureNetDev},
		{name: "no MemTotal", stat: fixtureStat, meminfo: "MemFree: 1 kB\n", netdev: fixtureNetDev},
		{
			// Only loopback: nothing would ever be reported, so the poller must
			// not publish a permanent zero.
			name:    "loopback only",
			stat:    fixtureStat,
			meminfo: fixtureMeminfo,
			netdev:  "  lo: 1 1 0 0 0 0 0 0 1 1 0 0 0 0 0 0\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeProcFixture(t, dir, tt.stat, tt.meminfo, tt.netdev)
			_, err := ReadGuestMetrics(dir)
			require.Error(t, err)
		})
	}
}

func TestReadCPUSeconds_GuestNotDoubleCounted(t *testing.T) {
	// guest and guest_nice are already included in user and nice by the kernel.
	dir := t.TempDir()
	writeProcFixture(t, dir,
		"cpu  50 10 0 0 0 0 0 0 40 5\n",
		fixtureMeminfo, fixtureNetDev)

	m, err := ReadGuestMetrics(dir)
	require.NoError(t, err)
	assert.InDelta(t, 0.6, m.CPUSeconds, 1e-9, "only user+nice count once")
}
