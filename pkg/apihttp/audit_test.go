package apihttp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// auditRecord builds one valid record at a given time.
func auditRecord(at, requestID, action string) v1.AuditRecord {
	return v1.AuditRecord{
		At:        at,
		RequestID: requestID,
		Actor:     v1.AuditActor{Type: "token", ID: "tok_01234567", Name: "ci"},
		Action:    action,
		Resource:  v1.AuditResource{Kind: "Service", ID: "svc-1", Name: "web"},
		Result:    "ok",
		SourceIP:  "10.0.0.5",
	}
}

func TestOpenAuditStoreMissingFileIsEmptyAndCreatesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "api", "audit.jsonl")

	store, err := OpenAuditStore(path, 0)
	require.NoError(t, err)
	require.NotNil(t, store)

	records, next, err := store.List(10, "")
	require.NoError(t, err)
	assert.Empty(t, records)
	assert.Nil(t, next)

	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "OpenAuditStore must not create the file")
}

func TestOpenAuditStoreValidatesPath(t *testing.T) {
	for _, path := range []string{"", "   "} {
		store, err := OpenAuditStore(path, 0)
		require.Error(t, err)
		assert.Nil(t, store)
		assert.Equal(t, output.CodeInvalid, requireErrorCode(t, err))
	}
}

func TestAuditAppendAndList(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state", "api")
	path := filepath.Join(dir, "audit.jsonl")

	store, err := OpenAuditStore(path, 0)
	require.NoError(t, err)

	want := auditRecord("2026-10-23T10:00:00Z", "req_000000000001", "service.create")
	require.NoError(t, store.Append(want))

	records, next, err := store.List(10, "")
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, want, records[0])
	assert.Nil(t, next)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	dirInfo, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())

	// One record per line, so a partial write can never corrupt a neighbour.
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(data), "\n"))
}

func TestAuditAppendValidatesRecord(t *testing.T) {
	store, err := OpenAuditStore(filepath.Join(t.TempDir(), "audit.jsonl"), 0)
	require.NoError(t, err)

	cases := map[string]v1.AuditRecord{
		"missing at":         {RequestID: "req_1", Action: "service.create"},
		"missing request id": {At: "2026-10-23T10:00:00Z", Action: "service.create"},
		"missing action":     {At: "2026-10-23T10:00:00Z", RequestID: "req_1"},
	}
	for name, rec := range cases {
		t.Run(name, func(t *testing.T) {
			err := store.Append(rec)
			require.Error(t, err)
			assert.Equal(t, output.CodeInvalid, requireErrorCode(t, err))
		})
	}
}

// TestAuditListOrdersAndPaginates pins the ordering contract: at ascending, then
// requestId ascending, with the same opaque cursor the resource lists use.
func TestAuditListOrdersAndPaginates(t *testing.T) {
	store, err := OpenAuditStore(filepath.Join(t.TempDir(), "audit.jsonl"), 0)
	require.NoError(t, err)

	// Appended out of order on purpose: a clock that steps backwards must not
	// reorder the log.
	records := []v1.AuditRecord{
		auditRecord("2026-10-23T10:00:02Z", "req_b", "service.delete"),
		auditRecord("2026-10-23T10:00:00Z", "req_a", "service.create"),
		auditRecord("2026-10-23T10:00:01Z", "req_c", "service.update"),
		auditRecord("2026-10-23T10:00:00Z", "req_0", "service.create"),
	}
	for _, rec := range records {
		require.NoError(t, store.Append(rec))
	}

	page, next, err := store.List(3, "")
	require.NoError(t, err)
	require.Len(t, page, 3)
	assert.Equal(t, []string{"req_0", "req_a", "req_c"}, requestIDs(page))
	require.NotNil(t, next)

	second, next2, err := store.List(3, *next)
	require.NoError(t, err)
	require.Len(t, second, 1)
	assert.Equal(t, []string{"req_b"}, requestIDs(second))
	assert.Nil(t, next2)

	badCursor, _, err := store.List(3, "not-a-cursor")
	require.Error(t, err)
	assert.Equal(t, output.CodeInvalid, requireErrorCode(t, err))
	assert.Nil(t, badCursor)
}

func requestIDs(records []v1.AuditRecord) []string {
	out := make([]string, 0, len(records))
	for _, rec := range records {
		out = append(out, rec.RequestID)
	}
	return out
}

func TestAuditListRejectsNonPositiveLimit(t *testing.T) {
	store, err := OpenAuditStore(filepath.Join(t.TempDir(), "audit.jsonl"), 0)
	require.NoError(t, err)

	for _, limit := range []int{0, -1} {
		_, _, err := store.List(limit, "")
		require.Error(t, err)
		assert.Equal(t, output.CodeInvalid, requireErrorCode(t, err))
	}
}

