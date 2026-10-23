// This file owns list pagination: limit parsing, the opaque cursor and the
// window function shared by every list handler. Ordering is the Client's
// contract (createdAt ascending, then id ascending); the HTTP layer only slices
// the stream, so a page never reorders what the manager reported.
package apihttp

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"

	"github.com/restuhaqza/swarmcracker/pkg/output"
)

const (
	// defaultLimit is the page size when the request omits limit.
	defaultLimit = 100
	// maxLimit is the hard page-size ceiling; larger values are invalid.
	maxLimit = 1000

	// cursorSeparator joins the two cursor components. NUL cannot appear in an
	// RFC 3339 timestamp or in a resource ID, so the split is unambiguous.
	cursorSeparator = "\x00"
)

// pageCursor is the position of the last item of a returned page.
type pageCursor struct {
	createdAt string
	id        string
}

// parseLimit validates the limit query parameter. An absent value means the
// default; zero, negatives, non-numeric values and values over the maximum are
// invalid, with the offending field named in details.fields.
func parseLimit(raw string) (int, error) {
	if raw == "" {
		return defaultLimit, nil
	}

	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 || limit > maxLimit {
		return 0, output.NewErrorf(output.CodeInvalid,
			"limit must be an integer between 1 and %d", maxLimit).
			WithDetails(map[string]any{"fields": map[string]any{"limit": raw}})
	}
	return limit, nil
}

// encodeCursor renders createdAt and id as an opaque URL-safe cursor.
func encodeCursor(createdAt, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(createdAt + cursorSeparator + id))
}

// decodeCursor parses a cursor produced by encodeCursor. Both raw and padded
// base64url are accepted so a client that round-trips the value through a
// generic base64 decoder still works; anything else is invalid.
func decodeCursor(raw string) (pageCursor, error) {
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		data, err = base64.URLEncoding.DecodeString(raw)
	}
	if err != nil {
		return pageCursor{}, invalidCursor(raw)
	}

	createdAt, id, found := strings.Cut(string(data), cursorSeparator)
	if !found || id == "" {
		return pageCursor{}, invalidCursor(raw)
	}
	return pageCursor{createdAt: createdAt, id: id}, nil
}

func invalidCursor(raw string) error {
	return output.NewError(output.CodeInvalid, "malformed cursor").
		WithDetails(map[string]any{"cursor": raw})
}

// paginate returns at most limit items starting after rawCursor and the cursor
// of the last returned item when more items remain. An empty rawCursor starts at
// the beginning.
func paginate[T any](items []T, limit int, rawCursor string, key func(T) (string, string)) ([]T, *string, error) {
	start := 0
	if rawCursor != "" {
		cursor, err := decodeCursor(rawCursor)
		if err != nil {
			return nil, nil, err
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
	next := encodeCursor(createdAt, id)
	return page, &next, nil
}

// afterCursor reports whether the item at createdAt/id sorts strictly after the
// cursor. RFC 3339 timestamps compare correctly as strings.
func afterCursor(createdAt, id string, cursor pageCursor) bool {
	if createdAt != cursor.createdAt {
		return createdAt > cursor.createdAt
	}
	return id > cursor.id
}

// paginateRequest applies the limit and cursor query parameters to items.
func paginateRequest[T any](r *http.Request, items []T, key func(T) (string, string)) ([]T, *string, error) {
	query := r.URL.Query()

	limit, err := parseLimit(query.Get("limit"))
	if err != nil {
		return nil, nil, err
	}
	return paginate(items, limit, query.Get("cursor"), key)
}
