package swarmkit

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestToStr covers the toStr conversion helper. toStr only returns the value
// for string inputs (and "" for nil); everything else yields "". The final
// case passes an already-formatted string, which must round-trip unchanged.
func TestToStr(t *testing.T) {
	type sampleStruct struct {
		Name  string
		Count int
	}

	tests := []struct {
		name string
		in   interface{}
		want string
	}{
		{"nil returns empty string", nil, ""},
		{"string returns as-is", "hello", "hello"},
		{"empty string returns empty string", "", ""},
		{"int is not a string", 42, ""},
		{"int64 is not a string", int64(7), ""},
		{"bool is not a string", true, ""},
		{"error is not a string", errors.New("boom"), ""},
		{"struct is not a string", sampleStruct{Name: "x", Count: 1}, ""},
		{"pointer is not a string", &sampleStruct{Name: "y"}, ""},
		{"percent-v formatted value returns the formatted string", fmt.Sprintf("%v", 3.14), "3.14"},
		{"percent-v formatted struct returns the formatted string", fmt.Sprintf("%v", sampleStruct{Name: "z", Count: 2}), "{z 2}"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			require.NotPanics(t, func() {
				got = toStr(tt.in)
			}, "toStr must never panic")
			assert.Equal(t, tt.want, got)
		})
	}
}
