package runtime

import (
	"errors"
	"fmt"
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

// idProblem words CheckTaskID's refusal for one field of a node-local record.
// kind is the noun the field's ID names ("task", "service"): SwarmKit gives both
// the same shape, and the record that holds one needs to say which field is wrong
// in that field's own terms. Callers match the sentinels rather than the text.
func idProblem(field, kind, id string) error {
	err := CheckTaskID(id)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrTaskIDEmpty):
		return fmt.Errorf("%s is empty: want a SwarmKit %s ID", field, kind)
	case errors.Is(err, ErrTaskIDPathSeparator):
		return fmt.Errorf("%s %q must not contain a path separator: a %s ID is alphanumeric", field, id, kind)
	case errors.Is(err, ErrTaskIDDot):
		return fmt.Errorf("%s %q must not contain a dot: a %s ID is alphanumeric", field, id, kind)
	default:
		return err
	}
}
