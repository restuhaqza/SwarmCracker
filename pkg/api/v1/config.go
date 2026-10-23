package v1

// Config is config metadata.
//
// Data holds the config payload and is only present for an identity carrying
// configs:read-values. A plain configs:read token must never receive it, and the
// field is omitted (not empty) when no value getter populated it.
type Config struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Revision  string            `json:"revision"`
	CreatedAt string            `json:"createdAt,omitempty"`
	UpdatedAt string            `json:"updatedAt,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
	Data      *string           `json:"data,omitempty"`
}

// ConfigDocument is a single-resource Config document.
type ConfigDocument struct {
	SchemaVersion int    `json:"schemaVersion"`
	Kind          string `json:"kind"`
	Config
}

// NewConfigDocument wraps a Config in the v1 envelope.
func NewConfigDocument(config Config) ConfigDocument {
	return ConfigDocument{SchemaVersion: SchemaVersion, Kind: KindConfig, Config: config}
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
