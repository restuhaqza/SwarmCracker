package broker

// This file owns the node-side regular-file handler: GET and PUT
// /v1/tasks/{taskID}/files on the internal broker.
//
// The bytes travel through the guest agent, so the manager never mounts the
// guest filesystem and never needs the rootfs path. Only regular files are
// served: there is no directory, tar or archive mode, and a directory is a typed
// 400 rather than an empty stream.
//
// The response of a GET is application/octet-stream and the body of a PUT is raw
// bytes: the two documented exceptions to the v1 envelope. Uploads are bounded
// both by the declared Content-Length and, for a chunked body, by
// http.MaxBytesReader while the guest reads.
//
// Audit is the public layer's job; nothing here records who read what.
//
// Files need a live guest agent. A task that has exited keeps its console log
// (readable through the logs operation) but has no guest, so files answers a
// typed 404 not_found instead of dialing a dead vsock path.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

const (
	// octetStream is the media type of a raw file transfer.
	octetStream = "application/octet-stream"
	// maxFileTransferBytes bounds one upload. A file larger than this is
	// refused rather than streamed into a guest.
	maxFileTransferBytes = 1 << 30
	// defaultFileMode is the mode a PUT uses when the caller names none.
	defaultFileMode = 0o644
	// maxFileMode is the widest mode a caller may request.
	maxFileMode = 0o777
)

// GuestFileTransfer is the file-transfer half of the guest agent client. The
// broker dials through ExecDialer, whose *agent.Client implements both halves; a
// dialer that only speaks exec cannot serve files, and says so.
type GuestFileTransfer interface {
	CopyFrom(ctx context.Context, guestPath string, dst io.Writer) error
	CopyTo(ctx context.Context, guestPath string, src io.Reader, mode uint32) error
}

// NewFilesHandler builds the regular-file operation. A nil dialer selects
// DialAgent, so the daemon wires the handler without repeating the default.
func NewFilesHandler(dial ExecDialer) Handler {
	return newFilesHandler(dial, maxFileTransferBytes)
}

// newFilesHandler is NewFilesHandler with an explicit upload cap. It is
// unexported so a test can exercise the cap without allocating a gigabyte.
func newFilesHandler(dial ExecDialer, limit int64) Handler {
	if dial == nil {
		dial = DialAgent
	}

	return func(ctx context.Context, task Task, w http.ResponseWriter, r *http.Request) {
		if limit <= 0 {
			limit = maxFileTransferBytes
		}
		serveFiles(ctx, dial, limit, task, w, r)
	}
}

// serveFiles dispatches one file request.
func serveFiles(ctx context.Context, dial ExecDialer, limit int64, task Task, w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		serveFileGet(ctx, dial, task, w, r)
	case http.MethodPut:
		serveFilePut(ctx, dial, limit, task, w, r)
	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPut)
		writeError(w, http.StatusMethodNotAllowed, output.CodeInvalid,
			"method %s is not allowed on a file", r.Method)
	}
}

// serveFileGet streams one guest file to the caller.
func serveFileGet(ctx context.Context, dial ExecDialer, task Task, w http.ResponseWriter, r *http.Request) {
	guestPath, pathErr := cleanGuestPath(r.URL.Query().Get("path"))
	if pathErr != nil {
		writeTypedError(w, http.StatusBadRequest, pathErr)
		return
	}

	guest, closeGuest, status, err := dialFileGuest(ctx, dial, task)
	if err != nil {
		writeTypedError(w, status, err)
		return
	}
	defer closeGuest()

	// The guest streams without announcing a size, so the header is delayed
	// until the first byte and the response is chunked. That way a guest failure
	// before the first byte is still a typed error document; a failure after it
	// truncates the stream, which the caller detects as a short read.
	body := &fileStreamWriter{w: w, flusher: flusherFor(w)}
	if err := guest.CopyFrom(ctx, guestPath, body); err != nil {
		// A failure before the first byte is still a typed document. A failure
		// after it has already started the response: the truncated stream is the
		// only signal left, so nothing else is written.
		if !body.started && ctx.Err() == nil {
			status, typed := mapGuestFileError(task, err)
			writeTypedError(w, status, typed)
		}
		return
	}
	body.finish()
}

