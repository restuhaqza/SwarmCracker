package v1

// Network is a user-defined network. SwarmKit's scope is a constant in this
// deployment, so it is deliberately absent from the contract.
type Network struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Revision   string            `json:"revision"`
	CreatedAt  string            `json:"createdAt,omitempty"`
	Driver     string            `json:"driver,omitempty"`
	IPAM       *NetworkIPAM      `json:"ipam,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	Attachable bool              `json:"attachable,omitempty"`
}

// NetworkIPAM is the first IPAM pool of a network.
type NetworkIPAM struct {
	Subnet  string `json:"subnet,omitempty"`
	Gateway string `json:"gateway,omitempty"`
}

// NetworkList is a paginated list of user-defined networks.
type NetworkList struct {
	SchemaVersion int       `json:"schemaVersion"`
	Kind          string    `json:"kind"`
	Items         []Network `json:"items"`
	Next          *string   `json:"next"`
	Warnings      []Warning `json:"warnings,omitempty"`
	Partial       []Partial `json:"partial,omitempty"`
}

// NewNetworkList returns a list document with a non-nil items array.
func NewNetworkList(items []Network) NetworkList {
	if items == nil {
		items = []Network{}
	}
	return NetworkList{
		SchemaVersion: SchemaVersion,
		Kind:          KindNetworkList,
		Items:         items,
		Next:          nil,
	}
}
