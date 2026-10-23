package broker

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// fakeFileGuest is a guest agent that scripts the copy operations. It implements
// the exec half too, because the handler dials through the same ExecDialer.
type fakeFileGuest struct {
	content []byte
	fromErr error
	toErr   error

	gotFromPath string
	gotToPath   string
	gotMode     uint32
	gotPut      []byte
	closed      bool
}

func (f *fakeFileGuest) ExecStatus(context.Context, []string, agent.ExecOptions) (agent.ExecResult, error) {
	return agent.ExecResult{}, errors.New("exec is not scripted for this test")
}

func (f *fakeFileGuest) CopyFrom(_ context.Context, guestPath string, dst io.Writer) error {
	f.gotFromPath = guestPath
	if f.fromErr != nil {
		return f.fromErr
	}
	_, err := dst.Write(f.content)
	return err
}

func (f *fakeFileGuest) CopyTo(_ context.Context, guestPath string, src io.Reader, mode uint32) error {
	f.gotToPath = guestPath
	f.gotMode = mode
	data, err := io.ReadAll(src)
	f.gotPut = data
	if err != nil {
		return err
	}
	return f.toErr
}

func (f *fakeFileGuest) Close() error {
	f.closed = true
	return nil
}

// fileTask is the task every file test resolves to.
func fileTask() Task {
	return Task{ID: "task-1", SocketPath: "/var/run/firecracker/task-1.sock"}
}

// runFilesHandler runs the handler for one request and returns the response.
func runFilesHandler(t *testing.T, guest *fakeFileGuest, limit int64, method, target string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()

	dial := &fakeDialer{guest: guest}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, target, body)
	newFilesHandler(dial.dial, limit)(context.Background(), fileTask(), rec, req)
	return rec
}

func TestFilesHandlerGetStreamsTheGuestFile(t *testing.T) {
	guest := &fakeFileGuest{content: []byte("hello bytes")}

	rec := runFilesHandler(t, guest, 1<<20, http.MethodGet,
		"/v1/tasks/task-1/files?path=/etc/hostname", nil)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "application/octet-stream", rec.Header().Get("Content-Type"))
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "hello bytes", rec.Body.String())
	assert.Equal(t, "/etc/hostname", guest.gotFromPath)
	assert.Empty(t, rec.Header().Get("Content-Length"),
		"the guest streams without a size, so the response is chunked")
	assert.True(t, guest.closed, "the handler must close a dialed guest")
}

func TestFilesHandlerGetEmptyFile(t *testing.T) {
	rec := runFilesHandler(t, &fakeFileGuest{}, 1<<20, http.MethodGet,
		"/v1/tasks/task-1/files?path=/tmp/empty", nil)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/octet-stream", rec.Header().Get("Content-Type"))
	assert.Empty(t, rec.Body.String())
}

func TestFilesHandlerPutWritesTheGuestFile(t *testing.T) {
	guest := &fakeFileGuest{}

	rec := runFilesHandler(t, guest, 1<<20, http.MethodPut,
		"/v1/tasks/task-1/files?path=/etc/app.conf", strings.NewReader("raw bytes"))

	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Empty(t, rec.Body.String())
	assert.Equal(t, "/etc/app.conf", guest.gotToPath)
	assert.Equal(t, uint32(0o644), guest.gotMode, "0644 is the default mode")
	assert.Equal(t, "raw bytes", string(guest.gotPut))
}

func TestFilesHandlerPutMode(t *testing.T) {
	guest := &fakeFileGuest{}

	rec := runFilesHandler(t, guest, 1<<20, http.MethodPut,
		"/v1/tasks/task-1/files?path=/etc/app.conf&mode=0600", strings.NewReader("x"))

	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Equal(t, uint32(0o600), guest.gotMode)
}

