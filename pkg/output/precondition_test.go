package output

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestPreconditionErrors pins the details the HTTP layer switches on: both are
// the stable `conflict` code, and only the reason distinguishes 428 from 412.
func TestPreconditionErrors(t *testing.T) {
	required := PreconditionRequired()
	assert.Equal(t, CodeConflict, required.Code)
	assert.Equal(t, "If-Match", required.Details["required"])
	assert.Equal(t, ReasonPreconditionRequired, required.Details["reason"])
	assert.False(t, required.Retryable)
	assert.Equal(t, 4, required.ExitCode())

	failed := PreconditionFailed("17", "18")
	assert.Equal(t, CodeConflict, failed.Code)
	assert.Equal(t, "17", failed.Details["expectedRevision"])
	assert.Equal(t, "18", failed.Details["currentRevision"])
	assert.Equal(t, ReasonPreconditionFailed, failed.Details["reason"])
	assert.False(t, failed.Retryable)
	assert.Equal(t, 4, failed.ExitCode())
}
