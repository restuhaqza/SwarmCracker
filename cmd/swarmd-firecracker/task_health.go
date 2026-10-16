package main

import (
	"encoding/json"
	"net/http"

	"github.com/restuhaqza/swarmcracker/pkg/runtime"
)

// taskHealthHandler serves GET runtime.TaskHealthPath: the live health of
// every task this node currently hosts, read from the per-task sidecars the
// executor publishes. Tasks that declare no healthcheck are absent (they never
// publish), and an unreadable sidecar is skipped rather than failing the
// response.
//
// Trust model: this exposes health only — state, failing streak, the probe
// command and its last bounded output. There is no auth and no secret; the
// bind address (--health-addr, default 127.0.0.1:8080) is the boundary. Bind it
// on the cluster network only when the cluster-wide HEALTH column is wanted.
func taskHealthHandler(socketDir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		resp := runtime.TaskHealthResponse{Tasks: runtime.CollectTaskHealth(socketDir)}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			http.Error(w, "failed to encode response", http.StatusInternalServerError)
		}
	})
}
