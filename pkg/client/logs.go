// This file owns the remote log client: GET /v1/tasks/{ref}/logs and the
// service-wide fan-out built on it.
//
// The node side reads a task's console file, so a log is available cluster-wide
// without a collector: the manager proxies to the node that owns the task, and
// this client streams the NDJSON frames back as text.
//
// The local files stay the source of truth for the node that produced them, so a
// single-node install keeps working with no API, no aggregation daemon and no
// extra configuration.
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// MaxRemoteLogTail is the largest backlog a node serves for one request. A call
// that asks for more is clamped rather than refused: "all of it" is a request
// for the cap, not a bad request. The cap is the node's, not this package's, so
// the two agree on what `--tail -1` means.
const MaxRemoteLogTail = 10000

// remoteLogLineBytes bounds one NDJSON frame. A console frame can carry a large
// chunk, so the default 64 KiB scanner limit is too small.
const remoteLogLineBytes = 1 << 20

// LogsOptions controls one remote log read.
type LogsOptions struct {
	// Tail is how many trailing lines to fetch. -1 means "as many as the node
	// serves" (MaxRemoteLogTail); 0 means "no backlog", which is what a caller
	// that only wants --follow asks for.
	Tail int
	// Since keeps lines from after this instant. Zero means no time filter.
	Since time.Time
	// Follow keeps the stream open and relays lines as the guest writes them.
	Follow bool
}

// LogsTask streams one task's console log through the public API.
//
// It requires --host: the local path reads the file directly and never needs the
// API, while a task on another node is only reachable through the manager's
// broker. An unknown task is a typed 404; a task with no log is the same.
func (c *Client) LogsTask(ctx context.Context, ref string, opts LogsOptions, out io.Writer) error {
	if c.remote == nil {
		return output.NewError(output.CodeUsage,
			"logs over the API require --host; without it the local console log is read")
	}
	return c.remote.logsTask(ctx, ref, opts, out, "")
}

// ServiceLogs streams the merged console logs of every task of a service.
//
// Lines are prefixed with a short task ID, because merged output without
// attribution is not usable when a service has more than one replica. Streams
// are interleaved as they arrive rather than sorted by time: the console log
// carries no per-line timestamp, so a merge can only be arrival-ordered.
//
// A task that never started has no log; that 404 is skipped instead of failing
// the whole command, so asking for a scaling service's logs still works while
// its extra replicas are being created.
func (c *Client) ServiceLogs(ctx context.Context, ref string, opts LogsOptions, out io.Writer) error {
	if c.remote == nil {
		return output.NewError(output.CodeUsage,
			"service logs require --host: only the manager can reach every node's console files")
	}

	tasks, err := c.remote.listServiceTasks(ctx, ref)
	if err != nil {
		return err
	}
	if len(tasks) == 0 {
		return output.NewErrorf(output.CodeNotFound, "service %q has no tasks", ref)
	}
	return c.remote.mergeTaskLogs(ctx, tasks, opts, out)
}

// mergeTaskLogs reads every task's log concurrently and writes prefixed lines to
// out as they arrive.
func (r *remoteClient) mergeTaskLogs(ctx context.Context, tasks []v1.Task, opts LogsOptions, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sink := &prefixWriter{w: out}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		served   int
	)
	for _, task := range tasks {
		prefix := task.ID
		if len(prefix) > 12 {
			prefix = prefix[:12]
		}
		wg.Add(1)
		go func(taskID, prefix string) {
			defer wg.Done()

			err := r.logsTask(ctx, taskID, opts, sink, prefix)
			if err == nil {
				mu.Lock()
				served++
				mu.Unlock()
				return
			}
			// A task with no log yet is normal during a rollout: skip it and let
			// the others answer.
			var typed *output.Error
			if errors.As(err, &typed) && typed.Code == output.CodeNotFound {
				return
			}
			mu.Lock()
			if firstErr == nil {
				firstErr = fmt.Errorf("task %s: %w", taskID, err)
				// One broken stream must not leave the others holding the
				// terminal open.
				cancel()
			}
			mu.Unlock()
		}(task.ID, prefix)
	}
	wg.Wait()

	if firstErr != nil {
		return firstErr
	}
	if served == 0 {
		return output.NewErrorf(output.CodeNotFound, "no task of the service has a log yet")
	}
	return nil
}

