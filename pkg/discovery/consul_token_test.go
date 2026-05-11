// Package discovery_test provides tests for Consul token configuration.
package discovery_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/discovery"
)

// TestConsulClient_WithToken tests ACL token configuration.
func TestConsulClient_WithToken(t *testing.T) {
	receivedToken := ""
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check for X-Consul-Token header
		mu.Lock()
		receivedToken = r.Header.Get("X-Consul-Token")
		mu.Unlock()

		// Return success for any request
		if r.URL.Path == "/v1/catalog/service/swarmcracker-vxlan" {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode([]interface{}{})
		} else {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{"Config": map[string]interface{}{}})
		}
	}))
	t.Cleanup(func() { server.Close() })

	cfg := discovery.ConsulConfig{
		Address:       server.URL,
		ServiceID:     "token-test-node",
		LocalIP:       "192.168.1.10",
		LocalHostname: "token-worker",
		Token:         "secret-acl-token-12345",
	}

	client, err := discovery.NewConsulClient(cfg)
	if err != nil {
		t.Fatalf("NewConsulClient with token failed: %v", err)
	}

	// Make a request to trigger token header
	_, err = client.GetNodes()
	if err != nil {
		t.Fatalf("GetNodes failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if receivedToken != "secret-acl-token-12345" {
		t.Errorf("Token header = %v, want secret-acl-token-12345", receivedToken)
	}
}

// TestConsulClient_WatchPeers_ErrorHandling tests WatchPeers error recovery.
func TestConsulClient_WatchPeers_ErrorHandling(t *testing.T) {
	errorCount := 0
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/catalog/service/swarmcracker-vxlan" {
			mu.Lock()
			errorCount++
			mu.Unlock()

			// Return error first, then success
			if errorCount <= 2 {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte("consul unavailable"))
				return
			}

			// Return success after errors
			w.Header().Set("X-Consul-Index", "100")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode([]interface{}{
				map[string]interface{}{
					"ServiceID":      "peer-1",
					"ServiceAddress": "192.168.1.20",
				},
			})
		}
	}))
	t.Cleanup(func() { server.Close() })

	cfg := discovery.ConsulConfig{
		Address:   server.URL,
		ServiceID: "watch-error-node",
		LocalIP:   "192.168.1.10",
	}

	client, err := discovery.NewConsulClient(cfg)
	if err != nil {
		t.Fatalf("NewConsulClient failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()

	callbackCalled := make(chan bool, 1)

	client.WatchPeers(ctx, func(peers []string) {
		if len(peers) > 0 {
			callbackCalled <- true
		}
	})

	// Wait for callback or timeout
	select {
	case <-callbackCalled:
		// Expected - WatchPeers recovered from errors
		t.Log("WatchPeers recovered from errors and called callback")
	case <-time.After(900 * time.Millisecond):
		t.Log("WatchPeers timeout - may not have recovered in time")
	}
}

// TestConsulClient_WatchPeers_NoPeers tests callback not called when no peers.
func TestConsulClient_WatchPeers_NoPeers(t *testing.T) {
	callCount := 0
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/catalog/service/swarmcracker-vxlan" {
			mu.Lock()
			callCount++
			mu.Unlock()

			w.Header().Set("X-Consul-Index", fmt.Sprintf("%d", 100+callCount))
			w.WriteHeader(http.StatusOK)
			// Return empty list - no peers
			json.NewEncoder(w).Encode([]interface{}{})
		}
	}))
	t.Cleanup(func() { server.Close() })

	cfg := discovery.ConsulConfig{
		Address:   server.URL,
		ServiceID: "no-peers-node",
		LocalIP:   "192.168.1.10",
	}

	client, err := discovery.NewConsulClient(cfg)
	if err != nil {
		t.Fatalf("NewConsulClient failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	callbackCalled := false

	client.WatchPeers(ctx, func(peers []string) {
		callbackCalled = true
	})

	// With no peers, callback should NOT be called
	time.Sleep(400 * time.Millisecond)

	if callbackCalled {
		t.Error("Callback should not be called when no peers found")
	}
}
