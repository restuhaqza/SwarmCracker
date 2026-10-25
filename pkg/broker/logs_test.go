package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// writeLogFile writes lines (each gets a trailing newline) and returns the task
// the handler resolves to. The socket file makes it a running task, which a
// follow stream needs: liveness is the socket, not the log.
func writeLogFile(t *testing.T, lines ...string) Task {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "task-1.log")
	socketPath := filepath.Join(dir, "task-1.sock")
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
	require.NoError(t, os.WriteFile(socketPath, nil, 0o600))

	return Task{ID: "task-1", SocketPath: socketPath, Running: true, LogPath: path}
}

// runLogsHandler runs the handler with a request for path and returns the
// recorded response.
func runLogsHandler(t *testing.T, ctx context.Context, task Task, target string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	NewLogsHandler()(ctx, task, rec, req)
	return rec
}

// decodeLogFrames parses every NDJSON frame in a recorded body.
func decodeLogFrames(t *testing.T, body []byte) []v1.LogFrame {
	t.Helper()

	frames := []v1.LogFrame{}
	decoder := json.NewDecoder(bytes.NewReader(body))
	for {
		var frame v1.LogFrame
		err := decoder.Decode(&frame)
		if errors.Is(err, io.EOF) {
			return frames
		}
		require.NoError(t, err)
		frames = append(frames, frame)
	}
}

// waitForLine waits until the streamed frames contain want. The response is read
// while the handler runs, so the test never relies on a flush count alone.
func waitForLine(t *testing.T, rec *streamRecorder, want string) {
	t.Helper()

	require.Eventually(t, func() bool {
		body := rec.Body()
		if body == "" {
			return false
		}
		return slices.Contains(frameLines(decodeLogFrames(t, []byte(body))), want)
	}, 3*time.Second, 20*time.Millisecond)
}

// frameLines returns the decoded data of every log frame, in order.
func frameLines(frames []v1.LogFrame) []string {
	lines := make([]string, 0, len(frames))
	for _, frame := range frames {
		if frame.Type == v1.LogFrameTypeLog {
			lines = append(lines, strings.TrimSuffix(string(frame.Data), "\n"))
		}
	}
	return lines
}

func numberedLines(from, to int) []string {
	lines := make([]string, 0, to-from+1)
	for i := from; i <= to; i++ {
		lines = append(lines, "line-"+strconv.Itoa(i))
	}
	return lines
}

func TestLogsHandlerStreamsBacklogAndEOF(t *testing.T) {
	task := writeLogFile(t, "first", "second")

	rec := runLogsHandler(t, context.Background(), task, "/v1/tasks/task-1/logs")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, v1.NDJSONMediaType, rec.Header().Get("Content-Type"))
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))

	frames := decodeLogFrames(t, rec.Body.Bytes())
	require.Len(t, frames, 3)
	assert.Equal(t, v1.LogFrameTypeLog, frames[0].Type)
	assert.Equal(t, v1.LogStreamStdout, frames[0].Stream, "the console log merges stdout and stderr")
	assert.Equal(t, []byte("first\n"), frames[0].Data)
	assert.Equal(t, []byte("second\n"), frames[1].Data)
	assert.Equal(t, v1.LogFrameTypeEOF, frames[2].Type)
	assert.Empty(t, frames[2].Data)
	assert.Empty(t, frames[2].Stream, "eof carries no stream")
}

// TestLogsHandlerTimestampsBacklogWithTheFileMTime pins the documented
// approximation: the console file has no per-line timestamps, so backlog lines
// carry the file's modification time.
func TestLogsHandlerTimestampsBacklogWithTheFileMTime(t *testing.T) {
	task := writeLogFile(t, "first")
	mtime := time.Date(2026, 10, 1, 8, 30, 0, 0, time.UTC)
	require.NoError(t, os.Chtimes(task.LogPath, mtime, mtime))

	rec := runLogsHandler(t, context.Background(), task, "/v1/tasks/task-1/logs")
	frames := decodeLogFrames(t, rec.Body.Bytes())
	require.NotEmpty(t, frames)

	assert.Equal(t, mtime.Format(time.RFC3339Nano), frames[0].Time)
}

