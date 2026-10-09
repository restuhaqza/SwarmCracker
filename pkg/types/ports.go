package types

import (
	"fmt"
	"strconv"
	"strings"
)

// PublishLabel is the service label that carries published-port mappings from
// the CLI to the executor. SwarmKit copies service annotations onto each task's
// ServiceAnnotations (api/objects.pb.go: "a direct copy of the service name and
// labels"), so the agent can program host port forwarding without needing to
// read the service object.
const PublishLabel = "swarmcracker.publish"

// PublishModeLabel is the service label that carries the publish mode for the
// service's ports. Like PublishLabel it is copied onto every task's
// ServiceAnnotations. The executor reads it to decide whether it owns the host
// forwarding (host mode) or leaves it to the manager-side ingress datapath
// (ingress mode).
const PublishModeLabel = "swarmcracker.publish-mode"

// Publish modes. Ingress publishes on the cluster (load-balanced across
// replicas) and is the default, matching Docker Swarm. Host forwards a concrete
// host port per replica on the node running it.
const (
	PublishModeIngress = "ingress"
	PublishModeHost    = "host"
)

// NormalizePublishMode validates and canonicalizes a publish mode. An empty
// value defaults to ingress.
func NormalizePublishMode(mode string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", PublishModeIngress:
		return PublishModeIngress, nil
	case PublishModeHost:
		return PublishModeHost, nil
	default:
		return "", fmt.Errorf("invalid publish mode %q (want ingress or host)", mode)
	}
}

// IsIngressPublishMode reports whether the given (possibly empty) label value
// selects ingress mode. An empty value means host: services created before the
// publish-mode label existed used per-replica host forwarding, so treating a
// missing label as ingress would silently break them.
func IsIngressPublishMode(mode string) bool {
	return strings.EqualFold(strings.TrimSpace(mode), PublishModeIngress)
}

// PublishedPort describes a host port forwarded to a guest port.
//
// For example, "--publish 8080:80" maps host port 8080 (TCP) to guest port 80.
type PublishedPort struct {
	// Protocol is "tcp" or "udp".
	Protocol string
	// TargetPort is the port inside the microVM.
	TargetPort uint32
	// PublishedPort is the port on the host. It is always set for the host
	// publishing mode; ephemeral allocation is not supported yet.
	PublishedPort uint32
}

// String renders the mapping in the canonical "[host:]container/proto" form.
func (p PublishedPort) String() string {
	return fmt.Sprintf("%d:%d/%s", p.PublishedPort, p.TargetPort, p.Protocol)
}

// ParsePublishSpec parses a list of --publish values of the form
// "[host:]container[/tcp|udp]". The host port is required: host publishing
// forwards a concrete host port, and ephemeral allocation is not supported yet.
func ParsePublishSpec(specs []string) ([]PublishedPort, error) {
	ports := make([]PublishedPort, 0, len(specs))
	seen := make(map[string]struct{}, len(specs))
	for _, raw := range specs {
		p, err := parsePublishOne(raw)
		if err != nil {
			return nil, err
		}
		key := fmt.Sprintf("%s/%d", p.Protocol, p.PublishedPort)
		if _, dup := seen[key]; dup {
			return nil, fmt.Errorf("duplicate publish mapping for host port %d/%s", p.PublishedPort, p.Protocol)
		}
		seen[key] = struct{}{}
		ports = append(ports, p)
	}
	return ports, nil
}

func parsePublishOne(raw string) (PublishedPort, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return PublishedPort{}, fmt.Errorf("empty publish spec")
	}

	proto := "tcp"
	if i := strings.LastIndex(s, "/"); i >= 0 {
		proto = strings.ToLower(s[i+1:])
		s = s[:i]
	}
	switch proto {
	case "tcp", "udp":
	default:
		return PublishedPort{}, fmt.Errorf("invalid protocol %q in %q (want tcp or udp)", proto, raw)
	}

	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return PublishedPort{}, fmt.Errorf("invalid publish spec %q (want [host:]container[/proto])", raw)
	}
	host, container := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	if host == "" {
		return PublishedPort{}, fmt.Errorf("host port is required in %q (ephemeral publishing is not supported yet)", raw)
	}

	hport, err := parsePort(host)
	if err != nil {
		return PublishedPort{}, fmt.Errorf("invalid host port in %q: %w", raw, err)
	}
	cport, err := parsePort(container)
	if err != nil {
		return PublishedPort{}, fmt.Errorf("invalid container port in %q: %w", raw, err)
	}
	return PublishedPort{Protocol: proto, TargetPort: cport, PublishedPort: hport}, nil
}

func parsePort(s string) (uint32, error) {
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("not a number: %q", s)
	}
	if n == 0 || n > 65535 {
		return 0, fmt.Errorf("port out of range: %d", n)
	}
	return uint32(n), nil
}

// FormatPublishLabel encodes ports into the value stored under PublishLabel.
func FormatPublishLabel(ports []PublishedPort) string {
	parts := make([]string, 0, len(ports))
	for _, p := range ports {
		parts = append(parts, p.String())
	}
	return strings.Join(parts, ",")
}

// ParsePublishLabel decodes a label value produced by FormatPublishLabel.
// An empty value yields a nil slice and no error.
func ParsePublishLabel(value string) ([]PublishedPort, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	return ParsePublishSpec(strings.Split(value, ","))
}
