// This file owns the manager-local audit log: an append-only JSONL file, the
// recordAudit helper future write handlers call, and the GET /v1/audit read.
//
// Audit records are manager-local in v1, like tokens. They never carry a secret
// or config payload: the action and the resource identity are the record.
package apihttp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

const (
	// DefaultAuditRetention is how long a record is kept when the caller asks for
	// no specific retention.
	DefaultAuditRetention = 30 * 24 * time.Hour

	auditFileMode = 0o600
	auditDirMode  = 0o700

	// auditMaxLineBytes bounds one record. A longer line is a malformed record,
	// not a reason to refuse to open the log.
	auditMaxLineBytes = 1 << 20
)

// AuditStore is an append-only audit log backed by one JSONL file. It is safe
// for concurrent use.
//
// The file is read once at Open, pruned of records older than the retention
// window, and then appended to. Every append fsyncs the line, so a crash loses
// at most the record in flight.
type AuditStore struct {
	mu        sync.RWMutex
	path      string
	retention time.Duration
	records   []v1.AuditRecord
}

// OpenAuditStore loads path, creating nothing until the first append. Records
// older than retention are dropped and the file is rewritten atomically; a
// retention of zero or less means DefaultAuditRetention.
//
// A missing file is an empty log. Malformed lines are skipped rather than
// making the whole log unreadable: a crash can truncate the final line, and one
// bad record must not hide the rest.
func OpenAuditStore(path string, retention time.Duration) (*AuditStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, output.NewError(output.CodeInvalid, "audit store path must not be empty")
	}
	if retention <= 0 {
		retention = DefaultAuditRetention
	}

	s := &AuditStore{path: path, retention: retention}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// load reads the file and applies retention.
func (s *AuditStore) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return output.NewErrorf(output.CodeInternal, "read audit store %s: %v", s.path, err)
	}

	records := make([]v1.AuditRecord, 0, bytes.Count(data, []byte{'\n'})+1)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), auditMaxLineBytes)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec v1.AuditRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if rec.At == "" || rec.Action == "" {
			continue
		}
		records = append(records, rec)
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, bufio.ErrTooLong) {
		return output.NewErrorf(output.CodeInternal, "read audit store %s: %v", s.path, err)
	}

	cutoff := time.Now().Add(-s.retention)
	kept := make([]v1.AuditRecord, 0, len(records))
	pruned := false
	for _, rec := range records {
		at, err := time.Parse(time.RFC3339Nano, rec.At)
		// A record whose timestamp cannot be read is never pruned: dropping what
		// cannot be dated would silently lose audit history.
		if err == nil && at.Before(cutoff) {
			pruned = true
			continue
		}
		kept = append(kept, rec)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.records = kept
	if !pruned {
		return nil
	}
	return s.rewriteLocked()
}

// Append writes one record and fsyncs it. A nil receiver is not used here: the
// Server checks for a nil store so read routes stay a no-op.
func (s *AuditStore) Append(rec v1.AuditRecord) error {
	if strings.TrimSpace(rec.At) == "" || rec.RequestID == "" || rec.Action == "" {
		return output.NewError(output.CodeInvalid,
			"audit record needs at, requestId and action")
	}

	line, err := json.Marshal(rec)
	if err != nil {
		return output.NewErrorf(output.CodeInternal, "encode audit record: %v", err)
	}
	line = append(line, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.path), auditDirMode); err != nil {
		return output.NewErrorf(output.CodeInternal, "create audit store directory: %v", err)
	}
	file, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, auditFileMode)
	if err != nil {
		return output.NewErrorf(output.CodeInternal, "open audit store %s: %v", s.path, err)
	}
	if _, err := file.Write(line); err != nil {
		_ = file.Close()
		return output.NewErrorf(output.CodeInternal, "append audit store %s: %v", s.path, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return output.NewErrorf(output.CodeInternal, "sync audit store %s: %v", s.path, err)
	}
	if err := file.Close(); err != nil {
		return output.NewErrorf(output.CodeInternal, "close audit store %s: %v", s.path, err)
	}

	s.records = append(s.records, rec)
	return nil
}

// List returns one page ordered by at, then requestId, ascending, and the
// cursor of the last returned record when more remain. limit must be positive;
// the HTTP handler validates it with parseLimit.
func (s *AuditStore) List(limit int, cursor string) ([]v1.AuditRecord, *string, error) {
	if limit <= 0 {
		return nil, nil, output.NewError(output.CodeInvalid, "limit must be positive")
	}

	s.mu.RLock()
	sorted := make([]v1.AuditRecord, len(s.records))
	copy(sorted, s.records)
	s.mu.RUnlock()

	sort.SliceStable(sorted, func(i, j int) bool {
		if cmp := compareCreatedAt(sorted[i].At, sorted[j].At); cmp != 0 {
			return cmp < 0
		}
		return sorted[i].RequestID < sorted[j].RequestID
	})

	return paginate(sorted, limit, cursor, func(rec v1.AuditRecord) (string, string) {
		return rec.At, rec.RequestID
	})
}

// rewriteLocked replaces the file with the in-memory set. The caller must hold
// the write lock.
func (s *AuditStore) rewriteLocked() error {
	var buf bytes.Buffer
	for _, rec := range s.records {
		line, err := json.Marshal(rec)
		if err != nil {
			return output.NewErrorf(output.CodeInternal, "encode audit record: %v", err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}

	if err := writeFileAtomic(s.path, buf.Bytes()); err != nil {
		return output.NewErrorf(output.CodeInternal, "write audit store %s: %v", s.path, err)
	}
	return nil
}

// recordAudit appends one record for the caller's identity. A nil store is a
// no-op, so a manager without an audit log does not fail future writes.
//
// Phase-2 write handlers call it with these action names:
// service.create, service.update, service.delete, config.create, config.update,
// config.delete, secret.create, secret.update, secret.delete, network.create,
// network.delete and node.update. Phase-3 adds task.exec, task.files and
// task.snapshot. Result is "ok" or "error"; nothing here inspects it.
func (s *Server) recordAudit(r *http.Request, action string, res v1.AuditResource, result string) error {
	if s.cfg.Audit == nil {
		return nil
	}

	identity, _ := IdentityFrom(r.Context())
	return s.cfg.Audit.Append(v1.AuditRecord{
		At:        time.Now().UTC().Format(time.RFC3339Nano),
		RequestID: requestIDFrom(r),
		Actor: v1.AuditActor{
			Type: identity.Type,
			ID:   identity.TokenID,
			Name: identity.Name,
		},
		Action:   action,
		Resource: res,
		Result:   result,
		SourceIP: auditSourceIP(r),
	})
}

// auditSourceIP returns the TCP peer address, host only. A forwarded header is
// deliberately ignored: trusting X-Forwarded-For would let any client write any
// source address into the audit log. A Unix-socket request has no host:port
// peer address, so its source IP is empty.
func auditSourceIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return ""
	}
	return host
}

// listAudit serves GET /v1/audit.
func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Audit == nil {
		s.writeError(w, r, output.NewError(output.CodeNotImplemented,
			"this manager does not record an audit log").
			WithDetails(map[string]any{"capability": "audit"}))
		return
	}

	limit, err := parseLimit(r.URL.Query())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	records, next, err := s.cfg.Audit.List(limit, r.URL.Query().Get("cursor"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	doc := v1.NewAuditList(records)
	doc.Next = next
	s.writeJSON(w, r, http.StatusOK, doc)
}
