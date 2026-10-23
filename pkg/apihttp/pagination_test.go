package apihttp

import (
	"encoding/base64"
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
		{"", 100},
		{"1", 1},
		{"2", 2},
		{"100", 100},
		{"1000", 1000},
	}
	for _, tc := range cases {
		got, err := parseLimit(tc.in)
		require.NoError(t, err, tc.in)
		assert.Equal(t, tc.want, got, tc.in)
	}

	for _, in := range []string{"0", "-1", "1001", "5000", "abc", "1.5", " "} {
		_, err := parseLimit(in)
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

func TestPaginateRejectsBadCursor(t *testing.T) {
	_, _, err := paginate([]pageItem{}, 10, "%%%", pageKey)
	require.Error(t, err)
	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeInvalid, typed.Code)
}