// TestAuditStorePrunesOnOpen covers the retention window: old records are gone
// after a reload, fresh ones survive, and the rewrite is a valid JSONL file.
func TestAuditStorePrunesOnOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")

	now := time.Now().UTC()
	old := now.Add(-72 * time.Hour).Format(time.RFC3339Nano)
	fresh := now.Add(-time.Hour).Format(time.RFC3339Nano)

	store, err := OpenAuditStore(path, 48*time.Hour)
	require.NoError(t, err)
	require.NoError(t, store.Append(auditRecord(old, "req_old", "service.create")))
	require.NoError(t, store.Append(auditRecord(fresh, "req_fresh", "service.update")))

	reopened, err := OpenAuditStore(path, 48*time.Hour)
	require.NoError(t, err)

	records, _, err := reopened.List(10, "")
	require.NoError(t, err)
	assert.Equal(t, []string{"req_fresh"}, requestIDs(records))

	// The rewrite dropped the stale line from disk too, not just from memory.
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "req_old")
	assert.Contains(t, string(data), "req_fresh")
	assert.Equal(t, 1, strings.Count(string(data), "\n"))

	// The file still loads.
	_, err = OpenAuditStore(path, 48*time.Hour)
	require.NoError(t, err)
}

func TestAuditStorePrunesOnlyWhenSomethingExpired(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")

	store, err := OpenAuditStore(path, time.Hour)
	require.NoError(t, err)
	require.NoError(t, store.Append(auditRecord(time.Now().UTC().Format(time.RFC3339Nano), "req_1", "service.create")))

	before, err := os.Stat(path)
	require.NoError(t, err)

	_, err = OpenAuditStore(path, time.Hour)
	require.NoError(t, err)

	after, err := os.Stat(path)
	require.NoError(t, err)
	assert.True(t, before.ModTime().Equal(after.ModTime()),
		"opening a log with nothing to prune must not rewrite it")
}

func TestAuditStoreUsesDefaultRetention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")

	// A record just outside the default window is pruned by a non-positive
	// retention, which means "use the 30-day default".
	old := time.Now().UTC().Add(-DefaultAuditRetention - time.Hour).Format(time.RFC3339Nano)
	store, err := OpenAuditStore(path, 0)
	require.NoError(t, err)
	require.NoError(t, store.Append(auditRecord(old, "req_old", "service.create")))

	reopened, err := OpenAuditStore(path, 0)
	require.NoError(t, err)
	records, _, err := reopened.List(10, "")
	require.NoError(t, err)
	assert.Empty(t, records)
}

// TestAuditStoreSkipsMalformedLines covers a crash-truncated tail and a garbage
// line: both are skipped, and the readable records survive.
func TestAuditStoreSkipsMalformedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")

	good := auditRecord("2026-10-23T10:00:00Z", "req_good", "service.create")
	line, err := json.Marshal(good)
	require.NoError(t, err)

	content := strings.Join([]string{
		string(line),
		"",
		"{not json",
		`{"at":"2026-10-23T10:00:01Z"}`, // missing requestId and action
		`{"at":"","requestId":"req_empty","action":"service.create"}`,
		string(line[:len(line)/2]), // truncated final line
	}, "\n") + "\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	store, err := OpenAuditStore(path, 0)
	require.NoError(t, err)

	records, _, err := store.List(10, "")
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "req_good", records[0].RequestID)

	// The store still accepts new records after skipping the bad ones.
	require.NoError(t, store.Append(auditRecord("2026-10-23T10:00:02Z", "req_new", "service.update")))
	records, _, err = store.List(10, "")
	require.NoError(t, err)
	assert.Len(t, records, 2)
}

// TestRecordAudit builds a record from the request context.
func TestRecordAudit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	store, err := OpenAuditStore(path, 0)
	require.NoError(t, err)

	srv, err := New(Config{Client: &fakeClient{}, Audit: store})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/v1/services", nil)
	req.RemoteAddr = "10.0.0.5:55555"
	ctx := context.WithValue(req.Context(), requestIDKey{}, "req_abc")
	ctx = withIdentity(ctx, Identity{
		Type: "token", TokenID: "tok_01234567", Name: "ci", Role: RoleOperator,
	})
	req = req.WithContext(ctx)

	require.NoError(t, srv.recordAudit(req, "service.create",
		v1.AuditResource{Kind: "Service", ID: "svc-1", Name: "web"}, "ok"))

	records, _, err := store.List(10, "")
	require.NoError(t, err)
	require.Len(t, records, 1)

	rec := records[0]
	assert.Equal(t, "req_abc", rec.RequestID)
	assert.Equal(t, v1.AuditActor{Type: "token", ID: "tok_01234567", Name: "ci"}, rec.Actor)
	assert.Equal(t, "service.create", rec.Action)
	assert.Equal(t, v1.AuditResource{Kind: "Service", ID: "svc-1", Name: "web"}, rec.Resource)
	assert.Equal(t, "ok", rec.Result)
	assert.Equal(t, "10.0.0.5", rec.SourceIP)

	_, parseErr := time.Parse(time.RFC3339Nano, rec.At)
	require.NoError(t, parseErr)
	assert.True(t, strings.HasSuffix(rec.At, "Z"), "At must be UTC: %q", rec.At)
}