func TestFilesHandlerPathValidation(t *testing.T) {
	cases := map[string]string{
		"missing":          "",
		"relative":         "etc/hostname",
		"dot relative":     "./etc/hostname",
		"traversal":        "/etc/../etc/passwd",
		"traversal prefix": "../etc/passwd",
		"dot":              ".",
	}

	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			for _, method := range []string{http.MethodGet, http.MethodPut} {
				guest := &fakeFileGuest{}
				target := "/v1/tasks/task-1/files"
				if path != "" {
					target += "?path=" + path
				}
				rec := runFilesHandler(t, guest, 1<<20, method, target, strings.NewReader("x"))

				require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
				doc := decodeErrorDocument(t, rec.Body)
				assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
				assert.Empty(t, guest.gotFromPath, "an invalid path must not reach the guest")
				assert.Empty(t, guest.gotToPath)
			}
		})
	}
}

// TestFilesHandlerCleanPathIsUsed pins that the request is cleaned before it
// reaches the guest, so "//etc//hostname" is not a second way to name a file.
func TestFilesHandlerCleanPathIsUsed(t *testing.T) {
	guest := &fakeFileGuest{content: []byte("x")}

	rec := runFilesHandler(t, guest, 1<<20, http.MethodGet,
		"/v1/tasks/task-1/files?path=//etc//hostname", nil)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "/etc/hostname", guest.gotFromPath)
}

func TestFilesHandlerModeValidation(t *testing.T) {
	for _, mode := range []string{"0999", "1777", "abc", "0o644", "-1"} {
		t.Run(mode, func(t *testing.T) {
			guest := &fakeFileGuest{}
			rec := runFilesHandler(t, guest, 1<<20, http.MethodPut,
				"/v1/tasks/task-1/files?path=/tmp/f&mode="+mode, strings.NewReader("x"))

			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			doc := decodeErrorDocument(t, rec.Body)
			assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
			assert.Empty(t, guest.gotToPath)
		})
	}
}

// TestFilesHandlerDirectoryIsNotSupported pins the mapped guest failure: the
// agent's "is a directory" read error becomes a typed 400 with a stable reason.
func TestFilesHandlerDirectoryIsNotSupported(t *testing.T) {
	guest := &fakeFileGuest{
		fromErr: errors.New("copy: get: read /var/log: is a directory"),
	}

	rec := runFilesHandler(t, guest, 1<<20, http.MethodGet,
		"/v1/tasks/task-1/files?path=/var/log", nil)

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	doc := decodeErrorDocument(t, rec.Body)
	assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
	assert.Equal(t, "directory_not_supported", doc.Error.Details["reason"])
}

func TestFilesHandlerMissingFileIsNotFound(t *testing.T) {
	guest := &fakeFileGuest{
		fromErr: errors.New("copy: get: open /etc/missing: no such file or directory"),
	}

	rec := runFilesHandler(t, guest, 1<<20, http.MethodGet,
		"/v1/tasks/task-1/files?path=/etc/missing", nil)

	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	doc := decodeErrorDocument(t, rec.Body)
	assert.Equal(t, string(output.CodeNotFound), doc.Error.Code)
}

// TestFilesHandlerDoesNotLeakPaths proves a guest or socket path never reaches
// the caller: the message names the task, not the node's filesystem.
func TestFilesHandlerDoesNotLeakPaths(t *testing.T) {
	guest := &fakeFileGuest{
		fromErr: errors.New("copy: get: open /var/lib/secret.txt: permission denied"),
	}

	rec := runFilesHandler(t, guest, 1<<20, http.MethodGet,
		"/v1/tasks/task-1/files?path=/var/lib/secret.txt", nil)

	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	body := rec.Body.String()
	assert.NotContains(t, body, "/var/lib/secret.txt")
	assert.NotContains(t, body, "task-1.sock")
	assert.NotContains(t, body, "permission denied")
}

// TestFilesHandlerHonoursContentLengthCap proves a declared body above the cap is
// refused before the guest is dialed.
func TestFilesHandlerHonoursContentLengthCap(t *testing.T) {
	guest := &fakeFileGuest{}

	rec := runFilesHandler(t, guest, 8, http.MethodPut,
		"/v1/tasks/task-1/files?path=/tmp/f", strings.NewReader("way more than eight bytes"))

	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
	doc := decodeErrorDocument(t, rec.Body)
	assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
	assert.Equal(t, "payload_too_large", doc.Error.Details["reason"])
	assert.Empty(t, guest.gotToPath, "an oversized body must not reach the guest")
}

