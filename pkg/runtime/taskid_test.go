package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCheckTaskID pins the one rule that decides whether an ID may name a file
// inside a directory. Two layers need it and neither may drift: the node broker,
// which turns an untrusted URL path into <socketDir>/<taskID>.sock, and the
// records here, which turn an ID into a record file name.
func TestCheckTaskID(t *testing.T) {
	const realTaskID = "0a76cd9b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7"

	cases := []struct {
		name string
		id   string
		want error
	}{
		{name: "a real SwarmKit task ID", id: realTaskID, want: nil},
		{name: "a hyphenated ID from a test", id: "task-1", want: nil},
		{name: "the longest allowed ID", id: strings.Repeat("a", MaxIDLength), want: nil},
		{name: "empty", id: "", want: ErrTaskIDEmpty},
		{name: "forward slash", id: "a/b", want: ErrTaskIDPathSeparator},
		{name: "backslash", id: `a\b`, want: ErrTaskIDPathSeparator},
		{name: "traversal", id: "..", want: ErrTaskIDDot},
		{name: "current directory", id: ".", want: ErrTaskIDDot},
		{name: "a dot inside", id: "a.b", want: ErrTaskIDDot},
		{name: "encoded traversal", id: "..%2F", want: ErrTaskIDDot},
		{name: "a space", id: "task 1", want: ErrTaskIDWhitespace},
		{name: "a leading space", id: " task-1", want: ErrTaskIDWhitespace},
		{name: "a tab", id: "task\t1", want: ErrTaskIDWhitespace},
		{name: "a newline", id: "task\n1", want: ErrTaskIDWhitespace},
		{name: "one past the bound", id: strings.Repeat("a", MaxIDLength+1), want: ErrTaskIDTooLong},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckTaskID(tc.id)
			if tc.want == nil {
				require.NoError(t, err, "ID %q names a single path element", tc.id)
				return
			}
			require.Error(t, err)
			assert.ErrorIs(t, err, tc.want, "ID %q must be refused for the reason the sentinel names", tc.id)
			assert.NotEmpty(t, err.Error())
		})
	}
}

// TestCheckTaskIDReasonsAreDistinct keeps the sentinels usable with errors.Is:
// a caller that wants to word a refusal itself must be able to tell the reasons
// apart.
func TestCheckTaskIDReasonsAreDistinct(t *testing.T) {
	reasons := []error{
		ErrTaskIDEmpty,
		ErrTaskIDPathSeparator,
		ErrTaskIDDot,
		ErrTaskIDWhitespace,
		ErrTaskIDTooLong,
	}

	for i, a := range reasons {
		for j, b := range reasons {
			if i == j {
				continue
			}
			assert.NotErrorIs(t, a, b, "sentinel %d must not match sentinel %d", i, j)
		}
	}
}

// TestMaxIDLengthFitsOneFileName keeps the bound honest. An ID becomes
// <id>.json, <id>.sock or <id>.log, so the longest ID a caller may send must still
// name an entry the filesystem accepts: a bound above NAME_MAX would turn a caller
// mistake into ENAMETOOLONG - a 503 for something that was never the node's fault.
func TestMaxIDLengthFitsOneFileName(t *testing.T) {
	dir := t.TempDir()
	id := strings.Repeat("a", MaxIDLength)
	require.NoError(t, CheckTaskID(id))

	for _, suffix := range []string{".json", ".sock", ".log"} {
		path := filepath.Join(dir, id+suffix)
		require.NoError(t, os.WriteFile(path, nil, 0o600),
			"an ID at the bound must still name %s", suffix)
	}
}
