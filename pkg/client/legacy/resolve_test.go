package legacy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatchRef(t *testing.T) {
	candidates := []refCandidate{
		{ID: "abcdef1234567890alpha", Name: "api"},
		{ID: "abcdef1234567890beta", Name: "web"},
		{ID: "deadbeef00000000", Name: "worker"},
	}

	// Full ID wins.
	id, err := matchRef("abcdef1234567890alpha", "service", candidates)
	require.NoError(t, err)
	assert.Equal(t, "abcdef1234567890alpha", id)

	// Exact name wins before a prefix.
	id, err = matchRef("api", "service", candidates)
	require.NoError(t, err)
	assert.Equal(t, "abcdef1234567890alpha", id)

	// Unique prefix.
	id, err = matchRef("dead", "service", candidates)
	require.NoError(t, err)
	assert.Equal(t, "deadbeef00000000", id)

	// Missing.
	_, err = matchRef("ffff", "service", candidates)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	// Ambiguous prefix.
	_, err = matchRef("abcdef", "service", candidates)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ambiguous")

	// Empty.
	_, err = matchRef("", "service", candidates)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}