func TestLogsHandlerTail(t *testing.T) {
	task := writeLogFile(t, numberedLines(1, 250)...)

	t.Run("default", func(t *testing.T) {
		rec := runLogsHandler(t, context.Background(), task, "/v1/tasks/task-1/logs")
		lines := frameLines(decodeLogFrames(t, rec.Body.Bytes()))
		require.Len(t, lines, 200)
		assert.Equal(t, "line-51", lines[0])
		assert.Equal(t, "line-250", lines[len(lines)-1])
	})

	t.Run("explicit", func(t *testing.T) {
		rec := runLogsHandler(t, context.Background(), task, "/v1/tasks/task-1/logs?tail=3")
		lines := frameLines(decodeLogFrames(t, rec.Body.Bytes()))
		assert.Equal(t, []string{"line-248", "line-249", "line-250"}, lines)
	})

	t.Run("larger than the file", func(t *testing.T) {
		rec := runLogsHandler(t, context.Background(), task, "/v1/tasks/task-1/logs?tail=10000")
		lines := frameLines(decodeLogFrames(t, rec.Body.Bytes()))
		assert.Len(t, lines, 250)
	})

	t.Run("zero", func(t *testing.T) {
		rec := runLogsHandler(t, context.Background(), task, "/v1/tasks/task-1/logs?tail=0")
		frames := decodeLogFrames(t, rec.Body.Bytes())
		require.Len(t, frames, 1, "tail=0 asks for no backlog, only the eof")
		assert.Equal(t, v1.LogFrameTypeEOF, frames[0].Type)
	})
}

func TestLogsHandlerSince(t *testing.T) {
	task := writeLogFile(t, "first", "second")
	mtime := time.Date(2026, 10, 1, 8, 30, 0, 0, time.UTC)
	require.NoError(t, os.Chtimes(task.LogPath, mtime, mtime))

	t.Run("before the file was written", func(t *testing.T) {
		rec := runLogsHandler(t, context.Background(), task,
			"/v1/tasks/task-1/logs?since="+mtime.Add(-time.Hour).Format(time.RFC3339))
		assert.Len(t, frameLines(decodeLogFrames(t, rec.Body.Bytes())), 2)
	})

	t.Run("after the file was written", func(t *testing.T) {
		rec := runLogsHandler(t, context.Background(), task,
			"/v1/tasks/task-1/logs?since="+mtime.Add(time.Hour).Format(time.RFC3339))
		assert.Empty(t, frameLines(decodeLogFrames(t, rec.Body.Bytes())))
	})

	t.Run("accepts nano precision", func(t *testing.T) {
		since := mtime.Add(-time.Nanosecond).Format(time.RFC3339Nano)
		rec := runLogsHandler(t, context.Background(), task, "/v1/tasks/task-1/logs?since="+since)
		assert.Len(t, frameLines(decodeLogFrames(t, rec.Body.Bytes())), 2)
	})
}

func TestLogsHandlerMissingLogIsNotFound(t *testing.T) {
	t.Run("no log path", func(t *testing.T) {
		rec := runLogsHandler(t, context.Background(), Task{ID: "task-1"}, "/v1/tasks/task-1/logs")
		require.Equal(t, http.StatusNotFound, rec.Code)

		doc := decodeErrorDocument(t, rec.Body)
		assert.Equal(t, string(output.CodeNotFound), doc.Error.Code)
	})

	t.Run("file does not exist", func(t *testing.T) {
		task := Task{ID: "task-1", LogPath: filepath.Join(t.TempDir(), "task-1.log")}
		rec := runLogsHandler(t, context.Background(), task, "/v1/tasks/task-1/logs")
		require.Equal(t, http.StatusNotFound, rec.Code)

		doc := decodeErrorDocument(t, rec.Body)
		assert.Equal(t, string(output.CodeNotFound), doc.Error.Code)
		assert.NotContains(t, rec.Body.String(), "task-1.log", "the log path stays node-local")
	})
}

