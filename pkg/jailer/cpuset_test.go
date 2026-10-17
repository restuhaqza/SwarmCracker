package jailer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseCPUSet_Valid covers canonicalisation: ascending order, merged
// overlapping and adjacent ranges, and no leading zeros.
func TestParseCPUSet_Valid(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "single cpu", in: "0", want: "0"},
		{name: "single high cpu", in: "63", want: "63"},
		{name: "already canonical range", in: "0-3", want: "0-3"},
		{name: "single-element range", in: "1-1", want: "1"},
		{name: "unsorted", in: "3,1-2", want: "1-3"},
		{name: "adjacent single merges into range", in: "0,1,2", want: "0-2"},
		{name: "adjacent ranges merge", in: "0-3,4-7", want: "0-7"},
		{name: "gap is preserved", in: "0-3,5-7", want: "0-3,5-7"},
		{name: "overlapping ranges merge", in: "0-3,2-5", want: "0-5"},
		{name: "dup cpus collapse", in: "2,2,2", want: "2"},
		{name: "range bridging gap", in: "0-1,3-4,2", want: "0-4"},
		{name: "shuffled ranges", in: "4-5,0-1,2-3", want: "0-5"},
		{name: "descending singles", in: "10,8,9", want: "8-10"},
		{name: "many segments", in: "8,0-2,4,6-7", want: "0-2,4,6-8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseCPUSet(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestParseCPUSet_Invalid covers the strict-rejection contract.
func TestParseCPUSet_Invalid(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{name: "empty", in: ""},
		{name: "space", in: " "},
		{name: "tabs and newline only", in: "\t\n"},
		{name: "bare word", in: "a"},
		{name: "trailing dash", in: "1-"},
		{name: "negative", in: "-1"},
		{name: "reversed range", in: "5-3"},
		{name: "empty element", in: "1,,2"},
		{name: "leading empty element", in: ",1"},
		{name: "trailing empty element", in: "1,"},
		{name: "leading whitespace", in: " 0"},
		{name: "trailing whitespace", in: "0 "},
		{name: "inner whitespace", in: "0, 1"},
		{name: "double dash", in: "1-2-3"},
		{name: "zero padded", in: "01"},
		{name: "zero padded range", in: "0-03"},
		{name: "plus sign", in: "+1"},
		{name: "decimal", in: "1.5"},
		{name: "hex", in: "0x1f"},
		{name: "overflow", in: "99999999999999999999999999"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseCPUSet(tt.in)
			require.Error(t, err)
			assert.Empty(t, got)
			assert.Contains(t, err.Error(), "invalid cpuset")
		})
	}
}

// TestParseCPUSet_CanonicalEquivalence asserts that equivalent inputs compare
// equal after parsing, so a spec and a kernel-written value never differ
// textually just because the user wrote the list differently.
func TestParseCPUSet_CanonicalEquivalence(t *testing.T) {
	groups := [][]string{
		{"0-3", "3,1-2,0", "0,1,2,3", "0-1,2-3"},
		{"0-1,3", "3,0-1", "1,0,3"},
		{"8-10", "10,8-9", "9,8,10"},
	}
	for _, group := range groups {
		t.Run(group[0], func(t *testing.T) {
			want, err := ParseCPUSet(group[0])
			require.NoError(t, err)
			for _, in := range group[1:] {
				got, err := ParseCPUSet(in)
				require.NoError(t, err, "input %q", in)
				assert.Equal(t, want, got, "input %q", in)
			}
		})
	}
}
