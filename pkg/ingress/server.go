package ingress

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"time"
)

// TableProvider supplies the routing table to serve.
type TableProvider interface {
	Table() Table
}

// NewTableServer returns an HTTP server exposing the routing table at
// GET /v1/ingress. It is meant to be started with ListenAndServeTLS("", "")
// using the provided (mutual-TLS) configuration, so only cluster nodes can
// read it.
func NewTableServer(provider TableProvider, tlsConfig *tls.Config, addr string) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/ingress", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(provider.Table())
	})
	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: 5 * time.Second,
	}
}
