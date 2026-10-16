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

// taskErrorReason is the operator-facing reason a task is not running: the
// task's Status.Err verbatim, or "" for a nil task or one carrying no Err.
//
// Status.Err is deliberately the *only* source. SwarmKit's field contract
// (vendored api/types.proto:584-591, "TaskStatus") reserves Err for "messages
// that should be surfaced to users" and Message for "notes on routine state
// transitions": the agent's controller writes Message="accepted"/"prepared"/
// "started"/"finished" on the success path (agent/exec/controller.go) and the
// scheduler clears Err once a task is placed (manager/scheduler/scheduler.go),
// so a healthy RUNNING task would print "started" in the ERROR column were
// Message a fallback. Stuck tasks always carry Err — the scheduler's verdict on
// an unschedulable task, the constraint/volume enforcers' rejections, and the
// executor's healthcheck failures. Do not re-add a Message fallback: it has no
// "task is not running" meaning.
func taskErrorReason(task *api.Task) string {
	if task == nil {
		return ""
	}
	return task.Status.Err
}

// taskErrorColumn renders taskErrorReason as one table cell: runs of whitespace
// (including newlines a multi-line error may carry) collapse to single spaces so
// the reason cannot break the row layout, and the result is truncated to
// taskErrorReasonWidth with the project's rune-aware truncateMiddle, the same
// treatment `vm list` gives an over-long image or service name.
func taskErrorColumn(task *api.Task) string {
	reason := strings.Join(strings.Fields(taskErrorReason(task)), " ")
	return truncateMiddle(reason, taskErrorReasonWidth)
}
