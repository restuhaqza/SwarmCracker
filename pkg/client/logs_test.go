package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// writeFrames writes NDJSON log frames, the wire format of /v1/tasks/{id}/logs.
func writeFrames(t *testing.T, w http.ResponseWriter, frames ...v1.LogFrame) {
	t.Helper()
	w.Header().Set("Content-Type", v1.NDJSONMediaType)
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	for _, frame := range frames {
		require.NoError(t, enc.Encode(frame))
	}
}

// TestLogsTask_StreamsLinesAndSendsQuery pins the whole task path: the request
// carries tail/since/follow and the stream is decoded into lines even when a
// frame splits one.
func TestLogsTask_StreamsLinesAndSendsQuery(t *testing.T) {
	var gotQuery map[string]string

	var out bytes.Buffer
	c := newTestRemote(t, Config{Token: "s3cret"}, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/tasks/abc123/logs", r.URL.Path)
		assert.Equal(t, v1.NDJSONMediaType, r.Header.Get("Accept"))
		assert.Equal(t, "Bearer s3cret", r.Header.Get("Authorization"))
		gotQuery = map[string]string{
			"tail":   r.URL.Query().Get("tail"),
			"since":  r.URL.Query().Get("since"),
			"follow": r.URL.Query().Get("follow"),
		}

		// One line split across two frames, then a whole line.
		writeFrames(t, w,
			v1.LogDataFrame(time.Unix(0, 0), []byte("hel")),
			v1.LogDataFrame(time.Unix(0, 0), []byte("lo\nwor")),
			v1.LogDataFrame(time.Unix(0, 0), []byte("ld\n")),
			v1.LogEOFFrame(),
		)
	})

	since := time.Date(2026, 10, 28, 8, 0, 0, 0, time.UTC)
	err := c.LogsTask(context.Background(), "abc123", LogsOptions{Tail: 3, Since: since, Follow: true}, &out)
	require.NoError(t, err)

	assert.Equal(t, "hello\nworld\n", out.String())
	assert.Equal(t, "3", gotQuery["tail"])
	assert.Equal(t, since.Format(time.RFC3339Nano), gotQuery["since"])
	assert.Equal(t, "true", gotQuery["follow"])
}

// TestLogsTask_TailClampsToTheNodeCap pins the documented meaning of --tail -1:
// "as much as the node serves", not an unbounded request.
func TestLogsTask_TailClampsToTheNodeCap(t *testing.T) {
	var tail string
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		tail = r.URL.Query().Get("tail")
		writeFrames(t, w, v1.LogEOFFrame())
	})

	require.NoError(t, c.LogsTask(context.Background(), "t1", LogsOptions{Tail: -1}, &bytes.Buffer{}))
	assert.Equal(t, fmt.Sprintf("%d", MaxRemoteLogTail), tail)

	require.NoError(t, c.LogsTask(context.Background(), "t1", LogsOptions{Tail: MaxRemoteLogTail + 500}, &bytes.Buffer{}))
	assert.Equal(t, fmt.Sprintf("%d", MaxRemoteLogTail), tail)

	require.NoError(t, c.LogsTask(context.Background(), "t1", LogsOptions{Tail: 0}, &bytes.Buffer{}))
	assert.Equal(t, "0", tail, "0 means no backlog, which is what --follow alone wants")
}

// TestLogsTask_NoHostIsAUsageError pins that the local path is never silently
// used for a remote request.
func TestLogsTask_NoHostIsAUsageError(t *testing.T) {
	c := &Client{}
	err := c.LogsTask(context.Background(), "t1", LogsOptions{}, &bytes.Buffer{})
	require.Error(t, err)
	assert.Equal(t, output.CodeUsage, codeOf(t, err).Code)
}

