package configs

import (
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStore_AddGetRemoveReset(t *testing.T) {
	s := NewStore()

	got, err := s.Get("missing")
	require.NoError(t, err)
	assert.Nil(t, got)

	s.Add(api.Config{ID: "c1", Spec: api.ConfigSpec{Data: []byte("one")}})
	got, err = s.Get("c1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, []byte("one"), got.Spec.Data)

	s.Add(api.Config{ID: "c1", Spec: api.ConfigSpec{Data: []byte("two")}})
	got, _ = s.Get("c1")
	assert.Equal(t, []byte("two"), got.Spec.Data)

	s.Add(api.Config{ID: "c2"}, api.Config{ID: "c3"})
	s.Remove([]string{"c1", "c3"})
	assert.Nil(t, mustGet(t, s, "c1"))
	assert.Nil(t, mustGet(t, s, "c3"))
	assert.NotNil(t, mustGet(t, s, "c2"))

	s.Reset()
	assert.Nil(t, mustGet(t, s, "c2"))
}

func mustGet(t *testing.T, s *Store, id string) *api.Config {
	t.Helper()
	got, err := s.Get(id)
	require.NoError(t, err)
	return got
}