func TestLogsHandlerRejectsInvalidQuery(t *testing.T) {
	task := writeLogFile(t, "first")

	cases := map[string]string{
		"negative tail":    "?tail=-1",
		"non-numeric tail": "?tail=abc",
		"empty tail":       "?tail=",
		"invalid since":    "?since=yesterday",
		"invalid follow":   "?follow=maybe",
	}

	for name, query := range cases {
		t.Run(name, func(t *testing.T) {
			rec := runLogsHandler(t, context.Background(), task, "/v1/tasks/task-1/logs"+query)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

			doc := decodeErrorDocument(t, rec.Body)
			assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
		})
	}
}

// TestLogsHandlerTailIsCapped pins that a tail above the maximum is clamped
// rather than refused: the only invalid tail values are negative and
// non-numeric.
func TestLogsHandlerTailIsCapped(t *testing.T) {
	task := writeLogFile(t, numberedLines(1, 250)...)

	rec := runLogsHandler(t, context.Background(), task, "/v1/tasks/task-1/logs?tail=999999")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Len(t, frameLines(decodeLogFrames(t, rec.Body.Bytes())), 250)
}

// streamRecorder is a concurrency-safe response writer: a following handler
// writes from its own goroutine while the test reads and flushes are counted, so
// httptest.ResponseRecorder (which is not safe for concurrent use) cannot be
// used. It also counts flushes, which is how the per-frame flush rule is pinned.
type streamRecorder struct {
	mu      sync.Mutex
	header  http.Header
	body    bytes.Buffer
	status  int
	flushes int
}

func newStreamRecorder() *streamRecorder {
	return &streamRecorder{header: make(http.Header)}
}

func (r *streamRecorder) Header() http.Header { return r.header }

func (r *streamRecorder) WriteHeader(status int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == 0 {
		r.status = status
	}
}

func (r *streamRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(p)
}

func (r *streamRecorder) Flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flushes++
}

// Status is the status the handler wrote, or 0 before the header.
func (r *streamRecorder) Status() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

// Body returns everything written so far.
func (r *streamRecorder) Body() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.String()
}

// Flushes returns the flush count.
func (r *streamRecorder) Flushes() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.flushes
}

func TestLogsHandlerFlushesEveryFrame(t *testing.T) {
	task := writeLogFile(t, "one", "two", "three")

	rec := newStreamRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/tasks/task-1/logs", nil)
	NewLogsHandler()(context.Background(), task, rec, req)

	require.Equal(t, http.StatusOK, rec.Status())
	assert.GreaterOrEqual(t, rec.Flushes(), 4,
		"three log frames plus the eof must each be flushed")
}

// TestLogsHandlerFollowStreamsNewLines proves follow streams bytes written after
// the handler started and never invents an eof: the stream ends when the caller
// cancels.
func TestLogsHandlerFollowStreamsNewLines(t *testing.T) {
	task := writeLogFile(t, "before")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	rec := newStreamRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/tasks/task-1/logs?follow=true", nil).WithContext(ctx)
	handler := NewLogsHandler()
	go func() {
		defer close(done)
		handler(ctx, task, rec, req)
	}()

	// The backlog frame is flushed before the test appends anything.
	waitForLine(t, rec, "before")

	f, err := os.OpenFile(task.LogPath, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString("after\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	waitForLine(t, rec, "after")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the follow handler must stop when the request context is cancelled")
	}

	frames := decodeLogFrames(t, []byte(rec.Body()))
	assert.Equal(t, []string{"before", "after"}, frameLines(frames))
	for _, frame := range frames {
		assert.NotEqual(t, v1.LogFrameTypeEOF, frame.Type,
			"a following stream must not invent an eof")
	}
}

// TestLogsHandlerFollowEndsWhenTheTaskDisappears pins that a follow stream whose
// log file is removed ends instead of polling a deleted task forever.
func TestLogsHandlerFollowEndsWhenTheTaskDisappears(t *testing.T) {
	task := writeLogFile(t, "before")

	done := make(chan struct{})
	rec := newStreamRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/tasks/task-1/logs?follow=true", nil)
	handler := NewLogsHandler()
	go func() {
		defer close(done)
		handler(context.Background(), task, rec, req)
	}()

	waitForLine(t, rec, "before")

	require.NoError(t, os.Remove(task.LogPath))

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("a follow stream must end when the task's log disappears")
	}
}

