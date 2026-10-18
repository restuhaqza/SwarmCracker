package jailer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeMeminfo writes a /proc/meminfo fixture and returns its path.
func writeMeminfo(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "meminfo")
	require.NoError(t, os.WriteFile(path, []byte(contents), 0600))
	return path
}

// TestReadHugepagesPool covers the 2M and 1G host shapes plus the strict
// missing/malformed-key contract. The 1G pool is read, not rejected: rejecting
// it is CheckHugepages' job, so the error can name the requested VM.
func TestReadHugepagesPool(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    HugepagesPool
		wantErr []string // substrings the error must contain
	}{
		{
			name: "2M pool",
			content: "MemTotal:       16384 kB\n" +
				"Hugepagesize:       2048 kB\n" +
				"HugePages_Total:     512\n" +
				"HugePages_Free:      128\n",
			want: HugepagesPool{PageSizeKB: 2048, Total: 512, Free: 128},
		},
		{
			name: "1G host pool is read, not rejected",
			content: "Hugepagesize:    1048576 kB\n" +
				"HugePages_Total:       0\n" +
				"HugePages_Free:        0\n",
			want: HugepagesPool{PageSizeKB: 1048576, Total: 0, Free: 0},
		},
		{
			name: "key order does not matter",
			content: "HugePages_Free:        7\n" +
				"Hugepagesize:       2048 kB\n" +
				"HugePages_Total:       9\n",
			want: HugepagesPool{PageSizeKB: 2048, Total: 9, Free: 7},
		},
		{
			name: "missing free count names the file and the key",
			content: "Hugepagesize:       2048 kB\n" +
				"HugePages_Total:       512\n",
			wantErr: []string{"meminfo", "HugePages_Free"},
		},
		{
			name: "malformed count names the key and the value",
			content: "Hugepagesize:       2048 kB\n" +
				"HugePages_Total:      many\n" +
				"HugePages_Free:        0\n",
			wantErr: []string{"HugePages_Total", "many"},
		},
		{
			name: "unsupported unit names the key",
			content: "Hugepagesize:          2 MB\n" +
				"HugePages_Total:       0\n" +
				"HugePages_Free:        0\n",
			wantErr: []string{"Hugepagesize", "MB"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeMeminfo(t, tt.content)

			got, err := ReadHugepagesPool(path)

			if len(tt.wantErr) > 0 {
				require.Error(t, err)
				for _, want := range tt.wantErr {
					assert.Contains(t, err.Error(), want)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestReadHugepagesPool_MissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent-meminfo")

	_, err := ReadHugepagesPool(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absent-meminfo")
}

// TestRequiredHugepages covers the 2M page arithmetic and the two rejected
// shapes: a non-positive size and an odd size.
func TestRequiredHugepages(t *testing.T) {
	tests := []struct {
		name    string
		sizeMiB int
		want    int
		wantErr string
	}{
		{name: "512 MiB", sizeMiB: 512, want: 256},
		{name: "1024 MiB", sizeMiB: 1024, want: 512},
		{name: "2 MiB is one page", sizeMiB: 2, want: 1},
		{name: "odd size", sizeMiB: 2047, wantErr: "multiple of 2 MiB"},
		{name: "zero", sizeMiB: 0, wantErr: "must be positive"},
		{name: "negative", sizeMiB: -512, wantErr: "must be positive"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RequiredHugepages(tt.sizeMiB)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Zero(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestCheckHugepages covers the accept path and every rejection, asserting the
// single error type callers type-assert and that the message carries the
// request and the pool numbers.
func TestCheckHugepages(t *testing.T) {
	tests := []struct {
		name    string
		pool    HugepagesPool
		sizeMiB int
		wantErr []string
	}{
		{
			name:    "enough free pages",
			pool:    HugepagesPool{PageSizeKB: 2048, Total: 1024, Free: 600},
			sizeMiB: 1024,
		},
		{
			name:    "exactly enough free pages",
			pool:    HugepagesPool{PageSizeKB: 2048, Total: 512, Free: 512},
			sizeMiB: 1024,
		},
		{
			name:    "empty pool names the request, the pool, and the sysctl",
			pool:    HugepagesPool{PageSizeKB: 2048},
			sizeMiB: 2048,
			wantErr: []string{
				"cannot back a 2048 MiB VM",
				"1024 pages needed",
				"0 free of 0 allocated",
				"vm.nr_hugepages=1024",
			},
		},
		{
			name:    "short pool names the numbers and the sysctl",
			pool:    HugepagesPool{PageSizeKB: 2048, Total: 512, Free: 100},
			sizeMiB: 1024,
			wantErr: []string{
				"1024 MiB",
				"512 pages needed",
				"100 free of 512 allocated",
				"vm.nr_hugepages=512",
			},
		},
		{
			name:    "wrong page size names the host's size",
			pool:    HugepagesPool{PageSizeKB: 1048576, Total: 4, Free: 4},
			sizeMiB: 1024,
			wantErr: []string{
				"1024 MiB",
				"512 pages needed",
				"1048576 KiB",
				"only 2M",
			},
		},
		{
			name:    "odd request names the size and the multiple",
			pool:    HugepagesPool{PageSizeKB: 2048, Total: 1024, Free: 600},
			sizeMiB: 2047,
			wantErr: []string{
				"2047 MiB",
				"multiple of 2 MiB",
				"600 free of 1024 allocated",
			},
		},
		{
			name:    "zero request names the size",
			pool:    HugepagesPool{PageSizeKB: 2048, Total: 1024, Free: 600},
			sizeMiB: 0,
			wantErr: []string{"0 MiB", "must be positive"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckHugepages(tt.pool, tt.sizeMiB)

			if len(tt.wantErr) == 0 {
				require.NoError(t, err)
				return
			}

			require.Error(t, err)
			var hpErr *HugepagesError
			require.True(t, errors.As(err, &hpErr), "error must be *HugepagesError, got %T", err)
			assert.Equal(t, tt.sizeMiB, hpErr.SizeMiB)
			assert.Equal(t, tt.pool.PageSizeKB, hpErr.PageSizeKB)
			assert.Equal(t, tt.pool.Free, hpErr.Free)
			assert.Equal(t, tt.pool.Total, hpErr.Total)
			for _, want := range tt.wantErr {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}