// TestLogsTask_TypedErrorIsPreserved pins that a 404 from the manager reaches the
// caller with its stable code, so a caller can distinguish "no such task" from a
// transport failure.
func TestLogsTask_TypedErrorIsPreserved(t *testing.T) {
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		writeTestDocument(w, http.StatusNotFound, v1.NewErrorDocument(v1.Error{
			Code:    string(output.CodeNotFound),
			Message: "task \"zzz\" not found",
		}))
	})

	err := c.LogsTask(context.Background(), "zzz", LogsOptions{}, &bytes.Buffer{})
	require.Error(t, err)
	typed := codeOf(t, err)
	assert.Equal(t, output.CodeNotFound, typed.Code)
	assert.Contains(t, typed.Message, "not found")
}

// TestServiceLogs_MergesEveryTask pins the aggregation: one stream, one line per
// task line, attributed by task ID, and a task with no log skipped rather than
// failing the whole command.
func TestServiceLogs_MergesEveryTask(t *testing.T) {
	taskA := "aaaaaaaaaaaa1111111111111"
	taskB := "bbbbbbbbbbbb2222222222222"
	taskC := "cccccccccccc3333333333333" // never started: no log

	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/services/web/tasks":
			writeTestDocument(w, http.StatusOK, v1.NewTaskList([]v1.Task{
				{ID: taskA}, {ID: taskB}, {ID: taskC},
			}))
		case "/v1/tasks/" + taskA + "/logs":
			writeFrames(t, w, v1.LogDataFrame(time.Unix(0, 0), []byte("from-a\n")), v1.LogEOFFrame())
		case "/v1/tasks/" + taskB + "/logs":
			writeFrames(t, w, v1.LogDataFrame(time.Unix(0, 0), []byte("from-b\n")), v1.LogEOFFrame())
		case "/v1/tasks/" + taskC + "/logs":
			writeTestDocument(w, http.StatusNotFound, v1.NewErrorDocument(v1.Error{
				Code:    string(output.CodeNotFound),
				Message: "task has no log",
			}))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	})

	var out bytes.Buffer
	require.NoError(t, c.ServiceLogs(context.Background(), "web", LogsOptions{Tail: -1}, &out))

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	require.Len(t, lines, 2, "one line per task that has a log; got %q", out.String())
	// Streams are read concurrently by design, so the merge order is arrival
	// order, not task order: only the attribution is guaranteed.
	assert.ElementsMatch(t, []string{
		"aaaaaaaaaaaa | from-a",
		"bbbbbbbbbbbb | from-b",
	}, lines)
}

// TestServiceLogs_NoTasksIsNotFound keeps an empty service from looking like
// success with no output.
func TestServiceLogs_NoTasksIsNotFound(t *testing.T) {
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		writeTestDocument(w, http.StatusOK, v1.NewTaskList(nil))
	})

	err := c.ServiceLogs(context.Background(), "empty", LogsOptions{}, &bytes.Buffer{})
	require.Error(t, err)
	assert.Equal(t, output.CodeNotFound, codeOf(t, err).Code)
}

// TestServiceLogs_RequiresHost pins the documented transport rule: only the
// manager can reach every node's console file, so a local client must not
// pretend to aggregate.
func TestServiceLogs_RequiresHost(t *testing.T) {
	c := &Client{}
	err := c.ServiceLogs(context.Background(), "web", LogsOptions{}, &bytes.Buffer{})
	require.Error(t, err)
	assert.Equal(t, output.CodeUsage, codeOf(t, err).Code)
}

// TestPumpLogStream_FlushesPartialLineAtEOF covers a console write that ends
// mid-line: the bytes are still the guest's last output and must be shown.
func TestPumpLogStream_FlushesPartialLineAtEOF(t *testing.T) {
	body := strings.NewReader(
		`{"type":"log","stream":"stdout","data":"dGFpbA=="}` + "\n" + // "tail", no newline
			`{"type":"eof"}` + "\n")

	var lines []string
	require.NoError(t, pumpLogStream(context.Background(), body, func(line []byte) {
		lines = append(lines, string(line))
	}))
	assert.Equal(t, []string{"tail"}, lines)
}
