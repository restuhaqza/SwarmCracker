package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHugepagesAnnotationConstants pins the label/annotation keys: the service
// label (what an operator writes) and the task annotation (what the VMM paths
// read) must stay the same key, exactly like the cpuset and the balloon.
func TestHugepagesAnnotationConstants(t *testing.T) {
	assert.Equal(t, "swarmcracker.hugepages", HugepagesLabel)
	assert.Equal(t, HugepagesLabel, AnnotationHugepages)

	assert.Equal(t, "swarmcracker.numa-node", NumaLabel)
	assert.Equal(t, NumaLabel, AnnotationNumaNode)
}

// TestParseHugepages covers the accepted spellings and the strict rejection:
// a value that is not a recognised request must not silently mean 4K memory.
func TestParseHugepages(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    bool
		wantErr bool
	}{
		{name: "absent", in: "", want: false},
		{name: "whitespace only", in: "   ", want: false},
		{name: "none", in: "none", want: false},
		{name: "false", in: "false", want: false},
		{name: "zero", in: "0", want: false},
		{name: "true", in: "true", want: true},
		{name: "2m lowercase", in: "2m", want: true},
		{name: "2M uppercase", in: "2M", want: true},
		{name: "padded and uppercase", in: " TRUE ", want: true},
		{name: "padded none", in: " none ", want: false},
		{name: "2g rejected", in: "2g", wantErr: true},
		{name: "yes rejected", in: "yes", wantErr: true},
		{name: "1m rejected", in: "1m", wantErr: true},
		{name: "truex rejected", in: "truex", wantErr: true},
		{name: "on rejected", in: "on", wantErr: true},
		{name: "one rejected", in: "1", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseHugepages(tt.in)

			if tt.wantErr {
				require.Error(t, err)
				assert.False(t, got)
				for _, allowed := range []string{"true", "false", "2M"} {
					assert.Contains(t, err.Error(), allowed,
						"the error must name every allowed value")
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
