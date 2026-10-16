package main

import (
	"strings"

	"github.com/moby/swarmkit/v2/api"
)

// taskErrorReasonWidth is the maximum number of characters the ERROR column
// prints. A reason is unbounded in SwarmKit (a constraint-enforcer message names
// every rejected node), so one long value would otherwise push the whole table
// out of shape; anything past this width is ellipsised.
const taskErrorReasonWidth = 40

// taskErrorReason is the operator-facing reason a task is not running, as
// SwarmKit records it on the task status. Status.Err carries what an operator
// needs to act on — the scheduler's verdict on an unschedulable task ("no
// suitable node (...)"), the constraint enforcer's rejections, and the executor's
// healthcheck failures — while Status.Message carries routine notes on a state
// transition ("prepared", "started"). Err therefore wins, and Message is the
// fallback for a task that is stuck without one. A nil task, or one carrying
// neither, yields "".
func taskErrorReason(task *api.Task) string {
	if task == nil {
		return ""
	}
	if task.Status.Err != "" {
		return task.Status.Err
	}
	return task.Status.Message
}

// taskErrorColumn renders taskErrorReason as one table cell: runs of whitespace
// (including newlines a multi-line error may carry) collapse to single spaces so
// the reason cannot break the row layout, and the result is truncated to
// taskErrorReasonWidth with the project's truncateMiddle, the same treatment
// `vm list` gives an over-long image or service name.
func taskErrorColumn(task *api.Task) string {
	reason := strings.Join(strings.Fields(taskErrorReason(task)), " ")
	return truncateMiddle(reason, taskErrorReasonWidth)
}
