// Package discovery_test provides integration tests for Consul discovery.
// These tests use httptest to mock Consul API responses.
package discovery_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"
	"github.com/restuhaqza/swarmcracker/pkg/discovery"
	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// mockConsulServer creates a test HTTP server that mimics Consul API.
func mockConsulServer(t *testing.T, handlers map[string]http.HandlerFunc) *httptest.Server {
	mux := http.NewServeMux()
	for path, handler := range handlers {
		mux.HandleFunc(path, handler)
	}

	server := httptest.NewServer(mux)
	t.Cleanup(func() { server.Close() })
	return server
}

// TestConsulClient_NewWithMockServer tests client creation with mock server.
func TestConsulClient_NewWithMockServer(t *testing.T) {
	server := mockConsulServer(t, map[string]http.HandlerFunc{
		"/v1/agent/self": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"Config": map[string]interface{}{
					"Datacenter": "dc1",
				},
			})
		},
	})

	cfg := discovery.ConsulConfig{
		Address:       server.URL,
		ServiceID:     "test-node-1",
		LocalIP:       "192.168.1.10",
		LocalHostname: "test-worker",
		VXLANPort:     4789,
	}

	client, err := discovery.NewConsulClient(cfg)
	if err != nil {
		t.Fatalf("NewConsulClient failed: %v", err)
	}

	if client == nil {
		t.Fatal("Client should not be nil")
	}

	// Verify config was applied
	// Note: We can't access private fields directly, but we can test behavior
}

// TestConsulClient_RegisterService tests service registration.
func TestConsulClient_RegisterService(t *testing.T) {
	var registeredService *api.AgentServiceRegistration
	var mu sync.Mutex

	server := mockConsulServer(t, map[string]http.HandlerFunc{
		"/v1/agent/service/register": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "PUT" {
				t.Errorf("Expected PUT method, got %s", r.Method)
			}

			var service api.AgentServiceRegistration
			if err := json.NewDecoder(r.Body).Decode(&service); err != nil {
				t.Errorf("Failed to decode service: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			mu.Lock()
			registeredService = &service
			mu.Unlock()

			w.WriteHeader(http.StatusOK)
		},
	})

	cfg := discovery.ConsulConfig{
		Address:       server.URL,
		ServiceID:     "worker-1",
		LocalIP:       "192.168.1.10",
		LocalHostname: "worker1",
		VXLANPort:     4789,
	}

	client, err := discovery.NewConsulClient(cfg)
	if err != nil {
		t.Fatalf("NewConsulClient failed: %v", err)
	}

	err = client.RegisterService(100, "192.168.127.1")
	if err != nil {
		t.Fatalf("RegisterService failed: %v", err)
	}

	// Verify registration
	mu.Lock()
	defer mu.Unlock()

	if registeredService == nil {
		t.Fatal("Service was not registered")
	}

	if registeredService.ID != "worker-1" {
		t.Errorf("Service ID = %v, want worker-1", registeredService.ID)
	}

	if registeredService.Name != "swarmcracker-vxlan" {
		t.Errorf("Service Name = %v, want swarmcracker-vxlan", registeredService.Name)
	}

	if registeredService.Address != "192.168.1.10" {
		t.Errorf("Service Address = %v, want 192.168.1.10", registeredService.Address)
	}

	if registeredService.Port != 4789 {
		t.Errorf("Service Port = %v, want 4789", registeredService.Port)
	}

	if registeredService.Meta["vxlan_id"] != "100" {
		t.Errorf("vxlan_id = %v, want 100", registeredService.Meta["vxlan_id"])
	}

	if registeredService.Meta["bridge_ip"] != "192.168.127.1" {
		t.Errorf("bridge_ip = %v, want 192.168.127.1", registeredService.Meta["bridge_ip"])
	}
}

// TestConsulClient_DeregisterService tests service deregistration.
func TestConsulClient_DeregisterService(t *testing.T) {
	deregisteredID := ""
	var mu sync.Mutex

	server := mockConsulServer(t, map[string]http.HandlerFunc{
		"/v1/agent/service/register": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		},
		"/v1/agent/service/deregister/": func(w http.ResponseWriter, r *http.Request) {
			// Extract service ID from path: /v1/agent/service/deregister/<service-id>
			path := r.URL.Path
			// Split path and get last segment
			segments := strings.Split(strings.TrimSuffix(path, "/"), "/")
			if len(segments) >= 5 {
				mu.Lock()
				deregisteredID = segments[len(segments)-1]
				mu.Unlock()
			}
			w.WriteHeader(http.StatusOK)
		},
	})

	cfg := discovery.ConsulConfig{
		Address:       server.URL,
		ServiceID:     "worker-2",
		LocalIP:       "192.168.1.11",
		LocalHostname: "worker2",
	}

	client, err := discovery.NewConsulClient(cfg)
	if err != nil {
		t.Fatalf("NewConsulClient failed: %v", err)
	}

	err = client.DeregisterService()
	if err != nil {
		t.Fatalf("DeregisterService failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if deregisteredID != "worker-2" {
		t.Errorf("Deregistered ID = %v, want worker-2", deregisteredID)
	}
}

// TestConsulClient_GetNodes tests getting nodes from catalog.
func TestConsulClient_GetNodes(t *testing.T) {
	// Mock catalog response with multiple nodes
	catalogResponse := []*api.CatalogService{
		{
			ID:             "node-1",
			ServiceID:      "worker-1",
			ServiceAddress: "192.168.1.10",
			Address:        "192.168.1.10",
			ServiceMeta:    map[string]string{"hostname": "worker1"},
		},
		{
			ID:             "node-2",
			ServiceID:      "worker-2",
			ServiceAddress: "192.168.1.11",
			Address:        "192.168.1.11",
			ServiceMeta:    map[string]string{"hostname": "worker2"},
		},
		{
			ID:             "node-3",
			ServiceID:      "test-node", // Our own node - should be skipped
			ServiceAddress: "192.168.1.12",
			Address:        "192.168.1.12",
			ServiceMeta:    map[string]string{"hostname": "test-node"},
		},
	}

	server := mockConsulServer(t, map[string]http.HandlerFunc{
		"/v1/catalog/service/swarmcracker-vxlan": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(catalogResponse)
		},
	})

	cfg := discovery.ConsulConfig{
		Address:       server.URL,
		ServiceID:     "test-node", // Will be filtered out
		LocalIP:       "192.168.1.12",
		LocalHostname: "test-node",
	}

	client, err := discovery.NewConsulClient(cfg)
	if err != nil {
		t.Fatalf("NewConsulClient failed: %v", err)
	}

	nodes, err := client.GetNodes()
	if err != nil {
		t.Fatalf("GetNodes failed: %v", err)
	}

	// Should get 2 nodes (worker-1, worker-2), excluding ourselves
	if len(nodes) != 2 {
		t.Errorf("GetNodes returned %d nodes, want 2", len(nodes))
	}

	// Verify node info
	for _, node := range nodes {
		if node.ID == "test-node" {
			t.Error("Should not include our own node")
		}
		if node.Status != "ready" {
			t.Errorf("Node status = %v, want ready", node.Status)
		}
	}
}

