package network

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestStaticPeerStore tests the static peer store
func TestStaticPeerStore(t *testing.T) {
	tests := []struct {
		name         string
		initialPeers []string
		addPeers     []string
		removePeers  []string
		expected     []string
	}{
		{
			name:         "empty store",
			initialPeers: []string{},
			addPeers:     []string{},
			removePeers:  []string{},
			expected:     []string{},
		},
		{
			name:         "initial peers",
			initialPeers: []string{"192.168.1.10", "192.168.1.11"},
			addPeers:     []string{},
			removePeers:  []string{},
			expected:     []string{"192.168.1.10", "192.168.1.11"},
		},
		{
			name:         "add peers",
			initialPeers: []string{"192.168.1.10"},
			addPeers:     []string{"192.168.1.11", "192.168.1.12"},
			removePeers:  []string{},
			expected:     []string{"192.168.1.10", "192.168.1.11", "192.168.1.12"},
		},
		{
			name:         "remove peers",
			initialPeers: []string{"192.168.1.10", "192.168.1.11", "192.168.1.12"},
			addPeers:     []string{},
			removePeers:  []string{"192.168.1.11"},
			expected:     []string{"192.168.1.10", "192.168.1.12"},
		},
		{
			name:         "add and remove",
			initialPeers: []string{"192.168.1.10"},
			addPeers:     []string{"192.168.1.11"},
			removePeers:  []string{"192.168.1.10"},
			expected:     []string{"192.168.1.11"},
		},
		{
			name:         "duplicate add",
			initialPeers: []string{"192.168.1.10"},
			addPeers:     []string{"192.168.1.10"}, // Already exists
			removePeers:  []string{},
			expected:     []string{"192.168.1.10"}, // Should not duplicate
		},
		{
			name:         "remove non-existent",
			initialPeers: []string{"192.168.1.10"},
			addPeers:     []string{},
			removePeers:  []string{"192.168.1.99"}, // Doesn't exist
			expected:     []string{"192.168.1.10"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps := NewStaticPeerStore(tt.initialPeers)

			for _, peer := range tt.addPeers {
				ps.AddPeer(peer)
			}

			for _, peer := range tt.removePeers {
				ps.RemovePeer(peer)
			}

			result := ps.GetPeers()

			// Order might vary, so compare as sets
			assert.ElementsMatch(t, tt.expected, result)
		})
	}
}

// TestStaticPeerStore_Concurrent tests concurrent access
func TestStaticPeerStore_Concurrent(t *testing.T) {
	ps := NewStaticPeerStore([]string{"192.168.1.10"})

	// Concurrent adds and removes
	done := make(chan bool)

	for i := 0; i < 10; i++ {
		go func(idx int) {
			ps.AddPeer(fmt.Sprintf("192.168.1.%d", idx+20))
			done <- true
		}(i)
	}

	for i := 0; i < 5; i++ {
		go func() {
			ps.GetPeers()
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 15; i++ {
		<-done
	}

	// Should have at least initial peer
	peers := ps.GetPeers()
	assert.Contains(t, peers, "192.168.1.10")
}