func TestLogsHandlerCancelledContextBeforeStart(t *testing.T) {
	task := writeLogFile(t, "before")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		NewLogsHandler()(ctx, task, httptest.NewRecorder(),
			httptest.NewRequest(http.MethodGet, "/v1/tasks/task-1/logs", nil).WithContext(ctx))
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the handler must honour an already-cancelled context")
	}
}

// TestLogsHandlerFollowsPartialLine pins that a trailing line without a newline
// is still emitted: the console log is a byte stream that may end mid-line.
func TestLogsHandlerFollowsPartialLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "task-1.log")
	require.NoError(t, os.WriteFile(path, []byte("no trailing newline"), 0o600))
	task := Task{ID: "task-1", LogPath: path}

	rec := runLogsHandler(t, context.Background(), task, "/v1/tasks/task-1/logs")
	frames := decodeLogFrames(t, rec.Body.Bytes())
	require.Len(t, frames, 2)
	assert.Equal(t, []byte("no trailing newline"), frames[0].Data)
	assert.Equal(t, v1.LogFrameTypeEOF, frames[1].Type)
}

// TestLogsHandlerFollowHoldsBackAPartialLine pins the follow half of that rule:
// a follower waits for the newline instead of framing half a line, so one line
// is not split across a poll boundary.
func TestLogsHandlerFollowHoldsBackAPartialLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "task-1.log")
	socketPath := filepath.Join(dir, "task-1.sock")
	require.NoError(t, os.WriteFile(path, []byte("partial"), 0o600))
	require.NoError(t, os.WriteFile(socketPath, nil, 0o600))
	task := Task{ID: "task-1", SocketPath: socketPath, Running: true, LogPath: path}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	rec := newStreamRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/tasks/task-1/logs?follow=true", nil).WithContext(ctx)
	go func() {
		defer close(done)
		NewLogsHandler()(ctx, task, rec, req)
	}()

	require.Never(t, func() bool {
		return len(decodeLogFrames(t, []byte(rec.Body()))) > 0
	}, 2*logPollInterval, 20*time.Millisecond,
		"a partial line must not be framed before its newline arrives")

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString("\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	waitForLine(t, rec, "partial")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the follow handler must stop when the request context is cancelled")
	}
}

// TestLogsHandlerServesTheLogOfAnExitedTask pins that logs do not depend on a
// live socket: the socket source resolves a log-only task after the VM stops,
// and the handler serves its backlog.
func TestLogsHandlerServesTheLogOfAnExitedTask(t *testing.T) {
	socketDir := t.TempDir()
	logDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(logDir, "task-1.log"), []byte("boom\n"), 0o600))

	task, ok, err := SocketDirSource{Dir: socketDir, LogDir: logDir}.Lookup(context.Background(), "task-1")
	require.NoError(t, err)
	require.True(t, ok)

	rec := runLogsHandler(t, context.Background(), task, "/v1/tasks/task-1/logs")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	frames := decodeLogFrames(t, rec.Body.Bytes())
	require.Len(t, frames, 2)
	assert.Equal(t, []byte("boom\n"), frames[0].Data)
	assert.Equal(t, v1.LogFrameTypeEOF, frames[1].Type)
}

// TestLogsHandlerFollowEndsWhenTheTaskStops pins that a follow stream ends when
// the VM goes away even though the console log remains: polling a static file
// forever leaves the client with no terminal condition.
func TestLogsHandlerFollowEndsWhenTheTaskStops(t *testing.T) {
	task := writeLogFile(t, "before")

	done := make(chan struct{})
	rec := newStreamRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/tasks/task-1/logs?follow=true", nil)
	go func() {
		defer close(done)
		NewLogsHandler()(context.Background(), task, rec, req)
	}()

	waitForLine(t, rec, "before")

	require.NoError(t, os.Remove(task.SocketPath))
	require.FileExists(t, task.LogPath, "the log outlives the task, so it cannot be the liveness signal")

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("a follow stream must end when the task stops running")
	}
}

