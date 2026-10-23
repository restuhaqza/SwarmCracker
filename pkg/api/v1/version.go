package v1

// VersionInfo is the feature-detection document. A client reads it to learn
// which API versions the server speaks, what it is running and which optional
// capabilities the deployment exposes.
type VersionInfo struct {
	SchemaVersion       int      `json:"schemaVersion"`
	Kind                string   `json:"kind"`
	APIVersions         []string `json:"apiVersions"`
	ServerVersion       string   `json:"serverVersion"`
	GRPCProtocolVersion string   `json:"grpcProtocolVersion"`
	ClusterID           string   `json:"clusterId,omitempty"`
	Capabilities        []string `json:"capabilities"`
}

// NewVersionInfo returns a version document whose arrays are never nil: an
// absent list is an empty list on the wire, never null.
func NewVersionInfo(apiVersions []string, serverVersion, grpcVersion, clusterID string, capabilities []string) VersionInfo {
	if apiVersions == nil {
		apiVersions = []string{}
	}
	if capabilities == nil {
		capabilities = []string{}
	}
	return VersionInfo{
		SchemaVersion:       SchemaVersion,
		Kind:                KindVersionInfo,
		APIVersions:         apiVersions,
		ServerVersion:       serverVersion,
		GRPCProtocolVersion: grpcVersion,
		ClusterID:           clusterID,
		Capabilities:        capabilities,
	}
}
