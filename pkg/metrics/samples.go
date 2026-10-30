package metrics

import (
	"sync"
	"time"
)

// TaskSample is one in-guest reading, as recorded by the daemon.
//
// It carries the same numbers the four per-VM resource series publish, kept in
// memory so that "what did task X last report?" can be answered inside the
// process, without an external Prometheus. The daemon fills the cache from
// RecordVMTaskMetrics and empties it from ClearVMTaskMetrics; anything else
// reads it through LastTaskSample.
//
// The CPU figure and the byte counters are cumulative, exactly as the guest
// reported them, so a caller compares two samples rather than trusting the
// absolute value. MemoryBytes is the exception: it is a gauge, the memory in use
// at the instant of the sample, so its absolute value is the reading.
type TaskSample struct {
	TaskID      string
	Service     string
	CPUSeconds  float64 // cumulative since boot
	MemoryBytes uint64  // in use (gauge, not cumulative)
	NetRxBytes  uint64
	NetTxBytes  uint64
	ObservedAt  time.Time // UTC, when the daemon recorded the reading
}

// taskSampleCache holds the last sample per task.
//
// It is keyed by task ID alone: a SwarmKit task belongs to exactly one node, and
// a daemon only ever records the tasks it runs, so the instance label the
// Prometheus series carry cannot disambiguate anything the task ID does not.
//
// The cache has its own lock, and no other lock in this package is ever taken
// while it is held (the Prometheus client's locks are released before the cache
// is touched), so it cannot participate in a lock-ordering cycle with the
// gauges.
var taskSampleCache = struct {
	mu     sync.RWMutex
	byTask map[string]TaskSample
}{byTask: make(map[string]TaskSample)}

// storeTaskSample records a sample as the task's latest reading, replacing any
// previous one: the cache answers "the last sample", never a history.
func storeTaskSample(sample TaskSample) {
	taskSampleCache.mu.Lock()
	defer taskSampleCache.mu.Unlock()
	taskSampleCache.byTask[sample.TaskID] = sample
}

// deleteTaskSample drops a task's cached sample. Deleting an absent task is a
// no-op: teardown paths are not guaranteed to run exactly once.
func deleteTaskSample(taskID string) {
	taskSampleCache.mu.Lock()
	defer taskSampleCache.mu.Unlock()
	delete(taskSampleCache.byTask, taskID)
}

// LastTaskSample returns the last recorded sample for a task.
//
// The second result is false when the task has no sample — either it was never
// polled (a guest still booting, or an image whose agent predates the metrics
// operation) or it has been torn down. Callers must branch on that boolean
// instead of reading a zero sample: an idle task legitimately reports zero CPU
// and zero network, so the zero value is not a "missing" marker.
//
// The returned value is a copy, so a caller may keep it while the cache moves on.
func LastTaskSample(taskID string) (TaskSample, bool) {
	taskSampleCache.mu.RLock()
	defer taskSampleCache.mu.RUnlock()
	sample, ok := taskSampleCache.byTask[taskID]
	return sample, ok
}
