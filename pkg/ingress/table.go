package ingress

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/restuhaqza/swarmcracker/pkg/network"
)

// TablePort is one published port in the wire form of the routing table.
type TablePort struct {
	Protocol      string `json:"protocol"`
	PublishedPort uint32 `json:"published_port"`
	TargetPort    uint32 `json:"target_port"`
}

// TableRoute is the load-balanced state for one service in the wire form.
type TableRoute struct {
	ServiceID string      `json:"service_id"`
	Ports     []TablePort `json:"ports"`
	Backends  []string    `json:"backends"`
}

// Table is the routing table a manager serves and nodes consume. Revision is a
// short content hash so consumers can cheaply detect changes.
type Table struct {
	Revision string       `json:"revision"`
	Routes   []TableRoute `json:"routes"`
}

// TableFromRoutes converts the internal routes into their wire form, computing
// a stable revision from the route content.
func TableFromRoutes(routes []network.IngressRoute) Table {
	t := Table{Routes: make([]TableRoute, 0, len(routes))}
	for _, r := range routes {
		ports := make([]TablePort, 0, len(r.Ports))
		for _, p := range r.Ports {
			ports = append(ports, TablePort{
				Protocol:      p.Protocol,
				PublishedPort: p.PublishedPort,
				TargetPort:    p.TargetPort,
			})
		}
		t.Routes = append(t.Routes, TableRoute{
			ServiceID: r.ServiceID,
			Ports:     ports,
			Backends:  append([]string(nil), r.Backends...),
		})
	}
	if data, err := json.Marshal(t.Routes); err == nil {
		sum := sha256.Sum256(data)
		t.Revision = hex.EncodeToString(sum[:8])
	}
	return t
}

// ToRoutes converts the wire form back into internal routes.
func (t Table) ToRoutes() []network.IngressRoute {
	routes := make([]network.IngressRoute, 0, len(t.Routes))
	for _, r := range t.Routes {
		ports := make([]network.IngressPort, 0, len(r.Ports))
		for _, p := range r.Ports {
			ports = append(ports, network.IngressPort{
				Protocol:      p.Protocol,
				PublishedPort: p.PublishedPort,
				TargetPort:    p.TargetPort,
			})
		}
		routes = append(routes, network.IngressRoute{
			ServiceID: r.ServiceID,
			Ports:     ports,
			Backends:  append([]string(nil), r.Backends...),
		})
	}
	return routes
}