// serveFilePut stores one raw request body as a guest file.
func serveFilePut(ctx context.Context, dial ExecDialer, limit int64, task Task, w http.ResponseWriter, r *http.Request) {
	guestPath, mode, status, pathErr := parseFilePut(r)
	if pathErr != nil {
		writeTypedError(w, status, pathErr)
		return
	}

	// A declared length above the cap is refused before any dial. A chunked
	// body has no declared length, so MaxBytesReader bounds it while it is read.
	if r.ContentLength > limit {
		writeTypedError(w, http.StatusRequestEntityTooLarge, payloadTooLarge(limit))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)

	guest, closeGuest, status, err := dialFileGuest(ctx, dial, task)
	if err != nil {
		writeTypedError(w, status, err)
		return
	}
	defer closeGuest()

	if err := guest.CopyTo(ctx, guestPath, r.Body, mode); err != nil {
		if ctx.Err() != nil {
			return // the caller went away; there is nobody to answer
		}
		status, typed := mapGuestFileError(task, err)
		writeTypedError(w, status, typed)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// dialFileGuest resolves the task's guest agent and its file-transfer half.
func dialFileGuest(ctx context.Context, dial ExecDialer, task Task) (GuestFileTransfer, func(), int, *output.Error) {
	// A task that has exited keeps its console log but has no guest agent, so
	// files is not_found rather than a dial of an empty or recycled vsock path.
	if !task.guestAvailable() {
		return nil, nil, http.StatusNotFound, output.NewErrorf(output.CodeNotFound,
			"task %q is not running", task.ID)
	}

	vsockPath := task.VsockPath()

	guest, err := dial(ctx, vsockPath, agent.DefaultPort)
	if err != nil {
		return nil, nil, http.StatusServiceUnavailable, output.NewErrorf(output.CodeUnavailable,
			"task %q guest agent is unavailable", task.ID)
	}

	files, ok := guest.(GuestFileTransfer)
	if !ok {
		if closer, isCloser := guest.(io.Closer); isCloser {
			_ = closer.Close()
		}
		return nil, nil, http.StatusNotImplemented, output.NewErrorf(output.CodeNotImplemented,
			"task %q guest agent does not support file transfer", task.ID)
	}

	closeGuest := func() {
		if closer, isCloser := guest.(io.Closer); isCloser {
			_ = closer.Close()
		}
	}
	return files, closeGuest, 0, nil
}

// parseFilePut validates the PUT path and mode.
func parseFilePut(r *http.Request) (string, uint32, int, *output.Error) {
	guestPath, err := cleanGuestPath(r.URL.Query().Get("path"))
	if err != nil {
		return "", 0, http.StatusBadRequest, err
	}
	mode, err := parseMode(r.URL.Query().Get("mode"))
	if err != nil {
		return "", 0, http.StatusBadRequest, err
	}
	return guestPath, mode, 0, nil
}

// cleanGuestPath enforces the path rules: absolute, cleaned, no ".." segment and
// never empty. A ".." is rejected rather than collapsed: a caller that tries to
// leave the directory it named is refused, not silently redirected.
func cleanGuestPath(raw string) (string, *output.Error) {
	invalid := output.NewError(output.CodeInvalid,
		"path must be an absolute regular-file path without a .. segment").
		WithDetails(map[string]any{"parameter": "path"})

	if raw == "" || !strings.HasPrefix(raw, "/") {
		return "", invalid
	}
	// A control character has no meaning in a path and would reach the guest as
	// a NUL-terminated or multi-line value; refuse it here, not in the guest.
	if strings.ContainsAny(raw, "\x00\n\r") {
		return "", invalid
	}
	for _, segment := range strings.Split(raw, "/") {
		if segment == ".." {
			return "", invalid
		}
	}

	cleaned := path.Clean(raw)
	if cleaned == "/" || cleaned == "." || !strings.HasPrefix(cleaned, "/") {
		return "", invalid
	}
	for _, segment := range strings.Split(cleaned, "/") {
		if segment == ".." {
			return "", invalid
		}
	}
	return cleaned, nil
}

// parseMode reads an octal mode, defaulting to 0644. The value is bounded by
// 0777: the owner is the guest process, so a wider mode would only express setuid
// and sticky bits the API does not offer.
func parseMode(raw string) (uint32, *output.Error) {
	if raw == "" {
		return defaultFileMode, nil
	}

	mode, err := strconv.ParseUint(raw, 8, 32)
	if err != nil || mode > maxFileMode {
		return 0, output.NewError(output.CodeInvalid,
			"mode must be an octal value between 0000 and 0777").
			WithDetails(map[string]any{"parameter": "mode"})
	}
	return uint32(mode), nil
}

// mapGuestFileError converts a guest failure into the public taxonomy. The guest
// error names the guest path, so only the classification crosses the wire: the
// message is rebuilt from fixed text and the task ID.
func mapGuestFileError(task Task, err error) (int, *output.Error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return http.StatusRequestEntityTooLarge, payloadTooLarge(tooLarge.Limit)
	}

	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "is a directory"):
		return http.StatusBadRequest, output.NewError(output.CodeInvalid,
			"directories are not supported; request a regular file").
			WithDetails(map[string]any{"reason": "directory_not_supported"})
	case strings.Contains(message, "no such file or directory"):
		return http.StatusNotFound, output.NewErrorf(output.CodeNotFound,
			"task %q has no such file", task.ID)
	default:
		return http.StatusInternalServerError, output.NewErrorf(output.CodeInternal,
			"task %q file transfer failed", task.ID)
	}
}

// payloadTooLarge is the typed 413 for an oversized upload.
func payloadTooLarge(limit int64) *output.Error {
	return output.NewErrorf(output.CodeInvalid,
		"the request body exceeds %d bytes", limit).
		WithDetails(map[string]any{"reason": "payload_too_large", "limit": limit})
}

// fileStreamWriter starts the octet-stream response on the first byte, so a
// guest failure before that point can still be a typed error document.
type fileStreamWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	started bool
}

func (f *fileStreamWriter) Write(p []byte) (int, error) {
	if !f.started {
		f.start()
	}
	n, err := f.w.Write(p)
	if f.flusher != nil {
		f.flusher.Flush()
	}
	return n, err
}

// finish starts an empty response: an empty file is a successful 200 with no
// body, not a 204.
func (f *fileStreamWriter) finish() {
	if !f.started {
		f.start()
	}
}

func (f *fileStreamWriter) start() {
	f.started = true
	f.w.Header().Set("Content-Type", octetStream)
	f.w.Header().Set("X-Content-Type-Options", "nosniff")
	f.w.WriteHeader(http.StatusOK)
	if f.flusher != nil {
		f.flusher.Flush()
	}
}
