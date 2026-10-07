package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatchRef(t *testing.T) {
	candidates := []refCandidate{
		{ID: "abcdef1234567890alpha", Name: "web"},
		{ID: "abcdef1234567890beta", Name: "api"},
		{ID: "ffffffffffffffffffffffff", Name: ""},
	}

	// Exact ID.
	id, err := matchRef("abcdef1234567890alpha", "service", candidates)
	require.NoError(t, err)
	assert.Equal(t, "abcdef1234567890alpha", id)

	// Exact name.
	id, err = matchRef("api", "service", candidates)
	require.NoError(t, err)
	assert.Equal(t, "abcdef1234567890beta", id)

	// Ambiguous prefix (two share "abcdef").
	_, err = matchRef("abcdef", "service", candidates)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ambiguous")

	// Unique prefix.
	id, err = matchRef("ffff", "service", candidates)
	require.NoError(t, err)
	assert.Equal(t, "ffffffffffffffffffffffff", id)

	// Unknown.
	_, err = matchRef("deadbeef", "service", candidates)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	// Empty.
	_, err = matchRef("", "service", candidates)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}
