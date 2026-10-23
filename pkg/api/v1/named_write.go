package v1

// This file owns the phase-2 write bodies for the resources that are not
// services: config, secret and network. They are separate from the read types
// because a write body carries data the read document must never echo (secret
// data) or gates behind configs:read-values (config data).
//
// There is no ConfigPatch, SecretPatch or NodePatch type here: a PATCH body is a
// raw RFC 7386 merge patch, parsed and validated by pkg/client, which is the only
// layer that can see the current object.

// ConfigWrite is the body of POST /v1/configs.
//
// Data is the payload as UTF-8 text. Binary payloads are rejected with
// 400 invalid and details.reason "binary_not_supported".
type ConfigWrite struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	Data   string            `json:"data,omitempty"` // UTF-8 payload
}

// SecretWrite is the body of POST /v1/secrets.
//
// Exactly one of Data (base64) or Driver must be present. Neither a read
// document nor a response to a write ever carries the payload.
type SecretWrite struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	Data   string            `json:"data,omitempty"` // base64
	Driver *SecretDriver     `json:"driver,omitempty"`
}

// SecretDriver names an external secret store. Options is passed to the driver
// verbatim.
type SecretDriver struct {
	Name    string            `json:"name"`
	Options map[string]string `json:"options,omitempty"`
}

// NetworkWrite is the body of POST /v1/networks. Driver defaults to "overlay".
// There is no update: driver, IPAM and labels are immutable after create.
type NetworkWrite struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver,omitempty"` // default "overlay"
	IPAM       *NetworkIPAM      `json:"ipam,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	Attachable bool              `json:"attachable,omitempty"`
}
