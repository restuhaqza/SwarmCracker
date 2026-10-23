// This file owns the phase-2 write routes for configs, secrets, networks and
// nodes.
//
// The split is the same as services_write.go: this layer owns the request media
// type, the body cap, the strict decode, the presence of If-Match and the status
// a typed failure maps to. Reference resolution, validation, merge patches and
// revision comparison live in pkg/client, the only place that can see
// Meta.Version.Index. There is deliberately no second SwarmKit mapping here.
package apihttp

import (
	"net/http"
	"strings"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// createOnlyIfNoneMatch reads the create-only marker. Only "*" is meaningful;
// the header never turns POST into an upsert.
func createOnlyIfNoneMatch(r *http.Request) (string, error) {
	value := strings.TrimSpace(r.Header.Get("If-None-Match"))
	if value != "" && value != "*" {
		return "", invalidField("If-None-Match", `must be exactly "*" on create`)
	}
	return value, nil
}

// binaryNotSupported is the contract's refusal of a config payload that is not
// UTF-8 text (contract §5.6). It is produced here because the refusal has to
// happen before the JSON decoder can replace the bytes; pkg/client produces the
// same document for a direct Go call.
func binaryNotSupported(path string) error {
	return output.NewErrorf(output.CodeInvalid,
		"%s must be UTF-8 text; binary payloads are not supported in v1", path).
		WithDetails(map[string]any{
			"reason": "binary_not_supported",
			"fields": map[string]any{path: "must be UTF-8 text"},
		})
}

// createConfig serves POST /v1/configs.
func (s *Server) createConfig(w http.ResponseWriter, r *http.Request) {
	if status, err := requireContentType(r, MediaType); err != nil {
		s.writeErrorStatus(w, r, status, err)
		return
	}
	ifNoneMatch, err := createOnlyIfNoneMatch(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	var body v1.ConfigWrite
	if status, err := readJSONWriteUTF8(w, r, &body); err != nil {
		s.writeErrorStatus(w, r, status, err)
		return
	}

	config, err := s.cfg.Client.CreateConfig(r.Context(), body,
		v1.WriteOptions{IfNoneMatch: ifNoneMatch})
	if err != nil {
		s.writeNamedAudit(r, "config.create",
			namedAuditResource(v1.KindConfig, "", "", body.Name), "error")
		s.writeWriteError(w, r, err)
		return
	}

	s.writeNamedAudit(r, "config.create",
		namedAuditResource(v1.KindConfig, config.ID, config.Name, body.Name), "ok")
	s.writeJSONWithETag(w, r, http.StatusCreated,
		v1.NewConfigDocument(configForResponse(r, config)), revisionETag(config.Revision))
}

// updateConfig serves PATCH /v1/configs/{id}. The body is an RFC 7386 merge
// patch; pkg/client accepts labels only.
func (s *Server) updateConfig(w http.ResponseWriter, r *http.Request) {
	if status, err := requireContentType(r, v1.MergePatchMediaType); err != nil {
		s.writeErrorStatus(w, r, status, err)
		return
	}

	ifMatch := strings.TrimSpace(r.Header.Get("If-Match"))
	if ifMatch == "" {
		s.writeWriteError(w, r, output.PreconditionRequired())
		return
	}

	patch, status, err := readWriteBody(w, r)
	if err != nil {
		s.writeErrorStatus(w, r, status, err)
		return
	}

	ref := r.PathValue("id")
	config, err := s.cfg.Client.UpdateConfig(r.Context(), ref, patch, v1.WriteOptions{IfMatch: ifMatch})
	if err != nil {
		s.writeNamedAudit(r, "config.update",
			namedAuditResource(v1.KindConfig, "", "", ref), "error")
		s.writeWriteError(w, r, err)
		return
	}

	s.writeNamedAudit(r, "config.update",
		namedAuditResource(v1.KindConfig, config.ID, config.Name, ref), "ok")
	s.writeJSONWithETag(w, r, http.StatusOK,
		v1.NewConfigDocument(configForResponse(r, config)), revisionETag(config.Revision))
}

// deleteConfig serves DELETE /v1/configs/{id}.
func (s *Server) deleteConfig(w http.ResponseWriter, r *http.Request) {
	ignoreMissing, status, err := parseIgnoreMissing(r.URL.Query())
	if err != nil {
		s.writeErrorStatus(w, r, status, err)
		return
	}

	ref := r.PathValue("id")
	config, err := s.cfg.Client.DeleteConfig(r.Context(), ref, v1.WriteOptions{
		IfMatch:       strings.TrimSpace(r.Header.Get("If-Match")),
		IgnoreMissing: ignoreMissing,
	})
	if err != nil {
		s.writeNamedAudit(r, "config.delete",
			namedAuditResource(v1.KindConfig, "", "", ref), "error")
		s.writeWriteError(w, r, err)
		return
	}

	s.writeNamedAudit(r, "config.delete",
		namedAuditResource(v1.KindConfig, config.ID, config.Name, ref), "ok")
	w.WriteHeader(http.StatusNoContent)
}

// configForResponse withholds the payload from an identity without
// configs:read-values. That includes the response to a write: a caller may have
// written a value without holding the grant that reads values back.
func configForResponse(r *http.Request, config v1.Config) v1.Config {
	if !canReadConfigValues(r) {
		config.Data = nil
	}
	return config
}

// createSecret serves POST /v1/secrets.
func (s *Server) createSecret(w http.ResponseWriter, r *http.Request) {
	if status, err := requireContentType(r, MediaType); err != nil {
		s.writeErrorStatus(w, r, status, err)
		return
	}
	ifNoneMatch, err := createOnlyIfNoneMatch(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	var body v1.SecretWrite
	if status, err := readJSONWrite(w, r, &body); err != nil {
		s.writeErrorStatus(w, r, status, err)
		return
	}

	secret, err := s.cfg.Client.CreateSecret(r.Context(), body,
		v1.WriteOptions{IfNoneMatch: ifNoneMatch})
	if err != nil {
		s.writeNamedAudit(r, "secret.create",
			namedAuditResource(v1.KindSecret, "", "", body.Name), "error")
		s.writeWriteError(w, r, err)
		return
	}

	s.writeNamedAudit(r, "secret.create",
		namedAuditResource(v1.KindSecret, secret.ID, secret.Name, body.Name), "ok")
	s.writeJSONWithETag(w, r, http.StatusCreated,
		v1.NewSecretDocument(secret), revisionETag(secret.Revision))
}

// updateSecret serves PATCH /v1/secrets/{id}. The body is a merge patch;
// pkg/client accepts labels only.
func (s *Server) updateSecret(w http.ResponseWriter, r *http.Request) {
	if status, err := requireContentType(r, v1.MergePatchMediaType); err != nil {
		s.writeErrorStatus(w, r, status, err)
		return
	}

	ifMatch := strings.TrimSpace(r.Header.Get("If-Match"))
	if ifMatch == "" {
		s.writeWriteError(w, r, output.PreconditionRequired())
		return
	}

	patch, status, err := readWriteBody(w, r)
	if err != nil {
		s.writeErrorStatus(w, r, status, err)
		return
	}

	ref := r.PathValue("id")
	secret, err := s.cfg.Client.UpdateSecret(r.Context(), ref, patch, v1.WriteOptions{IfMatch: ifMatch})
	if err != nil {
		s.writeNamedAudit(r, "secret.update",
			namedAuditResource(v1.KindSecret, "", "", ref), "error")
		s.writeWriteError(w, r, err)
		return
	}

	s.writeNamedAudit(r, "secret.update",
		namedAuditResource(v1.KindSecret, secret.ID, secret.Name, ref), "ok")
	s.writeJSONWithETag(w, r, http.StatusOK,
		v1.NewSecretDocument(secret), revisionETag(secret.Revision))
}

// deleteSecret serves DELETE /v1/secrets/{id}.
func (s *Server) deleteSecret(w http.ResponseWriter, r *http.Request) {
	ignoreMissing, status, err := parseIgnoreMissing(r.URL.Query())
	if err != nil {
		s.writeErrorStatus(w, r, status, err)
		return
	}

	ref := r.PathValue("id")
	secret, err := s.cfg.Client.DeleteSecret(r.Context(), ref, v1.WriteOptions{
		IfMatch:       strings.TrimSpace(r.Header.Get("If-Match")),
		IgnoreMissing: ignoreMissing,
	})
	if err != nil {
		s.writeNamedAudit(r, "secret.delete",
			namedAuditResource(v1.KindSecret, "", "", ref), "error")
		s.writeWriteError(w, r, err)
		return
	}

	s.writeNamedAudit(r, "secret.delete",
		namedAuditResource(v1.KindSecret, secret.ID, secret.Name, ref), "ok")
	w.WriteHeader(http.StatusNoContent)
}

// createNetwork serves POST /v1/networks.
func (s *Server) createNetwork(w http.ResponseWriter, r *http.Request) {
	if status, err := requireContentType(r, MediaType); err != nil {
		s.writeErrorStatus(w, r, status, err)
		return
	}
	ifNoneMatch, err := createOnlyIfNoneMatch(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	var body v1.NetworkWrite
	if status, err := readJSONWrite(w, r, &body); err != nil {
		s.writeErrorStatus(w, r, status, err)
		return
	}

	network, err := s.cfg.Client.CreateNetwork(r.Context(), body,
		v1.WriteOptions{IfNoneMatch: ifNoneMatch})
	if err != nil {
		s.writeNamedAudit(r, "network.create",
			namedAuditResource(v1.KindNetwork, "", "", body.Name), "error")
		s.writeWriteError(w, r, err)
		return
	}

	s.writeNamedAudit(r, "network.create",
		namedAuditResource(v1.KindNetwork, network.ID, network.Name, body.Name), "ok")
	s.writeJSONWithETag(w, r, http.StatusCreated,
		v1.NewNetworkDocument(network), revisionETag(network.Revision))
}

// deleteNetwork serves DELETE /v1/networks/{id}. There is no network update, so
// PATCH and PUT are answered by the route table's typed 405 (Allow: GET, DELETE).
func (s *Server) deleteNetwork(w http.ResponseWriter, r *http.Request) {
	ignoreMissing, status, err := parseIgnoreMissing(r.URL.Query())
	if err != nil {
		s.writeErrorStatus(w, r, status, err)
		return
	}

	ref := r.PathValue("id")
	network, err := s.cfg.Client.DeleteNetwork(r.Context(), ref, v1.WriteOptions{
		IfMatch:       strings.TrimSpace(r.Header.Get("If-Match")),
		IgnoreMissing: ignoreMissing,
	})
	if err != nil {
		s.writeNamedAudit(r, "network.delete",
			namedAuditResource(v1.KindNetwork, "", "", ref), "error")
		s.writeWriteError(w, r, err)
		return
	}

	s.writeNamedAudit(r, "network.delete",
		namedAuditResource(v1.KindNetwork, network.ID, network.Name, ref), "ok")
	w.WriteHeader(http.StatusNoContent)
}

// updateNode serves PATCH /v1/nodes/{id}. The body is a merge patch; pkg/client
// accepts availability and labels only.
func (s *Server) updateNode(w http.ResponseWriter, r *http.Request) {
	if status, err := requireContentType(r, v1.MergePatchMediaType); err != nil {
		s.writeErrorStatus(w, r, status, err)
		return
	}

	ifMatch := strings.TrimSpace(r.Header.Get("If-Match"))
	if ifMatch == "" {
		s.writeWriteError(w, r, output.PreconditionRequired())
		return
	}

	patch, status, err := readWriteBody(w, r)
	if err != nil {
		s.writeErrorStatus(w, r, status, err)
		return
	}

	ref := r.PathValue("id")
	node, err := s.cfg.Client.UpdateNode(r.Context(), ref, patch, v1.WriteOptions{IfMatch: ifMatch})
	if err != nil {
		s.writeNamedAudit(r, "node.update",
			namedAuditResource(v1.KindNode, "", "", ref), "error")
		s.writeWriteError(w, r, err)
		return
	}

	s.writeNamedAudit(r, "node.update",
		namedAuditResource(v1.KindNode, node.ID, node.Hostname, ref), "ok")
	s.writeJSONWithETag(w, r, http.StatusOK,
		v1.NewNodeDocument(node), revisionETag(node.Revision))
}

// namedAuditResource names the object a write touched. A failed create never
// produced an ID and a delete miss has no document, so the reference the caller
// used is the honest name.
func namedAuditResource(kind, id, name, fallbackName string) v1.AuditResource {
	resource := v1.AuditResource{Kind: kind, ID: id, Name: name}
	if resource.ID == "" && resource.Name == "" {
		resource.Name = fallbackName
	}
	return resource
}

// writeNamedAudit appends one audit record for a config, secret, network or node
// mutation. The mutation has already committed, so a failed append must not turn
// a successful write into an error: the caller would retry a create that already
// happened.
func (s *Server) writeNamedAudit(r *http.Request, action string, resource v1.AuditResource, result string) {
	_ = s.recordAudit(r, action, resource, result)
}