// logsTask streams one task's log, prefixing every line when prefix is set.
func (r *remoteClient) logsTask(ctx context.Context, ref string, opts LogsOptions, out io.Writer, prefix string) error {
	if err := validateSegments([]string{ref}); err != nil {
		return err
	}

	query := url.Values{}
	tail := opts.Tail
	switch {
	case tail < 0:
		tail = MaxRemoteLogTail
	case tail > MaxRemoteLogTail:
		tail = MaxRemoteLogTail
	}
	query.Set("tail", fmt.Sprintf("%d", tail))
	if !opts.Since.IsZero() {
		query.Set("since", opts.Since.UTC().Format(time.RFC3339Nano))
	}
	if opts.Follow {
		query.Set("follow", "true")
	}

	u := r.baseURL.JoinPath("v1", "tasks", url.PathEscape(ref), "logs")
	u.RawQuery = query.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return output.NewErrorf(output.CodeInternal, "build logs request: %v", err)
	}
	httpReq.Header.Set("Accept", v1.NDJSONMediaType)
	if r.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+r.token)
	}

	// The stream client has no whole-response timeout: a following log outlives
	// the generic request timeout, and its own context bounds it.
	resp, err := r.stream.Do(httpReq)
	if err != nil {
		return transportError("GET /v1/tasks/"+ref+"/logs", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		errorBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		return decodeErrorDocument(http.MethodGet, "/v1/tasks/"+ref+"/logs", resp.StatusCode, resp.Status, errorBody)
	}

	// One Write per line: when streams are merged, out is a prefixWriter, so a
	// single Write is what keeps two tasks' output from interleaving mid-line.
	emit := func(line []byte) {
		buf := make([]byte, 0, len(prefix)+3+len(line)+1)
		if prefix != "" {
			buf = append(buf, prefix...)
			buf = append(buf, ' ', '|', ' ')
		}
		buf = append(buf, line...)
		buf = append(buf, '\n')
		_, _ = out.Write(buf)
	}
	if err := pumpLogStream(ctx, resp.Body, emit); err != nil {
		return err
	}
	// A follow stream ends when the caller cancels; that is a clean stop, not a
	// truncated stream.
	if opts.Follow && ctx.Err() != nil {
		return nil
	}
	return nil
}

// pumpLogStream decodes NDJSON log frames and hands each complete line to emit.
// A frame can split a line (the node reads the file in chunks), so the remainder
// is carried across frames and flushed at end of stream.
func pumpLogStream(ctx context.Context, body io.Reader, emit func([]byte)) error {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), remoteLogLineBytes)

	var partial []byte
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var frame v1.LogFrame
		if err := json.Unmarshal(line, &frame); err != nil {
			return output.NewErrorf(output.CodeInternal, "decode log frame: %v", err)
		}
		switch frame.Type {
		case v1.LogFrameTypeLog:
			partial = append(partial, frame.Data...)
			for {
				idx := bytes.IndexByte(partial, '\n')
				if idx < 0 {
					break
				}
				emit(partial[:idx])
				partial = partial[idx+1:]
			}
		case v1.LogFrameTypeEOF:
			if len(partial) > 0 {
				emit(partial)
			}
			return nil
		default:
			return output.NewErrorf(output.CodeInternal, "unexpected log frame type %q", frame.Type)
		}
	}
	if err := sc.Err(); err != nil {
		if ctx.Err() != nil {
			// A cancelled follow closes the body; that is the caller stopping,
			// not a broken stream.
			return nil
		}
		return output.NewErrorf(output.CodeUnavailable, "read log stream: %v", err)
	}
	if len(partial) > 0 {
		emit(partial)
	}
	return nil
}

// prefixWriter serialises prefixed lines from concurrent log streams, so two
// streams can never interleave inside one line.
type prefixWriter struct {
	w  io.Writer
	mu sync.Mutex
}

// Write serialises other writers' output against the merged stream.
func (p *prefixWriter) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.w.Write(b)
}
