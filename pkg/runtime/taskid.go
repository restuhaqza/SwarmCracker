package runtime

import (
	"errors"
	"strings"
)

// A task ID becomes a path element: <socketDir>/<taskID>.sock,
// <logDir>/<taskID>.log, <recordDir>/<taskID>.json. It also arrives from an
// untrusted place — a manager's request URL, a migration artifact's manifest —
// so the shapes that could escape the directory must be refused. That refusal is
// one rule, not one rule per caller: two copies drift, and the copy that drifts
// is the one that reads or writes outside the directory.
//
// CheckTaskID is that rule. Callers word the refusal themselves (the broker says
// "task ID", the registry says which field of a record is wrong) but they ask
// this function and match these sentinels instead of re-deriving the shapes.
var (
	// ErrTaskIDEmpty is returned for an ID that is not there at all.
	ErrTaskIDEmpty = errors.New("task ID is empty")
	// ErrTaskIDPathSeparator is returned for an ID containing "/" or "\": it
	// would address a file in another directory.
	ErrTaskIDPathSeparator = errors.New("task ID contains a path separator")
	// ErrTaskIDDot is returned for an ID containing ".", the building block of
	// "." and "..".
	ErrTaskIDDot = errors.New("task ID contains a dot")
)

// CheckTaskID reports whether id can name a file inside a directory: it must be
// present and a single path element. SwarmKit task IDs are alphanumeric, so a
// separator or a dot buys nothing and is refused outright.
func CheckTaskID(id string) error {
	switch {
	case id == "":
		return ErrTaskIDEmpty
	case strings.ContainsAny(id, `/\`):
		return ErrTaskIDPathSeparator
	case strings.Contains(id, "."):
		return ErrTaskIDDot
	}
	return nil
}
