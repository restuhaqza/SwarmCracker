// This file owns list pagination: limit parsing, the opaque cursor and the
// window function shared by every list handler. Ordering is the Client's
// contract (createdAt ascending, then id ascending); the HTTP layer only slices
// the stream, so a page never reorders what the manager reported.
package apihttp

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/output"
)

const (
	// defaultLimit is the page size when the request omits limit.
	defaultLimit = 100
	// maxLimit is the hard page-size ceiling; larger values are invalid.
	maxLimit = 1000

	// cursorSeparator joins the cursor components. NUL cannot appear in an
	// RFC 3339 timestamp, a resource ID or a fingerprint, so the split is
	// unambiguous.
	cursorSeparator = "\x00"

	// auditCursorScope is the fingerprint the manager-local audit log binds its
	// cursors to. Audit records are not read through the paginated route table,
	// so the store names its own scope. It is not a route fingerprint: a cursor
	// from any public list is rejected by AuditStore.List and vice versa.
	auditCursorScope = "audit"
)

// pageCursor is the position of the last item of a returned page. Fingerprint
// binds the cursor to the query that produced it.
type pageCursor struct {
	createdAt   string
	id          string
	fingerprint string
}

// parseLimit validates the limit query parameter. An absent parameter means the
// default; a present but empty value is invalid, exactly like any other
// non-numeric one. Zero, negatives and values over the maximum are invalid too,
// with the offending field named in details.fields.
func parseLimit(query url.Values) (int, error) {
	if !query.Has("limit") {
		return defaultLimit, nil
	}

	raw := query.Get("limit")
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 || limit > maxLimit {
		return 0, output.NewErrorf(output.CodeInvalid,
			"limit must be an integer between 1 and %d", maxLimit).
			WithDetails(map[string]any{"fields": map[string]any{"limit": raw}})
	}
	return limit, nil
}

// encodeCursor renders the query fingerprint, createdAt and id as an opaque
// URL-safe cursor.
func encodeCursor(fingerprint, createdAt, id string) string {
	return base64.RawURLEncoding.EncodeToString(
		[]byte(createdAt + cursorSeparator + id + cursorSeparator + fingerprint))
}

// decodeCursor parses a cursor produced by encodeCursor. Both raw and padded
// base64url are accepted so a client that round-trips the value through a
// generic base64 decoder still works; anything else is invalid.
//
// A cursor without a fingerprint decodes with an empty one. That is a legacy
// (pre-binding) cursor: it is well formed, but no query claims it, so paginate
// rejects it as a mismatch rather than as malformed input.
func decodeCursor(raw string) (pageCursor, error) {
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		data, err = base64.URLEncoding.DecodeString(raw)
	}
	if err != nil {
		return pageCursor{}, invalidCursor(raw)
	}

	createdAt, rest, found := strings.Cut(string(data), cursorSeparator)
	if !found {
		return pageCursor{}, invalidCursor(raw)
	}
	id, fingerprint, _ := strings.Cut(rest, cursorSeparator)
	if id == "" {
		return pageCursor{}, invalidCursor(raw)
	}
	return pageCursor{createdAt: createdAt, id: id, fingerprint: fingerprint}, nil
}

func invalidCursor(raw string) error {
	return output.NewError(output.CodeInvalid, "malformed cursor").
		WithDetails(map[string]any{"cursor": raw})
}

// cursorMismatch is the 400 for a cursor issued for a different route or filter.
// The raw value is not echoed: a cursor is a position, not caller input worth
// repeating back in an error document.
func cursorMismatch() error {
	return output.NewError(output.CodeInvalid,
		"cursor was issued for a different query").
		WithDetails(map[string]any{"reason": "cursor_mismatch"})
}

// requestFingerprint binds a cursor to the route that produced it and to the
// effective filter values that selected its items. The route comes from the
// matched ServeMux pattern (so it is the declared route, not a caller-supplied
// path); filters are the values the handler actually applied, in a canonical
// order. limit and cursor are deliberately absent: a caller may change the page
// size between pages without invalidating its position.
func requestFingerprint(r *http.Request, filters []string) string {
	route := r.Pattern
	if route == "" {
		// A handler invoked outside the mux (a unit test, a future embedder)
		// still gets a stable binding: the method and path identify the query.
		route = r.Method + " " + r.URL.Path
	}

	hash := sha256.New()
	hash.Write([]byte(route))
	for _, filter := range filters {
		hash.Write([]byte(cursorSeparator))
		hash.Write([]byte(filter))
	}
	return hex.EncodeToString(hash.Sum(nil))[:16]
}

// paginate returns at most limit items starting after rawCursor and the cursor
// of the last returned item when more items remain. An empty rawCursor starts at
// the beginning. A cursor whose fingerprint differs from the current query -
// including a legacy cursor that carries none - is a cursor_mismatch.
func paginate[T any](
	items []T, limit int, rawCursor, fingerprint string, key func(T) (string, string),
) ([]T, *string, error) {
	start := 0
	if rawCursor != "" {
		cursor, err := decodeCursor(rawCursor)
		if err != nil {
			return nil, nil, err
		}
		if cursor.fingerprint != fingerprint {
			return nil, nil, cursorMismatch()
		}
		for start < len(items) {
			createdAt, id := key(items[start])
			if afterCursor(createdAt, id, cursor) {
				break
			}
			start++
		}
	}

	end := start + limit
	if end > len(items) {
		end = len(items)
	}

	page := items[start:end]
	if end == len(items) {
		return page, nil, nil
	}

	createdAt, id := key(items[end-1])
	next := encodeCursor(fingerprint, createdAt, id)
	return page, &next, nil
}

// afterCursor reports whether the item at createdAt/id sorts strictly after the
// cursor. Timestamps are compared as instants: RFC 3339 allows a fractional
// second of variable width (Go's RFC3339Nano drops trailing zeros), and
// "...00.12Z" sorts after "...00.125Z" as a string even though it is earlier.
// If either value does not parse, the comparison falls back to raw strings so a
// non-conforming client stream still pages deterministically.
func afterCursor(createdAt, id string, cursor pageCursor) bool {
	if cmp := compareCreatedAt(createdAt, cursor.createdAt); cmp != 0 {
		return cmp > 0
	}
	return id > cursor.id
}

// compareCreatedAt orders two RFC 3339 timestamps as instants, falling back to
// lexical order when either one cannot be parsed.
func compareCreatedAt(a, b string) int {
	ta, aerr := time.Parse(time.RFC3339Nano, a)
	tb, berr := time.Parse(time.RFC3339Nano, b)
	if aerr == nil && berr == nil {
		return ta.Compare(tb)
	}
	return strings.Compare(a, b)
}

// paginateRequest applies the limit and cursor query parameters to items.
// filters are the filter values the handler applied to items (label selectors, a
// path reference, a resolved task ID): they bind the cursor to the query that
// produced it, so a cursor cannot page a different result set.
func paginateRequest[T any](
	r *http.Request, items []T, key func(T) (string, string), filters ...string,
) ([]T, *string, error) {
	query := r.URL.Query()

	limit, err := parseLimit(query)
	if err != nil {
		return nil, nil, err
	}
	return paginate(items, limit, query.Get("cursor"), requestFingerprint(r, filters), key)
}