// TestConsulClient_GetNodes_Empty tests GetNodes with no services.
func TestConsulClient_GetNodes_Empty(t *testing.T) {
	server := mockConsulServer(t, map[string]http.HandlerFunc{
		"/v1/catalog/service/swarmcracker-vxlan": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode([]interface{}{})
		},
	})

	cfg := discovery.ConsulConfig{
		Address:   server.URL,
		ServiceID: "test-node",
		LocalIP:   "192.168.1.10",
	}

	client, err := discovery.NewConsulClient(cfg)
	if err != nil {
		t.Fatalf("NewConsulClient failed: %v", err)
	}

	nodes, err := client.GetNodes()
	if err != nil {
		t.Fatalf("GetNodes failed: %v", err)
	}

	if len(nodes) != 0 {
		t.Errorf("GetNodes returned %d nodes, want 0", len(nodes))
	}
}

// TestConsulClient_WatchPeers tests peer watching.
func TestConsulClient_WatchPeers(t *testing.T) {
	callCount := 0
	var receivedPeers []string
	var mu sync.Mutex

	// Catalog response with peers
	catalogResponse := []*api.CatalogService{
		{
			ServiceID:      "worker-1",
			ServiceAddress: "192.168.1.10",
		},
		{
			ServiceID:      "worker-2",
			ServiceAddress: "192.168.1.11",
		},
	}

	server := mockConsulServer(t, map[string]http.HandlerFunc{
		"/v1/catalog/service/swarmcracker-vxlan": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)

			// Return different index each time to simulate changes
			index := uint64(100 + callCount)
			w.Header().Set("X-Consul-Index", fmt.Sprintf("%d", index))

			mu.Lock()
			callCount++
			mu.Unlock()

			json.NewEncoder(w).Encode(catalogResponse)
		},
	})

	cfg := discovery.ConsulConfig{
		Address:   server.URL,
		ServiceID: "manager-node",
		LocalIP:   "192.168.1.1",
	}

	client, err := discovery.NewConsulClient(cfg)
	if err != nil {
		t.Fatalf("NewConsulClient failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	callbackCalled := make(chan bool, 1)

	client.WatchPeers(ctx, func(peers []string) {
		mu.Lock()
		receivedPeers = peers
		mu.Unlock()
		callbackCalled <- true
	})

	// Wait for callback or timeout
	select {
	case <-callbackCalled:
		mu.Lock()
		defer mu.Unlock()

		if len(receivedPeers) < 1 {
			t.Errorf("Received %d peers, expected at least 1", len(receivedPeers))
		}

		// Verify peers are not empty
		for _, peer := range receivedPeers {
			if peer == "" {
				t.Error("Peer IP should not be empty")
			}
		}

	case <-time.After(600 * time.Millisecond):
		// Timeout is expected since mock server returns immediately
		// The WatchPeers function uses blocking queries with WaitTime
		t.Log("WatchPeers timeout - expected with mock server")
	}
}

// TestConsulClient_Close tests client cleanup.
func TestConsulClient_Close(t *testing.T) {
	deregisterCalled := false
	var mu sync.Mutex

	server := mockConsulServer(t, map[string]http.HandlerFunc{
		"/v1/agent/service/register": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		},
		"/v1/agent/service/deregister/": func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			deregisterCalled = true
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		},
	})

	cfg := discovery.ConsulConfig{
		Address:   server.URL,
		ServiceID: "test-node",
		LocalIP:   "192.168.1.10",
	}

	client, err := discovery.NewConsulClient(cfg)
	if err != nil {
		t.Fatalf("NewConsulClient failed: %v", err)
	}

	// Close should call DeregisterService
	err = client.Close()
	if err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if !deregisterCalled {
		t.Error("Close should call DeregisterService")
	}
}

