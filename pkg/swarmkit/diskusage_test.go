package swarmkit

import (
	"path/filepath"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	swarmcrackermetrics "github.com/restuhaqza/swarmcracker/pkg/metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countSamples counts the exposed samples of the named metric family whose
// labels match every entry in match. It gathers the default registry, so it
// distinguishes a deleted series from one that merely reads back as zero.
func countSamples(t *testing.T, name string, match map[string]string) int {
	t.Helper()

	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)

	count := 0
	for _, mf := range families {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			labels := make(map[string]string, len(m.GetLabel()))
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			matched := true
			for k, v := range match {
				if labels[k] != v {
					matched = false
					break
				}
			}
			if matched {
				count++
			}
		}
	}
	return count
}

func TestDiskPathsLabels(t *testing.T) {
	e := &Executor{config: &Config{
		RootfsDir:   "/var/lib/firecracker/rootfs",
		SnapshotDir: "/var/lib/firecracker/snapshots",
		StateDir:    "/var/lib/swarmcracker",
	}}

	paths := e.diskPaths()
	require.Len(t, paths, 3)
	assert.Equal(t, diskPath{label: "rootfs", path: "/var/lib/firecracker/rootfs"}, paths[0])
	assert.Equal(t, diskPath{label: "snapshots", path: "/var/lib/firecracker/snapshots"}, paths[1])
	assert.Equal(t, diskPath{label: "state", path: "/var/lib/swarmcracker"}, paths[2])
}

func TestSampleDiskUsagePublishesOnlyExistingPaths(t *testing.T) {
	dir := t.TempDir()
	instance := "disk-sampler-existing"
	missing := filepath.Join(dir, "does-not-exist")

	// Sample first, then assert invariants. Free blocks change while the test
	// runs (other packages in `go test ./...` write to the same filesystem), so
	// comparing exact byte counts sampled twice is inherently racy.
	sampleDiskUsage(instance, []diskPath{
		{label: "rootfs", path: dir},
		{label: "snapshots", path: missing},
	})

	used := testutil.ToFloat64(swarmcrackermetrics.DiskUsageBytes.WithLabelValues(instance, "rootfs"))
	total := testutil.ToFloat64(swarmcrackermetrics.DiskTotalBytes.WithLabelValues(instance, "rootfs"))

	assert.Greater(t, total, float64(0), "existing path must publish its total size")
	assert.GreaterOrEqual(t, used, float64(0), "usage cannot be negative")
	assert.LessOrEqual(t, used, total, "usage cannot exceed the filesystem size")

	// The missing path emits nothing at all, not a zero series.
	assert.Equal(t, 0, countSamples(t, "swarmcracker_disk_usage_bytes",
		map[string]string{"instance": instance, "path": "snapshots"}))
	assert.Equal(t, 0, countSamples(t, "swarmcracker_disk_total_bytes",
		map[string]string{"instance": instance, "path": "snapshots"}))
}

func TestSampleDiskUsageSkipsUnconfiguredPath(t *testing.T) {
	instance := "disk-sampler-unset"

	sampleDiskUsage(instance, []diskPath{{label: "state", path: ""}})

	assert.Equal(t, 0, countSamples(t, "swarmcracker_disk_usage_bytes",
		map[string]string{"instance": instance}))
	assert.Equal(t, 0, countSamples(t, "swarmcracker_disk_total_bytes",
		map[string]string{"instance": instance}))
}

func TestSampleDiskUsageNilExecutorIsNoop(t *testing.T) {
	var e *Executor
	assert.NotPanics(t, func() { e.sampleDiskUsage() })
}
