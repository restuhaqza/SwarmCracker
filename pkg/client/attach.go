// This file owns the remote attach client: POST /v1/tasks/{ref}/attach over the
// public HTTPS API, one full-duplex request carrying the console both ways.
//
// The local path (cmd/swarmcracker's `vm attach` dialing the console Unix
// socket) predates it; this is what makes an interactive console reachable from
// another machine with only an API token and the cluster CA.

package client

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"

	"github.com/restuhaqza/swarmcracker/pkg/console"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// octetStreamMediaType is the media type of both attach bodies: raw bytes, not
// the v1 JSON envelope and not NDJSON.
const octetStreamMediaType = "application/octet-stream"

// consoleChunkBytes is the read size of the console copy loop. It matches the
// server's relay buffer, so one chunk in is one chunk out.
const consoleChunkBytes = 32 << 10

// AttachTask streams stdin to the task's console and the console back to
// stdout, until the console closes or the user detaches with Ctrl-P Ctrl-Q.
// It is remote-only; without --host it returns CodeUsage.
//
// A clean return means the session ended normally: the console reached EOF, or
// the user detached. A detach aborts the HTTP request — it closes the request
// body and cancels the context — which never stops the VM; the guest keeps
// running for the next attach.
func (c *Client) AttachTask(ctx context.Context, ref string, stdin io.Reader, stdout io.Writer) error {
	if c.remote == nil {
		return output.NewError(output.CodeUsage,
			"attach over the API requires --host; without it the local console socket is used")
	}
	return c.remote.attachTask(ctx, ref, stdin, stdout)
}

// attachTask posts the full-duplex request and copies the console out.
func (r *remoteClient) attachTask(ctx context.Context, ref string, stdin io.Reader, stdout io.Writer) error {
	if err := validateSegments([]string{ref}); err != nil {
		return err
	}
	if stdout == nil {
		stdout = io.Discard
	}

	// One cancellation covers the whole session: the caller's context ending
	// and a local detach both close the connection, which unblocks the
	// response reader and stops the transport pulling on stdin.
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var body io.Reader = http.NoBody
	var detach *console.DetachReader
	if stdin != nil {
		detach = console.NewDetachReader(stdin)
		body = detach

		// Watch the filter rather than waiting for the transport to notice the
		// body error: a detach must abort the request immediately, even while
		// the response copy is blocked in a read.
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			select {
			case <-detach.Done():
				cancel()
			case <-stop:
			}
		}()
	}

	u := r.baseURL.JoinPath("v1", "tasks", url.PathEscape(ref), "attach")
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, u.String(), body)
	if err != nil {
		return output.NewErrorf(output.CodeInternal, "build attach request: %v", err)
	}
	req.Header.Set("Accept", octetStreamMediaType)
	req.Header.Set("Content-Type", octetStreamMediaType)
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}

	// The session lasts as long as the console does, so the response has no
	// whole-request timeout: only the context bounds it. r.attach is the
	// no-timeout, HTTP/1.1-only client.
	resp, err := r.attach.Do(req)
	if err != nil {
		if detached(detach) {
			return nil
		}
		return transportError("POST /v1/tasks/"+ref+"/attach", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		errorBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		return decodeErrorDocument(http.MethodPost, "/v1/tasks/"+ref+"/attach",
			resp.StatusCode, resp.Status, errorBody)
	}

	// A 2xx that is not raw console bytes means the session never started: a
	// proxy, a captive portal or a wrong route answered instead of the manager.
	// Copying that body to stdout would print an HTML page as if it were
	// console output, so it is a protocol error and nothing is written.
	if err := checkConsoleContentType(resp.Header.Get("Content-Type")); err != nil {
		return err
	}

	err = copyConsole(stdout, resp.Body)
	if err != nil && detached(detach) {
		// The detach cancelled the request mid-read; that is a normal end.
		return nil
	}
	return err
}

// checkConsoleContentType accepts only the attach media type. An absent header
// is tolerated (a minimal server may omit it); anything else is a protocol
// violation, which is a server-side bug, not a transport failure.
func checkConsoleContentType(raw string) error {
	if raw == "" {
		return nil
	}
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return output.NewErrorf(output.CodeInternal,
			"attach: malformed server Content-Type %q: %v", raw, err)
	}
	if mediaType != octetStreamMediaType {
		return output.NewErrorf(output.CodeInternal,
			"attach: server answered Content-Type %q, want %s", mediaType, octetStreamMediaType)
	}
	return nil
}

// detached reports whether the filter saw the detach sequence. A nil filter
// means the caller supplied no stdin, which cannot detach.
func detached(filter *console.DetachReader) bool {
	return filter != nil && filter.Detached()
}

// copyConsole copies the console response to the caller's sink and flushes
// every chunk, so a prompt reaches the terminal before the next chunk arrives.
// A writer with no flush step (os.Stdout on a terminal) is already unbuffered.
func copyConsole(out io.Writer, body io.Reader) error {
	buf := make([]byte, consoleChunkBytes)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				return output.NewErrorf(output.CodeInternal, "write console output: %v", werr)
			}
			if flusher, ok := out.(interface{ Flush() error }); ok {
				if ferr := flusher.Flush(); ferr != nil {
					return output.NewErrorf(output.CodeInternal, "flush console output: %v", ferr)
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return transportError("read attach stream", err)
		}
	}
}
