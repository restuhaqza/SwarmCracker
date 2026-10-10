package secrets

import (
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStore_AddGetRemoveReset(t *testing.T) {
	s := NewStore()

	// Missing secret returns (nil, nil).
	got, err := s.Get("missing")
	require.NoError(t, err)
	assert.Nil(t, got)

	s.Add(api.Secret{ID: "s1", Spec: api.SecretSpec{Data: []byte("one")}})
	got, err = s.Get("s1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, []byte("one"), got.Spec.Data)

	// Add replaces an existing secret with the same ID.
	s.Add(api.Secret{ID: "s1", Spec: api.SecretSpec{Data: []byte("two")}})
	got, _ = s.Get("s1")
	assert.Equal(t, []byte("two"), got.Spec.Data)

	s.Add(api.Secret{ID: "s2"}, api.Secret{ID: "s3"})
	s.Remove([]string{"s1", "s3"})
	assert.Nil(t, mustGet(t, s, "s1"))
	assert.Nil(t, mustGet(t, s, "s3"))
	assert.NotNil(t, mustGet(t, s, "s2"))

	s.Reset()
	assert.Nil(t, mustGet(t, s, "s2"))
}

func mustGet(t *testing.T, s *Store, id string) *api.Secret {
	t.Helper()
	got, err := s.Get(id)
	require.NoError(t, err)
	return got
}
