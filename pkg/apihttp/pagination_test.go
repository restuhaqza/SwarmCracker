package apihttp

import (
	"encoding/base64"
	"net/url"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseLimit(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"1", 1},
		{"2", 2},
		{"100", 100},
		{"1000", 1000},
	}
	for _, tc := range cases {
		got, err := parseLimit(url.Values{"limit": {tc.in}})
		require.NoError(t, err, tc.in)
		assert.Equal(t, tc.want, got, tc.in)
	}

	// An absent parameter means the default.
	got, err := parseLimit(url.Values{})
	require.NoError(t, err)
	assert.Equal(t, 100, got)

	for _, in := range []string{"", "0", "-1", "1001", "5000", "abc", "1.5", " "} {
		_, err := parseLimit(url.Values{"limit": {in}})
		require.Error(t, err, in)
		var typed *output.Error
		require.ErrorAs(t, err, &typed)
		assert.Equal(t, output.CodeInvalid, typed.Code, in)
		fields, ok := typed.Details["fields"].(map[string]any)
		require.True(t, ok, in)
		assert.Contains(t, fields, "limit", in)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	encoded := encodeCursor("2026-01-02T00:00:00Z", "svc-42")
	cur, err := decodeCursor(encoded)
	require.NoError(t, err)
	assert.Equal(t, "2026-01-02T00:00:00Z", cur.createdAt)
	assert.Equal(t, "svc-42", cur.id)

	// Cursors are opaque but must survive a URL query round trip.
	assert.NotContains(t, encoded, "+")
	assert.NotContains(t, encoded, "/")
	assert.NotContains(t, encoded, "=")
}

func TestDecodeCursorMalformed(t *testing.T) {
	cases := []string{
		"!!!not-base64!!!",
		base64.RawURLEncoding.EncodeToString([]byte("no-separator")),
		base64.RawURLEncoding.EncodeToString([]byte("2026-01-02T00:00:00Z\x00")), // empty id
	}
	for _, in := range cases {
		_, err := decodeCursor(in)
		require.Error(t, err, in)
		var typed *output.Error
		require.ErrorAs(t, err, &typed)
		assert.Equal(t, output.CodeInvalid, typed.Code, in)
	}
}

type pageItem struct {
	createdAt string
	id        string
}

func pageKey(item pageItem) (string, string) { return item.createdAt, item.id }

func TestPaginate(t *testing.T) {
	items := []pageItem{
		{"2026-01-01T00:00:00Z", "a"},
		{"2026-01-01T00:00:00Z", "b"},
		{"2026-01-02T00:00:00Z", "c"},
	}

	page, next, err := paginate(items, 2, "", pageKey)
	require.NoError(t, err)
	require.Len(t, page, 2)
	assert.Equal(t, "a", page[0].id)
	require.NotNil(t, next)

	page2, next2, err := paginate(items, 2, *next, pageKey)
	require.NoError(t, err)
	require.Len(t, page2, 1)
	assert.Equal(t, "c", page2[0].id)
	assert.Nil(t, next2)

	// Everything fits in one page: no cursor.
	page3, next3, err := paginate(items, 100, "", pageKey)
	require.NoError(t, err)
	require.Len(t, page3, 3)
	assert.Nil(t, next3)

	// A cursor past the end returns an empty page and no next.
	last := encodeCursor("2026-01-03T00:00:00Z", "z")
	page4, next4, err := paginate(items, 2, last, pageKey)
	require.NoError(t, err)
	assert.Empty(t, page4)
	assert.Nil(t, next4)
}

func TestPaginateSameTimestampUsesIDTiebreak(t *testing.T) {
	items := []pageItem{
		{"2026-01-01T00:00:00Z", "a"},
		{"2026-01-01T00:00:00Z", "b"},
		{"2026-01-01T00:00:00Z", "c"},
	}

	page, next, err := paginate(items, 1, "", pageKey)
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.NotNil(t, next)

	page2, _, err := paginate(items, 1, *next, pageKey)
	require.NoError(t, err)
	require.Len(t, page2, 1)
	assert.Equal(t, "b", page2[0].id)
}

func TestPaginateFractionalSecondsAcrossPageBoundary(t *testing.T) {
	// Go formats RFC3339Nano with trailing zeros stripped, so these values have
	// different fractional widths. Lexically, "...00.12Z" sorts AFTER
	// "...00.125Z" (Z > 5), which used to make the cursor skip .125 entirely.
	items := []pageItem{
		{"2026-01-01T00:00:00.12Z", "a"},
		{"2026-01-01T00:00:00.125Z", "b"},
		{"2026-01-01T00:00:00.13Z", "c"},
		{"2026-01-01T00:00:01Z", "d"},
	}

	var seen []string
	cursor := ""
	for pages := 0; ; pages++ {
		require.Less(t, pages, 10, "pagination did not terminate")

		page, next, err := paginate(items, 1, cursor, pageKey)
		require.NoError(t, err)
		for _, item := range page {
			seen = append(seen, item.id)
		}
		if next == nil {
			break
		}
		cursor = *next
	}

	assert.Equal(t, []string{"a", "b", "c", "d"}, seen, "no item may be dropped at a fractional-second boundary")
}

func TestAfterCursorComparesInstants(t *testing.T) {
	cursor := pageCursor{createdAt: "2026-01-01T00:00:00.12Z", id: "a"}

	// Later instant but lexically smaller string.
	assert.True(t, afterCursor("2026-01-01T00:00:00.125Z", "b", cursor))
	// Earlier instant, lexically larger string.
	assert.False(t, afterCursor("2026-01-01T00:00:00.119Z", "b", cursor))
	// The same instant written with another offset is equal, so the ID decides.
	assert.True(t, afterCursor("2026-01-01T01:00:00.12+01:00", "b", cursor))

	// Values that do not parse fall back to lexical order.
	assert.True(t, afterCursor("zzz", "b", pageCursor{createdAt: "aaa", id: "a"}))
	assert.False(t, afterCursor("aaa", "b", pageCursor{createdAt: "zzz", id: "a"}))
}

func TestPaginateRejectsBadCursor(t *testing.T) {
	_, _, err := paginate([]pageItem{}, 10, "%%%", pageKey)
	require.Error(t, err)
	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeInvalid, typed.Code)
}
