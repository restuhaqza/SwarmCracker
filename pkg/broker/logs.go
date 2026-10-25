package broker

// This file owns the node-side log handler: GET /v1/tasks/{taskID}/logs on the
// internal broker.
//
// The source is the task's Firecracker console log (<logDir>/<taskID>.log). The
// console merges stdout and stderr, so every frame is stream "stdout", and the
// file carries no per-line timestamps. The timestamps below are therefore an
// approximation: backlog lines carry the file's modification time and lines
// observed while following carry the time they were read. A caller that needs
// exact ordering inside one poll interval must use the frame order, which is
// always oldest to newest.
//
// The response is application/x-ndjson — the documented exception to the v1
// envelope — so output is followed while the task runs. Frames are written and
// flushed one at a time; a single frame and a single read never exceed the read
// buffer, so neither the backlog nor a newline-free line is ever held in memory.
// The console file outlives the task, so logs stay readable after a task exits;
// a follow stream ends when the VM stops or the log disappears.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

const (
	// defaultLogTail is the backlog length when the caller names none.
	defaultLogTail = 200
	// maxLogTail caps the backlog: a log is tailed, not downloaded.
	maxLogTail = 10000
	// logPollInterval is how often a following stream checks for appended
	// bytes. It is deliberately short: a console log is interactive output.
	logPollInterval = 250 * time.Millisecond
	// logReadBuffer bounds one read and one frame. A line longer than the
	// buffer — a progress bar using \r, or binary output — is framed in
	// buffer-sized chunks instead of being accumulated, so no frame and no
	// allocation grows with the line.
	logReadBuffer = 64 * 1024
	// logScanBlock is the block size of the backwards tail scan.
	logScanBlock = 32 * 1024
)

// logsQuery is the validated query of a log request.
type logsQuery struct {
	tail   int
	since  time.Time
	follow bool
}

// NewLogsHandler builds the log operation. The source path comes from
// Task.LogPath, which SocketDirSource derives from the node's log directory.
func NewLogsHandler() Handler {
	return func(ctx context.Context, task Task, w http.ResponseWriter, r *http.Request) {
		serveLogs(ctx, task, w, r)
	}
}

// serveLogs validates the query, opens the log and streams it.
func serveLogs(ctx context.Context, task Task, w http.ResponseWriter, r *http.Request) {
	query, status, queryErr := parseLogsQuery(r)
	if queryErr != nil {
		writeTypedError(w, status, queryErr)
		return
	}

	file, info, status, openErr := openTaskLog(task)
	if openErr != nil {
		writeTypedError(w, status, openErr)
		return
	}
	defer func() { _ = file.Close() }()

	// The tail offset is computed before the header so a read failure is still
	// a typed error document rather than a truncated stream.
	start, tailErr := tailStart(file, info.Size(), query.tail)
	if tailErr != nil {
		writeError(w, http.StatusServiceUnavailable, codeUnavailable,
			"task %q log is unavailable", task.ID)
		return
	}

	// Backlog: oldest to newest. tail applies after since, and since can only
	// compare against the file's modification time: the console log has no
	// per-line timestamps. A backlog the file's mtime excludes still starts the
	// follow stream at the current end of the file.
	if !query.since.IsZero() && info.ModTime().Before(query.since) {
		start = info.Size()
	}

	stream := &logStream{w: w, flusher: flusherFor(w)}
	stream.writeHeader()

	// A following stream keeps a trailing partial line for the next poll; a
	// finite one emits it, because no later bytes will complete it.
	next, readErr := stream.emit(file, start, info.Size(), info.ModTime(), !query.follow)
	if readErr != nil {
		return
	}
	if !query.follow {
		_ = stream.frame(v1.LogEOFFrame())
		return
	}

	stream.follow(ctx, task, file, next, query.since)
}

// parseLogsQuery validates tail, since and follow. Every rejection is a typed
// 400: a caller that mistypes a parameter must not receive a stream it cannot
// interpret.
func parseLogsQuery(r *http.Request) (logsQuery, int, *output.Error) {
	values := r.URL.Query()
	query := logsQuery{tail: defaultLogTail}

	if values.Has("tail") {
		tail, err := strconv.Atoi(values.Get("tail"))
		if err != nil || tail < 0 {
			return query, http.StatusBadRequest, output.NewError(output.CodeInvalid,
				"tail must be a non-negative integer").
				WithDetails(map[string]any{"parameter": "tail"})
		}
		if tail > maxLogTail {
			tail = maxLogTail
		}
		query.tail = tail
	}

	if values.Has("since") {
		since, err := time.Parse(time.RFC3339Nano, values.Get("since"))
		if err != nil {
			return query, http.StatusBadRequest, output.NewError(output.CodeInvalid,
				"since must be an RFC3339 timestamp").
				WithDetails(map[string]any{"parameter": "since"})
		}
		query.since = since
	}

	if values.Has("follow") {
		follow, err := strconv.ParseBool(values.Get("follow"))
		if err != nil {
			return query, http.StatusBadRequest, output.NewError(output.CodeInvalid,
				"follow must be true or false").
				WithDetails(map[string]any{"parameter": "follow"})
		}
		query.follow = follow
	}

	return query, 0, nil
}