func TestRecordAuditLocalIdentityAndUnixSourceIP(t *testing.T) {
	store, err := OpenAuditStore(filepath.Join(t.TempDir(), "audit.jsonl"), 0)
	require.NoError(t, err)

	srv, err := New(Config{Client: &fakeClient{}, Audit: store})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/v1/services", nil)
	// A Unix listener reports the socket path as RemoteAddr.
	req.RemoteAddr = "/var/lib/swarmkit/api.sock"
	// The middleware always supplies the request ID; a handler runs under it.
	ctx := context.WithValue(req.Context(), requestIDKey{}, "req_unix")
	ctx = withIdentity(ctx, LocalIdentity())
	req = req.WithContext(ctx)

	require.NoError(t, srv.recordAudit(req, "service.delete",
		v1.AuditResource{Kind: "Service", ID: "svc-1"}, "error"))

	records, _, err := store.List(10, "")
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, v1.AuditActor{Type: "local"}, records[0].Actor,
		"a local actor has no token ID or name")
	assert.Empty(t, records[0].SourceIP, "a Unix-socket request has no source IP")
	assert.Equal(t, "error", records[0].Result)
}

// TestRecordAuditIgnoresForwardedFor pins that the recorded source is the TCP
// peer: a client header must not be able to write the audit log.
func TestRecordAuditIgnoresForwardedFor(t *testing.T) {
	store, err := OpenAuditStore(filepath.Join(t.TempDir(), "audit.jsonl"), 0)
	require.NoError(t, err)

	srv, err := New(Config{Client: &fakeClient{}, Audit: store})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/v1/services", nil)
	req.RemoteAddr = "10.0.0.5:55555"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	ctx := context.WithValue(req.Context(), requestIDKey{}, "req_forwarded")
	req = req.WithContext(withIdentity(ctx, LocalIdentity()))

	require.NoError(t, srv.recordAudit(req, "service.create", v1.AuditResource{Kind: "Service"}, "ok"))

	records, _, err := store.List(10, "")
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "10.0.0.5", records[0].SourceIP)
}

func TestRecordAuditWithoutStoreIsNoOp(t *testing.T) {
	srv, err := New(Config{Client: &fakeClient{}})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/v1/services", nil)
	assert.NoError(t, srv.recordAudit(req, "service.create", v1.AuditResource{Kind: "Service"}, "ok"))
}

// writeAuditLines writes one JSONL file from already-encoded lines.
func writeAuditLines(t *testing.T, path string, lines ...[]byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	for _, line := range lines {
		buf.Write(line)
		buf.WriteByte('\n')
	}
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o600))
	return buf.Bytes()
}

func mustMarshalRecord(t *testing.T, rec v1.AuditRecord) []byte {
	t.Helper()
	line, err := json.Marshal(rec)
	require.NoError(t, err)
	return line
}

