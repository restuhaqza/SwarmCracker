package v1

// This file owns the task log wire contract. A task log is served as NDJSON —
// the same documented exception as exec — because output is followed while the
// task runs. The Firecracker console merges stdout and stderr, so Stream is
// always stdout, and the console file carries no per-line timestamps, so Time is
// the best approximation the node can produce (see pkg/broker/logs.go).

import "time"

// Log frame types.
const (
	LogFrameTypeLog = "log"
	LogFrameTypeEOF = "eof"
)

// LogStreamStdout is the only stream a task log carries: the console file merges
// stdout and stderr.
const LogStreamStdout = "stdout"

// LogFrame is one line of a task log stream. Data carries raw console bytes
// (base64 in JSON); Time is RFC3339Nano; Stream is set on a log frame and empty
// on eof.
type LogFrame struct {
	Type   string `json:"type"`             // log|eof
	Stream string `json:"stream,omitempty"` // stdout (the console log is merged)
	Time   string `json:"time,omitempty"`   // RFC3339Nano
	Data   []byte `json:"data,omitempty"`   // base64 in JSON
}

// LogDataFrame returns one log frame carrying data observed at at.
func LogDataFrame(at time.Time, data []byte) LogFrame {
	return LogFrame{
		Type:   LogFrameTypeLog,
		Stream: LogStreamStdout,
		Time:   at.UTC().Format(time.RFC3339Nano),
		Data:   data,
	}
}

// LogEOFFrame returns the terminal frame of a non-following log stream. A
// following stream never sends one: it ends when the caller disconnects or the
// task disappears.
func LogEOFFrame() LogFrame {
	return LogFrame{Type: LogFrameTypeEOF}
}
