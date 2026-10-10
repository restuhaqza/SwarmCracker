// Package secrets provides an in-memory store for SwarmKit secret payloads.
//
// It implements the swarmkit agent's exec.SecretsManager interface so that the
// agent can push secret data to the executor as assignments change. The
// executor looks secrets up by ID while preparing a task, so the payload can be
// injected into the guest rootfs.
package secrets

import (
	"sync"

	"github.com/moby/swarmkit/v2/api"
)

// Store is a concurrency-safe, in-memory secret store keyed by secret ID.
type Store struct {
	mu      sync.RWMutex
	secrets map[string]*api.Secret
}

// NewStore creates an empty secret store.
func NewStore() *Store {
	return &Store{secrets: make(map[string]*api.Secret)}
}

// Get returns the secret with the given ID. It returns (nil, nil) when the
// secret is not present, matching the exec.SecretGetter contract.
func (s *Store) Get(secretID string) (*api.Secret, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.secrets[secretID], nil
}

// Add stores one or more secrets, replacing any existing secret with the same
// ID.
func (s *Store) Add(secrets ...api.Secret) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range secrets {
		sec := secrets[i]
		s.secrets[sec.ID] = &sec
	}
}

// Remove deletes the secrets with the given IDs.
func (s *Store) Remove(ids []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		delete(s.secrets, id)
	}
}

// Reset removes all secrets.
func (s *Store) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secrets = make(map[string]*api.Secret)
}
