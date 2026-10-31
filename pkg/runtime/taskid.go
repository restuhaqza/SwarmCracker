package runtime

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
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
	// ErrTaskIDWhitespace is returned for an ID with a space, tab or newline in
	// it. SwarmKit IDs are alphanumeric: a blank in one is a caller mistake -
	// usually a name where an ID belongs - and it would reach a log line and a
	// file name as an invisible difference.
	ErrTaskIDWhitespace = errors.New("task ID contains whitespace")
	// ErrTaskIDTooLong is returned for an ID past MaxIDLength. The ID becomes a
	// file name, so the bound is what keeps a caller mistake a refusal instead of
	// the filesystem's ENAMETOOLONG, which would read as the node's failure.
	ErrTaskIDTooLong = errors.New("task ID is too long")
)

// MaxIDLength is the longest ID an operation accepts. It is a bound rather than a
// shape: SwarmKit IDs are 25 characters, and the files this rule protects name an
// entry as <id>.json, <id>.sock or <id>.log, so 128 leaves the whole filename well
// inside the kernel's NAME_MAX while never refusing a real ID. Without it a long
// enough ID is a 503 from the filesystem on write and a 500 on read, for a request
// that was the caller's mistake.
const MaxIDLength = 128

// CheckTaskID reports whether id can name a file inside a directory: it must be
// present, a single path element, and short enough to be one. SwarmKit task IDs are
// alphanumeric, so a separator, a dot or a blank buys nothing and is refused
// outright.
func CheckTaskID(id string) error {
	switch {
	case id == "":
		return ErrTaskIDEmpty
	case strings.ContainsAny(id, `/\`):
		return ErrTaskIDPathSeparator
	case strings.Contains(id, "."):
		return ErrTaskIDDot
	case strings.IndexFunc(id, unicode.IsSpace) >= 0:
		return ErrTaskIDWhitespace
	case len(id) > MaxIDLength:
		return ErrTaskIDTooLong
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
	case errors.Is(err, ErrTaskIDWhitespace):
		return fmt.Errorf("%s %q must not contain whitespace: a %s ID is alphanumeric", field, id, kind)
	case errors.Is(err, ErrTaskIDTooLong):
		return fmt.Errorf("%s is %d characters long: want at most %d, since it names a file",
			field, len(id), MaxIDLength)
	default:
		return err
	}
}
