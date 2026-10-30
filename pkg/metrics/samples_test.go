package metrics

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTaskSampleRecorded pins that a recorded guest sample is readable from
// memory with every field the guest reported, and that it is stamped with the
// record time rather than being left at the zero time.
func TestTaskSampleRecorded(t *testing.T) {
	taskID := "task-sample-recorded"
	t.Cleanup(func() { ClearVMTaskMetrics("sample-instance", taskID) })

	before := time.Now()
	RecordVMTaskMetrics("sample-instance", taskID, "web", 12.5, 64<<20, 1234, 5678)

	sample, ok := LastTaskSample(taskID)
	require.True(t, ok, "a recorded sample must be readable")

	assert.Equal(t, taskID, sample.TaskID)
	assert.Equal(t, "web", sample.Service)
	assert.InDelta(t, 12.5, sample.CPUSeconds, 1e-9)
	assert.Equal(t, uint64(64<<20), sample.MemoryBytes)
	assert.Equal(t, uint64(1234), sample.NetRxBytes)
	assert.Equal(t, uint64(5678), sample.NetTxBytes)

	// ObservedAt is set at record time, in UTC, so a caller can tell a fresh
	// sample from a stale one without a second clock source.
	require.False(t, sample.ObservedAt.IsZero(), "ObservedAt must be set at record time")
	assert.Equal(t, time.UTC, sample.ObservedAt.Location(), "ObservedAt must be UTC")
	assert.False(t, sample.ObservedAt.Before(before), "ObservedAt must not predate the record call")
	assert.WithinDuration(t, time.Now(), sample.ObservedAt, time.Second)
}

// TestTaskSampleReplacedByLatest pins that a task's cache entry is replaced, not
// appended: the cache answers "the last sample", so a later reading must win.
func TestTaskSampleReplacedByLatest(t *testing.T) {
	taskID := "task-sample-replaced"
	t.Cleanup(func() { ClearVMTaskMetrics("sample-instance", taskID) })

	RecordVMTaskMetrics("sample-instance", taskID, "web", 10, 1<<20, 100, 200)
	first, ok := LastTaskSample(taskID)
	require.True(t, ok)

	RecordVMTaskMetrics("sample-instance", taskID, "web", 20, 2<<20, 300, 400)
	second, ok := LastTaskSample(taskID)
	require.True(t, ok)

	assert.InDelta(t, 10.0, first.CPUSeconds, 1e-9)
	assert.InDelta(t, 20.0, second.CPUSeconds, 1e-9)
	assert.Equal(t, uint64(300), second.NetRxBytes)
	assert.False(t, second.ObservedAt.Before(first.ObservedAt),
		"the replacement must not look older than the sample it replaced")
}

// TestTaskSamplePerTask pins that samples are keyed per task: recording one task
// must not answer for another, and a task with no sample is simply absent.
func TestTaskSamplePerTask(t *testing.T) {
	t.Cleanup(func() {
		ClearVMTaskMetrics("sample-instance", "task-sample-a")
		ClearVMTaskMetrics("sample-instance", "task-sample-b")
	})

	RecordVMTaskMetrics("sample-instance", "task-sample-a", "web", 1, 10, 100, 1000)
	RecordVMTaskMetrics("sample-instance", "task-sample-b", "api", 2, 20, 200, 2000)

	a, ok := LastTaskSample("task-sample-a")
	require.True(t, ok)
	b, ok := LastTaskSample("task-sample-b")
	require.True(t, ok)

	assert.InDelta(t, 1.0, a.CPUSeconds, 1e-9)
	assert.Equal(t, "web", a.Service)
	assert.InDelta(t, 2.0, b.CPUSeconds, 1e-9)
	assert.Equal(t, "api", b.Service)

	// A task that was never sampled answers "no sample" rather than a zero
	// sample: zero is a valid reading (an idle VM), so the boolean is the only
	// way to tell the two apart.
	missing, ok := LastTaskSample("task-sample-never-recorded")
	assert.False(t, ok)
	assert.Equal(t, TaskSample{}, missing)
}

// TestTaskSampleClearedOnTeardown pins that teardown drops the cached sample
// too: a removed task must not keep answering with its last reading forever.
func TestTaskSampleClearedOnTeardown(t *testing.T) {
	t.Cleanup(func() { ClearVMTaskMetrics("sample-instance", "task-sample-stays") })

	RecordVMTaskMetrics("sample-instance", "task-sample-gone", "web", 1, 10, 100, 1000)
	RecordVMTaskMetrics("sample-instance", "task-sample-stays", "api", 2, 20, 200, 2000)

	_, ok := LastTaskSample("task-sample-gone")
	require.True(t, ok, "the sample must exist before the clear")

	ClearVMTaskMetrics("sample-instance", "task-sample-gone")

	cleared, ok := LastTaskSample("task-sample-gone")
	assert.False(t, ok, "a torn-down task must not answer with a stale sample")
	assert.Equal(t, TaskSample{}, cleared)

	_, ok = LastTaskSample("task-sample-stays")
	assert.True(t, ok, "clearing one task must not touch another task's sample")

	// Clearing twice is harmless: teardown paths are not guaranteed to run once.
	ClearVMTaskMetrics("sample-instance", "task-sample-gone")
	_, ok = LastTaskSample("task-sample-gone")
	assert.False(t, ok)
}

// TestTaskSampleConcurrentAccess pins that the cache is safe for the daemon's
// real access pattern — one poller per task recording independently while a
// reader asks for samples — and stays consistent under -race.
func TestTaskSampleConcurrentAccess(t *testing.T) {
	const (
		tasks   = 16
		rounds  = 20
		readers = 4
	)

	taskIDs := make([]string, tasks)
	for i := range taskIDs {
		taskIDs[i] = fmt.Sprintf("task-sample-race-%02d", i)
		t.Cleanup(func() { ClearVMTaskMetrics("race-instance", taskIDs[i]) })
	}

	// One recorder per task, like the daemon's one poller per VM.
	var writers sync.WaitGroup
	for i, taskID := range taskIDs {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for round := 1; round <= rounds; round++ {
				RecordVMTaskMetrics("race-instance", taskID, "web",
					float64(i*rounds+round), uint64(round), uint64(round), uint64(round))
			}
		}()
	}

	// Concurrent readers, like a broker answering a manager's request.
	stop := make(chan struct{})
	var readersDone sync.WaitGroup
	for range readers {
		readersDone.Add(1)
		go func() {
			defer readersDone.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, taskID := range taskIDs {
					if sample, ok := LastTaskSample(taskID); ok {
						assert.Equal(t, taskID, sample.TaskID)
					}
				}
			}
		}()
	}

	writers.Wait()
	close(stop)
	readersDone.Wait()

	// Every task ends with exactly its last written value: replacements do not
	// interleave into a half-updated entry.
	for i, taskID := range taskIDs {
		sample, ok := LastTaskSample(taskID)
		require.True(t, ok)
		assert.InDelta(t, float64(i*rounds+rounds), sample.CPUSeconds, 1e-9)
	}
}
