package jailer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCreateCgroup_RejectsNumaNodeNotOnline pins the first grantability check:
// requesting a node the host does not have online is a hard error, not a
// silently narrowed cpuset.mems (the kernel keeps the value and only narrows the
// effective set). Nothing may be created for a refused request either.
func TestCreateCgroup_RejectsNumaNodeNotOnline(t *testing.T) {
	mgr, base := newTempCgroupManager(t)
	withOnlineNodes(t, mgr, "0")
	taskID := "task-numa-offline"

	err := mgr.CreateCgroup(taskID, ResourceLimits{CpusetMems: "1"})
	require.Error(t, err)

	var cgroupErr *CgroupError
	require.True(t, errors.As(err, &cgroupErr), "want a *CgroupError, got %T", err)
	assert.Equal(t, taskID, cgroupErr.TaskID)
	assert.Equal(t, CgroupStageValidate, cgroupErr.Stage)
	assert.False(t, IsQuotaStage(err), "an ungrantable NUMA request is never a quota failure")

	msg := err.Error()
	assert.Contains(t, msg, "requested NUMA nodes 1 are not grantable")
	assert.Contains(t, msg, "node(s) 1 are not online")
	assert.Contains(t, msg, "host online NUMA nodes: 0")

	_, statErr := os.Stat(filepath.Join(base, taskID))
	assert.True(t, os.IsNotExist(statErr), "a refused NUMA request must leave no cgroup behind")
}

// TestCreateCgroup_RejectsNumaNodeExcludedByParent pins the second check: the
// node is online, but the parent cgroup's effective mems excludes it, which is
// the other way the kernel silently narrows instead of failing.
func TestCreateCgroup_RejectsNumaNodeExcludedByParent(t *testing.T) {
	mgr, base := newTempCgroupManager(t)
	withOnlineNodes(t, mgr, "0-1")
	allowed := filepath.Join(base, "cpuset.mems.effective")
	require.NoError(t, os.WriteFile(allowed, []byte("0\n"), 0o644))
	taskID := "task-numa-parent"

	err := mgr.CreateCgroup(taskID, ResourceLimits{CpusetMems: "1"})
	require.Error(t, err)

	msg := err.Error()
	assert.Contains(t, msg, "requested NUMA nodes 1 are not grantable")
	assert.Contains(t, msg, "node(s) 1 are excluded by the parent cgroup's cpuset.mems.effective 0")
	assert.Contains(t, msg, allowed, "the error names the file that excluded the node")
	assert.Contains(t, msg, "host online NUMA nodes: 0-1", "the node is online, so the host list is not the cause")
	assert.NotContains(t, msg, "are not online")

	_, statErr := os.Stat(filepath.Join(base, taskID))
	assert.True(t, os.IsNotExist(statErr))
}

// TestCreateCgroup_AcceptsGrantableNumaNodes pins the happy path: online and
// parent-allowed nodes are written exactly as requested.
func TestCreateCgroup_AcceptsGrantableNumaNodes(t *testing.T) {
	mgr, base := newTempCgroupManager(t)
	withOnlineNodes(t, mgr, "0-1")
	require.NoError(t, os.WriteFile(filepath.Join(base, "cpuset.mems.effective"), []byte("0-1\n"), 0o644))

	require.NoError(t, mgr.CreateCgroup("task-numa-ok", ResourceLimits{CpusetMems: "1,0"}))
	assert.Equal(t, "0-1", readFileString(t, filepath.Join(base, "task-numa-ok", "cpuset.mems")))
}

// TestCreateCgroup_EmptyNumaRequestKeepsAutoResolution pins that the check is
// scoped to an explicit request: an empty CpusetMems is not validated and still
// resolves from the parent (then the host) exactly as before.
func TestCreateCgroup_EmptyNumaRequestKeepsAutoResolution(t *testing.T) {
	mgr, base := newTempCgroupManager(t)
	// An unreadable host list would make a validation attempt fail; the auto
	// path never runs one.
	mgr.nodeOnlinePath = filepath.Join(t.TempDir(), "missing-node-online")
	require.NoError(t, os.WriteFile(filepath.Join(base, "cpuset.mems"), []byte("0\n"), 0o644))

	require.NoError(t, mgr.CreateCgroup("task-numa-auto", ResourceLimits{CpusetCPUs: "0"}))
	assert.Equal(t, "0", readFileString(t, filepath.Join(base, "task-numa-auto", "cpuset.mems")))
}