// openTaskLog opens the task's console log. A task without a log path, a
// missing file and a non-regular file are all not_found: there is no log to
// serve. The path stays node-local, so no answer names it.
func openTaskLog(task Task) (*os.File, os.FileInfo, int, *output.Error) {
	if task.LogPath == "" {
		return nil, nil, http.StatusNotFound, output.NewErrorf(output.CodeNotFound,
			"task %q has no log", task.ID)
	}

	file, err := os.Open(task.LogPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, http.StatusNotFound, output.NewErrorf(output.CodeNotFound,
				"task %q has no log", task.ID)
		}
		return nil, nil, http.StatusServiceUnavailable, output.NewErrorf(output.CodeUnavailable,
			"task %q log is unavailable", task.ID)
	}

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, http.StatusNotFound, output.NewErrorf(output.CodeNotFound,
			"task %q has no log", task.ID)
	}

	return file, info, 0, nil
}

// tailStart returns the offset where the last n complete lines begin. A trailing
// newline terminates the last line rather than starting an empty one.
func tailStart(file *os.File, size int64, n int) (int64, error) {
	if n <= 0 || size <= 0 {
		return size, nil
	}

	offset := size
	seen := 0
	for offset > 0 {
		blockSize := int64(logScanBlock)
		if blockSize > offset {
			blockSize = offset
		}
		offset -= blockSize

		block := make([]byte, blockSize)
		if _, err := file.ReadAt(block, offset); err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		for i := len(block) - 1; i >= 0; i-- {
			if block[i] != '\n' {
				continue
			}
			if offset+int64(i) == size-1 {
				continue // the terminator of the last line
			}
			seen++
			if seen == n {
				return offset + int64(i) + 1, nil
			}
		}
	}
	return 0, nil
}

// logStream writes NDJSON frames and flushes each one.
type logStream struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

// writeHeader sends the stream headers and 200. It runs after the log is open,
// so an earlier failure is still a typed error document.
func (s *logStream) writeHeader() {
	s.w.Header().Set("Content-Type", v1.NDJSONMediaType)
	s.w.Header().Set("X-Content-Type-Options", "nosniff")
	s.w.WriteHeader(http.StatusOK)
	s.flush()
}

// frame writes and flushes one frame.
func (s *logStream) frame(frame v1.LogFrame) error {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(frame); err != nil {
		return err
	}
	if _, err := s.w.Write(buf.Bytes()); err != nil {
		return err
	}
	s.flush()
	return nil
}

func (s *logStream) flush() {
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

// emit streams the bytes in [start, end) as log frames and returns the offset
// after the last byte it consumed. A complete line is one frame; a line longer
// than the read buffer is several buffer-sized frames, because ReadSlice hands
// back a full buffer with ErrBufferFull instead of growing. That keeps every
// frame and every allocation bounded no matter how the log is formatted.
// includePartial also emits a trailing chunk that has no newline yet; a
// following stream leaves it for the next poll so a line is not cut in half by
// an arbitrary pause.
func (s *logStream) emit(file *os.File, start, end int64, at time.Time, includePartial bool) (int64, error) {
	if end <= start {
		return start, nil
	}

	reader := bufio.NewReaderSize(io.NewSectionReader(file, start, end-start), logReadBuffer)
	next := start
	for next < end {
		chunk, err := reader.ReadSlice('\n')

		switch {
		case len(chunk) > 0 && chunk[len(chunk)-1] == '\n':
			// A whole line, possibly the last chunk of a long one.
			if frameErr := s.frame(v1.LogDataFrame(at, chunk)); frameErr != nil {
				return next, frameErr
			}
			next += int64(len(chunk))
		case errors.Is(err, bufio.ErrBufferFull):
			// The line is longer than the buffer: frame what we have and
			// continue. The next call resumes mid-line, so the frames are
			// byte-for-byte the same stream.
			if frameErr := s.frame(v1.LogDataFrame(at, chunk)); frameErr != nil {
				return next, frameErr
			}
			next += int64(len(chunk))
		default:
			// End of the window (or a read failure) inside a line. Only a
			// finite stream emits it: a follower keeps the partial line for the
			// poll that completes it.
			if includePartial && len(chunk) > 0 {
				if frameErr := s.frame(v1.LogDataFrame(at, chunk)); frameErr != nil {
					return end, frameErr
				}
				next += int64(len(chunk))
			}
			if err != nil && !errors.Is(err, io.EOF) {
				return next, err
			}
			return next, nil
		}
	}

	return next, nil
}

// follow polls the log for appended bytes until the request is cancelled, the
// task stops running or the task's log disappears. It never writes an eof: a
// following stream ends only when there is nothing left to follow.
func (s *logStream) follow(ctx context.Context, task Task, file *os.File, pos int64, since time.Time) {
	ticker := time.NewTicker(logPollInterval)
	defer ticker.Stop()

	for {
		// The console log outlives the VM, so a finished task would leave this
		// loop polling a static file forever. The API socket is the liveness
		// signal, and checking it before the wait also ends a follow of a task
		// that was already gone when the request arrived.
		if !task.stillRunning() {
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		size, ok := logSize(task.LogPath)
		if !ok {
			return
		}
		if size < pos {
			// The log was truncated or replaced. Re-read it from the start.
			pos = 0
		}
		if size <= pos {
			continue
		}

		at := time.Now().UTC()
		if !since.IsZero() && at.Before(since) {
			continue
		}

		next, err := s.emit(file, pos, size, at, false)
		if err != nil {
			return
		}
		pos = next
	}
}

// logSize reports the current size of a log by path, not of an open descriptor:
// an open descriptor keeps a removed file alive, so it would never reveal that
// the task's log is gone. It returns false once the file is, which ends a
// following stream.
func logSize(path string) (int64, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return 0, false
	}
	return info.Size(), true
}
