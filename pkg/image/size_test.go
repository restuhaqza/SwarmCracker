package image

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDiskSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"", 0},
		{"0", 0},
		{"1024", 1024},
		{"1K", 1 << 10},
		{"1KB", 1 << 10},
		{"1KiB", 1 << 10},
		{"512M", 512 << 20},
		{"10G", 10 << 30},
		{"10GB", 10 << 30},
		{"10GiB", 10 << 30},
		{"1.5G", int64(1.5 * (1 << 30))},
		{"2T", 2 << 40},
		{"  10g  ", 10 << 30},
	}

	for _, tc := range cases {
		got, err := parseDiskSize(tc.in)
		require.NoError(t, err, "input %q", tc.in)
		assert.Equal(t, tc.want, got, "input %q", tc.in)
	}

	for _, bad := range []string{"abc", "10X", "-5G", "G", "10 IB"} {
		_, err := parseDiskSize(bad)
		assert.Error(t, err, "input %q should be rejected", bad)
	}
}

func TestRootfsLargeEnough(t *testing.T) {
	assert.True(t, rootfsLargeEnough(100, 0), "no minimum always passes")
	assert.True(t, rootfsLargeEnough(100, 100))
	assert.True(t, rootfsLargeEnough(101, 100))
	assert.False(t, rootfsLargeEnough(99, 100))
}

func TestFirstInt64(t *testing.T) {
	assert.Equal(t, int64(0), firstInt64(nil))
	assert.Equal(t, int64(7), firstInt64([]int64{7, 8}))
}
