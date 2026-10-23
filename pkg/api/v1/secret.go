package v1

// Secret is secret metadata. There is no data field on any read path, ever.
type Secret struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Revision  string            `json:"revision"`
	CreatedAt string            `json:"createdAt,omitempty"`
	UpdatedAt string            `json:"updatedAt,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
	Driver    string            `json:"driver,omitempty"`
}

// SecretList is a paginated list of secrets.
type SecretList struct {
	SchemaVersion int       `json:"schemaVersion"`
	Kind          string    `json:"kind"`
	Items         []Secret  `json:"items"`
	Next          *string   `json:"next"`
	Warnings      []Warning `json:"warnings,omitempty"`
	Partial       []Partial `json:"partial,omitempty"`
}

// NewSecretList returns a list document with a non-nil items array.
func NewSecretList(items []Secret) SecretList {
	if items == nil {
		items = []Secret{}
	}
	return SecretList{
		SchemaVersion: SchemaVersion,
		Kind:          KindSecretList,
		Items:         items,
		Next:          nil,
	}
}
