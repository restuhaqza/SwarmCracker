// This file owns the resource handlers. Each one is intentionally thin: call the
// Client, apply label selectors, page the result, wrap it in the v1 document and
// let the shared response helpers do the rest.
package apihttp

import (
	"net/http"
	"strings"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

func (s *Server) getVersion(w http.ResponseWriter, r *http.Request) {
	clusterID := s.cfg.ClusterID
	if clusterID == "" {
		// Best effort: a version probe must answer even when the manager has
		// lost contact with its own cluster.
		if id, err := s.cfg.Client.ClusterID(r.Context()); err == nil {
			clusterID = id
		}
	}

	s.writeJSON(w, r, http.StatusOK, v1.NewVersionInfo(
		[]string{"v1"},
		s.cfg.ServerVersion,
		s.cfg.GRPCVersion,
		clusterID,
		s.cfg.Capabilities,
	))
}

func (s *Server) listServices(w http.ResponseWriter, r *http.Request) {
	items, err := s.cfg.Client.ListServices(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	items, err = filterByLabels(items, r.URL.Query()["label"], func(v v1.Service) map[string]string {
		return v.Labels
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	page, next, err := paginateRequest(r, items, func(v v1.Service) (string, string) {
		return v.CreatedAt, v.ID
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	doc := v1.NewServiceList(page)
	doc.Next = next
	s.writeJSON(w, r, http.StatusOK, doc)
}

func (s *Server) getService(w http.ResponseWriter, r *http.Request) {
	service, err := s.cfg.Client.GetService(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	// The validator of a single service is its SwarmKit revision, not the body
	// hash: If-Match on a later PATCH compares revisions, so the value the read
	// hands out must be the one the write expects.
	s.writeJSONWithETag(w, r, http.StatusOK,
		v1.NewServiceDocument(service), revisionETag(service.Revision))
}

func (s *Server) listServiceTasks(w http.ResponseWriter, r *http.Request) {
	items, err := s.cfg.Client.ListServiceTasks(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	page, next, err := paginateRequest(r, items, func(v v1.Task) (string, string) {
		return v.CreatedAt, v.ID
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	doc := v1.NewTaskList(page)
	doc.Next = next
	s.writeJSON(w, r, http.StatusOK, doc)
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	items, err := s.cfg.Client.ListTasks(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	page, next, err := paginateRequest(r, items, func(v v1.Task) (string, string) {
		return v.CreatedAt, v.ID
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	doc := v1.NewTaskList(page)
	doc.Next = next
	s.writeJSON(w, r, http.StatusOK, doc)
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	task, err := s.cfg.Client.GetTask(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, v1.NewTaskDocument(task))
}

func (s *Server) listNodes(w http.ResponseWriter, r *http.Request) {
	items, err := s.cfg.Client.ListNodes(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	items, err = filterByLabels(items, r.URL.Query()["label"], func(v v1.Node) map[string]string {
		return v.Labels
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	page, next, err := paginateRequest(r, items, func(v v1.Node) (string, string) {
		return v.CreatedAt, v.ID
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	doc := v1.NewNodeList(page)
	doc.Next = next
	s.writeJSON(w, r, http.StatusOK, doc)
}

func (s *Server) getNode(w http.ResponseWriter, r *http.Request) {
	node, err := s.cfg.Client.GetNode(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	// The validator of a single resource is its revision: If-Match on a later
	// PATCH compares revisions, so the value a read hands out must be the one the
	// write expects.
	s.writeJSONWithETag(w, r, http.StatusOK,
		v1.NewNodeDocument(node), revisionETag(node.Revision))
}

func (s *Server) listNetworks(w http.ResponseWriter, r *http.Request) {
	items, err := s.cfg.Client.ListNetworks(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	items, err = filterByLabels(items, r.URL.Query()["label"], func(v v1.Network) map[string]string {
		return v.Labels
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	page, next, err := paginateRequest(r, items, func(v v1.Network) (string, string) {
		return v.CreatedAt, v.ID
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	doc := v1.NewNetworkList(page)
	doc.Next = next
	s.writeJSON(w, r, http.StatusOK, doc)
}

func (s *Server) getNetwork(w http.ResponseWriter, r *http.Request) {
	network, err := s.cfg.Client.GetNetwork(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSONWithETag(w, r, http.StatusOK,
		v1.NewNetworkDocument(network), revisionETag(network.Revision))
}

func (s *Server) listConfigs(w http.ResponseWriter, r *http.Request) {
	items, err := s.cfg.Client.ListConfigs(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	items, err = filterByLabels(items, r.URL.Query()["label"], func(v v1.Config) map[string]string {
		return v.Labels
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if !canReadConfigValues(r) {
		items = redactConfigValues(items)
	}

	page, next, err := paginateRequest(r, items, func(v v1.Config) (string, string) {
		return v.CreatedAt, v.ID
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	doc := v1.NewConfigList(page)
	doc.Next = next
	s.writeJSON(w, r, http.StatusOK, doc)
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	config, err := s.cfg.Client.GetConfig(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSONWithETag(w, r, http.StatusOK,
		v1.NewConfigDocument(configForResponse(r, config)), revisionETag(config.Revision))
}

// canReadConfigValues reports whether the caller may see config payloads. A
// plain configs:read token never may.
func canReadConfigValues(r *http.Request) bool {
	identity, ok := IdentityFrom(r.Context())
	return ok && identity.HasScope(ScopeConfigsReadValues)
}

// redactConfigValues returns a copy of items without their payloads, so a list
// response cannot leak what GetConfig would withhold. The copy keeps the
// client's slice untouched.
func redactConfigValues(items []v1.Config) []v1.Config {
	out := make([]v1.Config, len(items))
	copy(out, items)
	for i := range out {
		out[i].Data = nil
	}
	return out
}

func (s *Server) listSecrets(w http.ResponseWriter, r *http.Request) {
	items, err := s.cfg.Client.ListSecrets(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	items, err = filterByLabels(items, r.URL.Query()["label"], func(v v1.Secret) map[string]string {
		return v.Labels
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	page, next, err := paginateRequest(r, items, func(v v1.Secret) (string, string) {
		return v.CreatedAt, v.ID
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	doc := v1.NewSecretList(page)
	doc.Next = next
	s.writeJSON(w, r, http.StatusOK, doc)
}

func (s *Server) getSecret(w http.ResponseWriter, r *http.Request) {
	secret, err := s.cfg.Client.GetSecret(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSONWithETag(w, r, http.StatusOK,
		v1.NewSecretDocument(secret), revisionETag(secret.Revision))
}

// labelSelector is one parsed ?label= value. hasValue distinguishes "key" from
// "key=value".
type labelSelector struct {
	key      string
	value    string
	hasValue bool
}

// filterByLabels applies every selector (logical AND). An unknown key or value
// simply matches nothing; filtering never fails on absent data.
func filterByLabels[T any](items []T, raw []string, labelsOf func(T) map[string]string) ([]T, error) {
	if len(raw) == 0 {
		return items, nil
	}

	selectors, err := parseLabelSelectors(raw)
	if err != nil {
		return nil, err
	}

	kept := make([]T, 0, len(items))
	for _, item := range items {
		if matchesLabels(labelsOf(item), selectors) {
			kept = append(kept, item)
		}
	}
	return kept, nil
}

// parseLabelSelectors parses "key" and "key=value" selectors.
func parseLabelSelectors(raw []string) ([]labelSelector, error) {
	selectors := make([]labelSelector, 0, len(raw))
	for _, item := range raw {
		key, value, hasValue := strings.Cut(item, "=")
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, output.NewError(output.CodeInvalid,
				"label selector must be key or key=value").
				WithDetails(map[string]any{"fields": map[string]any{"label": item}})
		}
		selectors = append(selectors, labelSelector{key: key, value: value, hasValue: hasValue})
	}
	return selectors, nil
}

func matchesLabels(labels map[string]string, selectors []labelSelector) bool {
	for _, selector := range selectors {
		got, ok := labels[selector.key]
		if !ok {
			return false
		}
		if selector.hasValue && got != selector.value {
			return false
		}
	}
	return true
}
