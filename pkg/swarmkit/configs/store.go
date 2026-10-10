// Package configs provides an in-memory store for SwarmKit config payloads.
//
// It implements the swarmkit agent's exec.ConfigsManager interface so that the
// agent can push config data to the executor as assignments change. The
// executor looks configs up by ID while preparing a task, so the payload can be
// injected into the guest rootfs.
package configs

import (
	"sync"

	"github.com/moby/swarmkit/v2/api"
)

// Store is a concurrency-safe, in-memory config store keyed by config ID.
type Store struct {
	mu      sync.RWMutex
	configs map[string]*api.Config
}

// NewStore creates an empty config store.
func NewStore() *Store {
	return &Store{configs: make(map[string]*api.Config)}
}

// Get returns the config with the given ID. It returns (nil, nil) when the
// config is not present, matching the exec.ConfigGetter contract.
func (s *Store) Get(configID string) (*api.Config, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.configs[configID], nil
}

// Add stores one or more configs, replacing any existing config with the same
// ID.
func (s *Store) Add(configs ...api.Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range configs {
		c := configs[i]
		s.configs[c.ID] = &c
	}
}

// Remove deletes the configs with the given IDs.
func (s *Store) Remove(ids []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		delete(s.configs, id)
	}
}

// Reset removes all configs.
func (s *Store) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configs = make(map[string]*api.Config)
}
