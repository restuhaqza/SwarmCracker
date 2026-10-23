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

// SecretDocument is a single-resource Secret document. It carries metadata
// only; the payload has no path into a read document.
type SecretDocument struct {
	SchemaVersion int    `json:"schemaVersion"`
	Kind          string `json:"kind"`
	Secret
}

// NewSecretDocument wraps a Secret in the v1 envelope.
func NewSecretDocument(secret Secret) SecretDocument {
	return SecretDocument{SchemaVersion: SchemaVersion, Kind: KindSecret, Secret: secret}
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
