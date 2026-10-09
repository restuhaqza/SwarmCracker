package ingress

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/rs/zerolog/log"
)

// TableClient fetches the routing table from a manager and programs the local
// load balancer from it.
type TableClient struct {
	url      string
	client   *http.Client
	interval time.Duration
}

// NewTableClient creates a client for the given table URL. interval is clamped
// to a sane minimum.
func NewTableClient(url string, tlsConfig *tls.Config, interval time.Duration) *TableClient {
	if interval < time.Second {
		interval = 2 * time.Second
	}
	return &TableClient{
		url: url,
		client: &http.Client{
			Timeout:   10 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsConfig},
		},
		interval: interval,
	}
}

// Fetch retrieves the current routing table.
func (c *TableClient) Fetch(ctx context.Context) (Table, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return Table{}, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return Table{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return Table{}, fmt.Errorf("ingress table: unexpected status %d", resp.StatusCode)
	}
	var t Table
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return Table{}, err
	}
	return t, nil
}

// Run fetches the table on a timer and programs the load balancer from it until
// the context is cancelled. On fetch failure it keeps the last programmed rules
// rather than dropping them.
func (c *TableClient) Run(ctx context.Context, lb LoadBalancer) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	fetch := func() {
		table, err := c.Fetch(ctx)
		if err != nil {
			log.Warn().Err(err).Msg("Ingress table fetch failed; keeping last rules")
			return
		}
		if err := lb.ProgramIngress(table.ToRoutes()); err != nil {
			log.Warn().Err(err).Msg("Failed to program ingress rules from table")
		}
	}

	fetch()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fetch()
		}
	}
}
