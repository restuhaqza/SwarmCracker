package v1

// Config is config metadata. The payload is never part of a read document:
// config values are only available through the configs:read-values capability,
// which phase 0 does not issue.
type Config struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Revision  string            `json:"revision"`
	CreatedAt string            `json:"createdAt,omitempty"`
	UpdatedAt string            `json:"updatedAt,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// ConfigList is a paginated list of configs.
type ConfigList struct {
	SchemaVersion int       `json:"schemaVersion"`
	Kind          string    `json:"kind"`
	Items         []Config  `json:"items"`
	Next          *string   `json:"next"`
	Warnings      []Warning `json:"warnings,omitempty"`
	Partial       []Partial `json:"partial,omitempty"`
}

// NewConfigList returns a list document with a non-nil items array.
func NewConfigList(items []Config) ConfigList {
	if items == nil {
		items = []Config{}
	}
	return ConfigList{
		SchemaVersion: SchemaVersion,
		Kind:          KindConfigList,
		Items:         items,
		Next:          nil,
	}
}