// TestLogsHandlerFollowOfAnExitedTaskEndsAfterTheBacklog pins the log-only case:
// there is nothing to follow, so the stream ends after the backlog and still
// invents no eof.
func TestLogsHandlerFollowOfAnExitedTaskEndsAfterTheBacklog(t *testing.T) {
	socketDir := t.TempDir()
	logDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(logDir, "task-1.log"), []byte("boom\n"), 0o600))

	task, ok, err := SocketDirSource{Dir: socketDir, LogDir: logDir}.Lookup(context.Background(), "task-1")
	require.NoError(t, err)
	require.True(t, ok)

	done := make(chan struct{})
	rec := newStreamRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/tasks/task-1/logs?follow=true", nil)
	go func() {
		defer close(done)
		NewLogsHandler()(context.Background(), task, rec, req)
	}()

	waitForLine(t, rec, "boom")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a follow stream of a task that is already gone must end")
	}

	for _, frame := range decodeLogFrames(t, []byte(rec.Body())) {
		assert.NotEqual(t, v1.LogFrameTypeEOF, frame.Type,
			"a following stream must not invent an eof")
	}
}

// TestLogsHandlerLongLineIsFramedInBoundedChunks pins that a line longer than
// the read buffer is split into buffer-sized frames rather than accumulated: the
// concatenated data is still the exact line, and no frame exceeds the buffer.
func TestLogsHandlerLongLineIsFramedInBoundedChunks(t *testing.T) {
	long := strings.Repeat("x", 2*logReadBuffer+123)
	task := writeLogFile(t, long)

	rec := runLogsHandler(t, context.Background(), task, "/v1/tasks/task-1/logs")
	frames := decodeLogFrames(t, rec.Body.Bytes())
	require.Greater(t, len(frames), 2, "a line longer than the buffer must be chunked")

	var joined strings.Builder
	for _, frame := range frames[:len(frames)-1] {
		require.Equal(t, v1.LogFrameTypeLog, frame.Type)
		require.LessOrEqual(t, len(frame.Data), logReadBuffer,
			"no frame may exceed the read buffer")
		joined.Write(frame.Data)
	}
	assert.Equal(t, long+"\n", joined.String())
	assert.Equal(t, v1.LogFrameTypeEOF, frames[len(frames)-1].Type)
}

// newlineFreeRecorder decodes each frame but keeps only its size, so a test can
// observe a huge newline-free log without itself retaining the whole body.
type newlineFreeRecorder struct {
	header    http.Header
	status    int
	frames    int
	totalData int64
	maxData   int
}

func (r *newlineFreeRecorder) Header() http.Header { return r.header }

func (r *newlineFreeRecorder) WriteHeader(status int) { r.status = status }

func (r *newlineFreeRecorder) Write(p []byte) (int, error) {
	var frame v1.LogFrame
	if err := json.Unmarshal(bytes.TrimSpace(p), &frame); err != nil {
		return 0, err
	}
	if frame.Type == v1.LogFrameTypeLog {
		r.frames++
		r.totalData += int64(len(frame.Data))
		if len(frame.Data) > r.maxData {
			r.maxData = len(frame.Data)
		}
	}
	return len(p), nil
}

// TestLogsHandlerBoundsANewlineFreeLog covers the shape that defeats a line
// reader: a console log with no newline at all (a progress bar using \r, binary
// output, `cat /dev/urandom`). tailStart finds no line boundary and returns 0,
// so the whole file is in the backlog; the handler must still frame it in
// buffer-sized chunks instead of allocating the region.
func TestLogsHandlerBoundsANewlineFreeLog(t *testing.T) {
	content := strings.Repeat("y", 3*logReadBuffer+17)
	dir := t.TempDir()
	path := filepath.Join(dir, "task-1.log")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	task := Task{ID: "task-1", LogPath: path}

	rec := &newlineFreeRecorder{header: make(http.Header)}
	req := httptest.NewRequest(http.MethodGet, "/v1/tasks/task-1/logs", nil)
	NewLogsHandler()(context.Background(), task, rec, req)

	require.Equal(t, http.StatusOK, rec.status)
	require.Greater(t, rec.frames, 1, "the newline-free region must be chunked")
	assert.Equal(t, int64(len(content)), rec.totalData, "chunking must not lose bytes")
	assert.LessOrEqual(t, rec.maxData, logReadBuffer,
		"a frame must never hold the whole newline-free region")
}
