package ingress

import (
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/network"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sampleRoutes() []network.IngressRoute {
	return []network.IngressRoute{
		{
			ServiceID: "svc1",
			Ports:     []network.IngressPort{{Protocol: "tcp", PublishedPort: 8080, TargetPort: 80}},
			Backends:  []string{"192.168.127.5", "192.168.127.6"},
		},
		{
			ServiceID: "svc2",
			Ports:     []network.IngressPort{{Protocol: "udp", PublishedPort: 53, TargetPort: 53}},
			Backends:  []string{"192.168.127.7"},
		},
	}
}

func TestTable_RoundTrip(t *testing.T) {
	routes := sampleRoutes()
	table := TableFromRoutes(routes)
	require.NotEmpty(t, table.Revision, "a content revision is computed")
	require.Len(t, table.Routes, 2)

	assert.Equal(t, routes, table.ToRoutes())
}

func TestTable_RevisionTracksContent(t *testing.T) {
	base := TableFromRoutes(sampleRoutes())

	same := TableFromRoutes(sampleRoutes())
	assert.Equal(t, base.Revision, same.Revision, "identical content yields the same revision")

	changed := sampleRoutes()
	changed[0].Backends = []string{"192.168.127.5"}
	other := TableFromRoutes(changed)
	assert.NotEqual(t, base.Revision, other.Revision, "changed content yields a new revision")
}

func TestTable_Empty(t *testing.T) {
	table := TableFromRoutes(nil)
	assert.Empty(t, table.Routes)
	assert.Empty(t, table.ToRoutes())
}