// TestCreateVMCgroup_RejectsUngrantableNumaNodes pins the ruling at the policy
// level: a NUMA-only task whose nodes cannot be granted fails the VM (no cgroup,
// created=false) through the same rejection path as an impossible cpuset.
func TestCreateVMCgroup_RejectsUngrantableNumaNodes(t *testing.T) {
	t.Run("node not online", func(t *testing.T) {
		mgr, base := newTempCgroupManager(t)
		withOnlineNodes(t, mgr, "0")

		created, err := CreateVMCgroup(ensureOKCgroupOps{mgr}, nil, zerolog.Nop(), CgroupRequest{
			Task:     numaTask("1"),
			VCPUs:    1,
			MemoryMB: 128,
		})
		require.Error(t, err)
		assert.False(t, created)
		assert.Contains(t, err.Error(), `cannot apply NUMA node list "1" to task task-numa`)
		assert.Contains(t, err.Error(), "host online NUMA nodes: 0")
		_, statErr := os.Stat(filepath.Join(base, "task-numa"))
		assert.True(t, os.IsNotExist(statErr))
	})

	t.Run("node excluded by the parent", func(t *testing.T) {
		mgr, base := newTempCgroupManager(t)
		withOnlineNodes(t, mgr, "0-1")
		require.NoError(t, os.WriteFile(filepath.Join(base, "cpuset.mems.effective"), []byte("0\n"), 0o644))

		created, err := CreateVMCgroup(ensureOKCgroupOps{mgr}, nil, zerolog.Nop(), CgroupRequest{
			Task:     numaTask("1"),
			VCPUs:    1,
			MemoryMB: 128,
		})
		require.Error(t, err)
		assert.False(t, created)
		assert.Contains(t, err.Error(), `cannot apply NUMA node list "1" to task task-numa`)
		assert.Contains(t, err.Error(), "parent cgroup's cpuset.mems.effective 0")
	})

	t.Run("all nodes grantable", func(t *testing.T) {
		mgr, _ := newTempCgroupManager(t)
		withOnlineNodes(t, mgr, "0-1")

		created, err := CreateVMCgroup(ensureOKCgroupOps{mgr}, nil, zerolog.Nop(), CgroupRequest{
			Task:     numaTask("1,0"),
			VCPUs:    1,
			MemoryMB: 128,
		})
		require.NoError(t, err)
		assert.True(t, created)
	})
}

// TestSubtractCPUSetRanges pins the range arithmetic the checks depend on,
// including partial coverage and the int64 upper bound.
func TestSubtractCPUSetRanges(t *testing.T) {
	ranges := func(s string) []cpusetRange {
		parsed, err := parseCPUSetRanges(s)
		require.NoError(t, err)
		return mergeCPUSetRanges(parsed)
	}

	tests := []struct {
		name    string
		want    string
		allowed string
		missing string
	}{
		{name: "fully covered", want: "0-1", allowed: "0-3", missing: ""},
		{name: "none covered", want: "4-5", allowed: "0-3", missing: "4-5"},
		{name: "partially covered", want: "0-5", allowed: "2-3", missing: "0-1,4-5"},
		{name: "hole", want: "0-3", allowed: "1-2", missing: "0,3"},
		{name: "multiple wanted ranges", want: "0,2", allowed: "0,2", missing: ""},
		{name: "adjacent allowed does not count", want: "2", allowed: "0-1", missing: "2"},
		{name: "max int64 start", want: "9223372036854775807", allowed: "0", missing: "9223372036854775807"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatCPUSetRanges(subtractCPUSetRanges(ranges(tt.want), ranges(tt.allowed)))
			assert.Equal(t, tt.missing, got)
		})
	}
}