// chunkedBody is a reader with no declared length, so the server cannot reject
// it from Content-Length and the handler must bound it while reading.
type chunkedBody struct {
	remaining int
}

func (b *chunkedBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		return 0, io.EOF
	}
	n := len(p)
	if n > b.remaining {
		n = b.remaining
	}
	for i := 0; i < n; i++ {
		p[i] = 'a'
	}
	b.remaining -= n
	return n, nil
}

func TestFilesHandlerBoundsChunkedBody(t *testing.T) {
	guest := &fakeFileGuest{}
	req := httptest.NewRequest(http.MethodPut, "/v1/tasks/task-1/files?path=/tmp/f",
		&chunkedBody{remaining: 64})
	require.LessOrEqual(t, req.ContentLength, int64(0), "the body must arrive without a length")

	rec := httptest.NewRecorder()
	dial := &fakeDialer{guest: guest}
	newFilesHandler(dial.dial, 8)(context.Background(), fileTask(), rec, req)

	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
	doc := decodeErrorDocument(t, rec.Body)
	assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
	assert.Equal(t, "payload_too_large", doc.Error.Details["reason"])
}

func TestFilesHandlerMissingGuestSocket(t *testing.T) {
	dial := &fakeDialer{err: errors.New("dial unix /var/run/firecracker/task-1.vsock: no such file")}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/tasks/task-1/files?path=/etc/hostname", nil)

	newFilesHandler(dial.dial, 1<<20)(context.Background(),
		Task{ID: "task-1"}, rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	doc := decodeErrorDocument(t, rec.Body)
	assert.Equal(t, string(output.CodeUnavailable), doc.Error.Code)
	assert.NotContains(t, rec.Body.String(), "vsock")
}

func TestFilesHandlerUnsupportedMethod(t *testing.T) {
	guest := &fakeFileGuest{}
	rec := runFilesHandler(t, guest, 1<<20, http.MethodDelete,
		"/v1/tasks/task-1/files?path=/etc/hostname", nil)

	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Empty(t, guest.gotFromPath)
}

// TestFilesHandlerPutForwardsTheBodyWithoutBuffering pins that the handler hands
// the request body to the guest instead of materializing it: nothing reads the
// body before CopyTo does.
func TestFilesHandlerPutForwardsTheBodyWithoutBuffering(t *testing.T) {
	body := &observingReader{data: []byte("payload")}
	guest := &fakeFileGuest{}
	req := httptest.NewRequest(http.MethodPut, "/v1/tasks/task-1/files?path=/tmp/f", body)

	rec := httptest.NewRecorder()
	dial := &fakeDialer{guest: guest}
	newFilesHandler(dial.dial, 1<<20)(context.Background(), fileTask(), rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Equal(t, "payload", string(guest.gotPut))
	assert.Equal(t, len("payload"), body.offset, "the body is consumed exactly once")
}

// observingReader is a body that reports how many bytes were consumed.
type observingReader struct {
	data   []byte
	offset int
}

func (r *observingReader) Read(p []byte) (int, error) {
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.offset:])
	r.offset += n
	return n, nil
}

// TestFilesHandlerDirectoryReasonIsStable keeps the error taxonomy stable: the
// reason is a fixed string, never the path.
func TestFilesHandlerDirectoryReasonIsStable(t *testing.T) {
	guest := &fakeFileGuest{toErr: errors.New("copy: put: open /x: is a directory")}

	rec := runFilesHandler(t, guest, 1<<20, http.MethodPut,
		"/v1/tasks/task-1/files?path=/x", strings.NewReader("x"))

	require.Equal(t, http.StatusBadRequest, rec.Code)
	doc := decodeErrorDocument(t, rec.Body)
	assert.Equal(t, "directory_not_supported", doc.Error.Details["reason"])
}