// TestAuditStoreReadsPastAnOversizedLine is the regression for a poisoned log: an
// over-long line used to stop the scan, hiding every record behind it, and the
// next retention rewrite then deleted them from disk.
func TestAuditStoreReadsPastAnOversizedLine(t *testing.T) {
	now := time.Now().UTC()

	expired := auditRecord(now.Add(-2*time.Hour).Format(time.RFC3339Nano), "req_expired", "service.create")
	middle := auditRecord(now.Add(-30*time.Minute).Format(time.RFC3339Nano), "req_middle", "service.update")
	tail := auditRecord(now.Add(-10*time.Minute).Format(time.RFC3339Nano), "req_tail", "service.delete")

	// A real record whose resource name is far past the cap, so the line the
	// loader must skip is one the encoder could produce.
	huge := auditRecord(now.Add(-20*time.Minute).Format(time.RFC3339Nano), "req_huge", "service.update")
	huge.Resource.Name = strings.Repeat("n", 2*auditMaxLineBytes)

	t.Run("no pruning still reads both neighbours", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "audit.jsonl")
		before := writeAuditLines(t, path,
			mustMarshalRecord(t, middle), mustMarshalRecord(t, huge), mustMarshalRecord(t, tail))

		store, err := OpenAuditStore(path, 0) // 30 days: nothing expires
		require.NoError(t, err)

		records, _, err := store.List(10, "")
		require.NoError(t, err)
		assert.Equal(t, []string{"req_middle", "req_tail"}, requestIDs(records))

		after, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, before, after, "an open with nothing to prune must not rewrite the file")
	})

	t.Run("pruning keeps the tail on disk", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "audit.jsonl")
		writeAuditLines(t, path,
			mustMarshalRecord(t, expired),
			mustMarshalRecord(t, middle),
			mustMarshalRecord(t, huge),
			mustMarshalRecord(t, tail))

		store, err := OpenAuditStore(path, time.Hour)
		require.NoError(t, err)

		records, _, err := store.List(10, "")
		require.NoError(t, err)
		assert.Equal(t, []string{"req_middle", "req_tail"}, requestIDs(records),
			"records behind the oversized line must still be read")

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Contains(t, string(data), "req_middle")
		assert.Contains(t, string(data), "req_tail", "the tail must survive the retention rewrite")
		assert.NotContains(t, string(data), "req_expired")
		assert.NotContains(t, string(data), "req_huge", "the unreadable line is dropped by the rewrite")
		assert.Equal(t, 2, strings.Count(string(data), "\n"))

		reopened, err := OpenAuditStore(path, time.Hour)
		require.NoError(t, err)
		again, _, err := reopened.List(10, "")
		require.NoError(t, err)
		assert.Equal(t, []string{"req_middle", "req_tail"}, requestIDs(again))
	})
}

// TestAuditAppendRejectsOversizedRecord keeps the writer inside the cap the
// loader enforces: the store must never author a line it cannot read back.
func TestAuditAppendRejectsOversizedRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	store, err := OpenAuditStore(path, 0)
	require.NoError(t, err)

	rec := auditRecord(time.Now().UTC().Format(time.RFC3339Nano), "req_oversized", "service.create")
	rec.Resource.Name = strings.Repeat("n", auditMaxLineBytes)

	err = store.Append(rec)
	require.Error(t, err)
	assert.Equal(t, output.CodeInvalid, requireErrorCode(t, err))

	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	assert.EqualValues(t, auditMaxLineBytes, typed.Details["limit"])
	assert.Greater(t, typed.Details["bytes"], auditMaxLineBytes)

	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "a rejected record must not create the file")

	records, _, err := store.List(10, "")
	require.NoError(t, err)
	assert.Empty(t, records)

	// A record just inside the cap is accepted and reads back.
	small := auditRecord(time.Now().UTC().Format(time.RFC3339Nano), "req_small", "service.create")
	small.Resource.Name = strings.Repeat("n", 4096)
	require.NoError(t, store.Append(small))

	reopened, err := OpenAuditStore(path, 0)
	require.NoError(t, err)
	records, _, err = reopened.List(10, "")
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "req_small", records[0].RequestID)
}

// TestAuditStoreSkipsRecordWithoutRequestID covers a hand-edited line that would
// otherwise break pagination: the cursor needs a non-empty id.
func TestAuditStoreSkipsRecordWithoutRequestID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	writeAuditLines(t, path,
		[]byte(`{"at":"2026-10-23T10:00:00Z","action":"service.create","actor":{"type":"local"},"resource":{"kind":"Service"},"result":"ok"}`),
		mustMarshalRecord(t, auditRecord("2026-10-23T10:00:01Z", "req_good", "service.update")),
	)

	store, err := OpenAuditStore(path, 0)
	require.NoError(t, err)

	records, next, err := store.List(1, "")
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "req_good", records[0].RequestID)
	assert.Nil(t, next, "the single readable record is the whole page")
}

// TestRecordAuditMissingIdentityUsesUnknownActor keeps recordAudit from writing
// an empty actor type, which the wire schema does not allow.
func TestRecordAuditMissingIdentityUsesUnknownActor(t *testing.T) {
	store, err := OpenAuditStore(filepath.Join(t.TempDir(), "audit.jsonl"), 0)
	require.NoError(t, err)

	srv, err := New(Config{Client: &fakeClient{}, Audit: store})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/v1/services", nil)
	// Only the request ID: no Identity, as if the helper were called outside wrap.
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey{}, "req_noidentity"))

	require.NoError(t, srv.recordAudit(req, "service.create",
		v1.AuditResource{Kind: "Service"}, "ok"))

	records, _, err := store.List(10, "")
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, v1.AuditActor{Type: "unknown"}, records[0].Actor)
}
