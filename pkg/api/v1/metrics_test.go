package v1

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMetricsDocumentEnvelope pins the wire shape of one task's last in-guest
// sample: the v1 envelope, a nanosecond-precision UTC instant, and every number
// the guest reported.
func TestMetricsDocumentEnvelope(t *testing.T) {
	observedAt := time.Date(2026, 10, 30, 12, 0, 0, 123456789, time.UTC)

	doc := NewTaskMetrics(TaskMetrics{
		TaskID:      "t1abc",
		Service:     "web",
		CPUSeconds:  12.5,
		MemoryBytes: 64 << 20,
		NetRxBytes:  1234,
		NetTxBytes:  5678,
		ObservedAt:  observedAt,
	})

	data, err := json.Marshal(doc)
	require.NoError(t, err)

	require.JSONEq(t, `{
		"schemaVersion": 1,
		"kind": "TaskMetrics",
		"taskId": "t1abc",
		"service": "web",
		"cpuSeconds": 12.5,
		"memoryBytes": 67108864,
		"netRxBytes": 1234,
		"netTxBytes": 5678,
		"observedAt": "2026-10-30T12:00:00.123456789Z"
	}`, string(data))
}

// TestMetricsDocumentOmitsAnEmptyService proves a sample whose guest reported no
// service (an unlabelled task) omits the key instead of publishing "".
func TestMetricsDocumentOmitsAnEmptyService(t *testing.T) {
	doc := NewTaskMetrics(TaskMetrics{
		TaskID:     "t1abc",
		CPUSeconds: 0,
		ObservedAt: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC),
	})

	data, err := json.Marshal(doc)
	require.NoError(t, err)

	var fields map[string]any
	require.NoError(t, json.Unmarshal(data, &fields))
	assert.NotContains(t, fields, "service")
	assert.Equal(t, float64(SchemaVersion), fields["schemaVersion"])
	assert.Equal(t, KindTaskMetrics, fields["kind"])
	// Zero is a valid reading: an idle guest really reports zero CPU, so the
	// numeric fields are always present rather than omitted at zero.
	assert.Contains(t, fields, "cpuSeconds")
	assert.Contains(t, fields, "memoryBytes")
}
