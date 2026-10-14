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

// maxTaskReportBytes bounds a node's task report. A few thousand tasks with a
// handful of addresses each is well under it, and the cap keeps a misbehaving
// node from streaming the manager out of memory.
const maxTaskReportBytes = 1 << 20

// NewTableServer returns an HTTP server exposing the routing table at
// GET /v1/ingress and, when reports is non-nil, accepting node task reports at
// POST /v1/ingress/report. It is meant to be started with
// ListenAndServeTLS("", "") using the provided (mutual-TLS) configuration, so
// only cluster nodes can read the table or report into it.
func NewTableServer(provider TableProvider, reports *ReportStore, tlsConfig *tls.Config, addr string) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/ingress", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(provider.Table())
	})
	if reports != nil {
		mux.HandleFunc("/v1/ingress/report", reportHandler(reports))
	}
	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: 5 * time.Second,
	}
}

// reportHandler stores a node's task report. The identity comes from the client
// certificate rather than the body, so a node cannot report addresses on
// another node's behalf.
func reportHandler(reports *ReportStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		nodeID := peerNodeID(r)
		if nodeID == "" {
			http.Error(w, "client certificate required", http.StatusUnauthorized)
			return
		}

		var rep TaskReport
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTaskReportBytes)).Decode(&rep); err != nil {
			http.Error(w, "invalid report", http.StatusBadRequest)
			return
		}
		rep.NodeID = nodeID
		reports.Put(rep)
		w.WriteHeader(http.StatusNoContent)
	}
}

// peerNodeID returns the identity carried by the client's certificate. Cluster
// node certificates identify the node, which is what makes a report
// attributable.
func peerNodeID(r *http.Request) string {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return ""
	}
	return r.TLS.PeerCertificates[0].Subject.CommonName
}