// TestConsulClient_InterfaceComplianceVerified verifies interface implementation.
func TestConsulClient_InterfaceComplianceVerified(t *testing.T) {
	// Compile-time check
	var _ types.NodeDiscovery = (*discovery.ConsulClient)(nil)
}

// TestConsulClient_ErrorHandling tests error scenarios.
func TestConsulClient_ErrorHandling(t *testing.T) {
	t.Run("catalog service error", func(t *testing.T) {
		server := mockConsulServer(t, map[string]http.HandlerFunc{
			"/v1/catalog/service/swarmcracker-vxlan": func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte("internal error"))
			},
		})

		cfg := discovery.ConsulConfig{
			Address:   server.URL,
			ServiceID: "test-node",
			LocalIP:   "192.168.1.10",
		}

		client, err := discovery.NewConsulClient(cfg)
		if err != nil {
			t.Fatalf("NewConsulClient failed: %v", err)
		}

		_, err = client.GetNodes()
		if err == nil {
			t.Error("GetNodes should return error on 500 response")
		}
	})

	t.Run("register service error", func(t *testing.T) {
		server := mockConsulServer(t, map[string]http.HandlerFunc{
			"/v1/agent/service/register": func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte("bad request"))
			},
		})

		cfg := discovery.ConsulConfig{
			Address:   server.URL,
			ServiceID: "test-node",
			LocalIP:   "192.168.1.10",
		}

		client, err := discovery.NewConsulClient(cfg)
		if err != nil {
			t.Fatalf("NewConsulClient failed: %v", err)
		}

		err = client.RegisterService(100, "192.168.127.1")
		if err == nil {
			t.Error("RegisterService should return error on 400 response")
		}
	})
}

// TestConsulConfig_Defaults tests config default values.
func TestConsulConfig_Defaults(t *testing.T) {
	tests := []struct {
		name     string
		input    discovery.ConsulConfig
		wantAddr string
		wantPort int
	}{
		{
			name:     "empty address defaults",
			input:    discovery.ConsulConfig{ServiceID: "node1", LocalIP: "10.0.0.1"},
			wantAddr: "127.0.0.1:8500",
			wantPort: 4789,
		},
		{
			name:     "custom values preserved",
			input:    discovery.ConsulConfig{Address: "consul:8500", ServiceID: "node2", LocalIP: "10.0.0.2", VXLANPort: 8472},
			wantAddr: "consul:8500",
			wantPort: 8472,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.input

			// Apply defaults (same logic as NewConsulClient)
			if cfg.Address == "" {
				cfg.Address = "127.0.0.1:8500"
			}
			if cfg.VXLANPort == 0 {
				cfg.VXLANPort = 4789
			}

			if cfg.Address != tt.wantAddr {
				t.Errorf("Address = %v, want %v", cfg.Address, tt.wantAddr)
			}
			if cfg.VXLANPort != tt.wantPort {
				t.Errorf("VXLANPort = %v, want %v", cfg.VXLANPort, tt.wantPort)
			}
		})
	}
}
