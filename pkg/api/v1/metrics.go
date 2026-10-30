package v1

import "time"

// TaskMetrics is the last in-guest resource sample a task's node holds for it.
//
// It is a point reading, not a history: the node keeps only the most recent
// sample its poller recorded, so a caller compares two reads taken over an
// interval rather than trusting one absolute value. CPUSeconds and the byte
// counters are cumulative since the guest booted; MemoryBytes is a gauge (memory
// in use at the instant of the sample).
//
// The document is served by the node broker and relayed to a manager-side
// autoscaler, so its fields are the numbers that decision needs and nothing the
// guest's own agent shape exposes.
type TaskMetrics struct {
	SchemaVersion int       `json:"schemaVersion"`
	Kind          string    `json:"kind"`
	TaskID        string    `json:"taskId"`
	Service       string    `json:"service,omitempty"`
	CPUSeconds    float64   `json:"cpuSeconds"`
	MemoryBytes   uint64    `json:"memoryBytes"`
	NetRxBytes    uint64    `json:"netRxBytes"`
	NetTxBytes    uint64    `json:"netTxBytes"`
	ObservedAt    time.Time `json:"observedAt"`
}

// NewTaskMetrics wraps a sample in the v1 envelope.
func NewTaskMetrics(sample TaskMetrics) TaskMetrics {
	sample.SchemaVersion = SchemaVersion
	sample.Kind = KindTaskMetrics
	return sample
}
