package conformance

// This file owns the mock environment: an in-memory apihttp.Client plus a fake
// broker, wired to the real apihttp handler on loopback TLS. It is the CI leg of
// the suite. The lab leg runs the same fixtures against a real manager, so
// nothing here may leak into the fixtures themselves.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/apihttp"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

const (
	mockServerVersion = "0.11.0"
	mockGRPCVersion   = "v1"
	mockNodeA         = "node-a"
	mockNodeB         = "node-b"
)

// TestConformanceMock executes every fixture against the real handler with an
// in-memory backend. Mutations always run: this backend is thrown away.
func TestConformanceMock(t *testing.T) {
	harness := newMockHarness(t)

	Run(t, Env{
		BaseURL:       harness.server.URL,
		Client:        harness.server.Client(),
		Tokens:        harness.tokens,
		AllowMutation: true,
	}, "fixtures")
}

// mockHarness is the real handler, a real token store and a TLS listener.
type mockHarness struct {
	server *httptest.Server
	tokens map[string]string
}

func newMockHarness(t *testing.T) *mockHarness {
	t.Helper()

	backend := newMockBackend()

	store, err := apihttp.OpenTokenStore(filepath.Join(t.TempDir(), "tokens.json"))
	if err != nil {
		t.Fatalf("open token store: %v", err)
	}
	reader, _, err := store.Create("conformance-reader", apihttp.RoleReader)
	if err != nil {
		t.Fatalf("mint reader token: %v", err)
	}
	// A runner must carry a selector. task-a is the only task the exec fixture
	// uses, so the selector names exactly that task.
	runner, _, err := store.CreateWithOptions("conformance-runner", apihttp.RoleRunner,
		apihttp.TokenOptions{TaskSelectors: &apihttp.TaskSelectors{TaskIDs: []string{"task-a"}}})
	if err != nil {
		t.Fatalf("mint runner token: %v", err)
	}
	admin, _, err := store.Create("conformance-admin", apihttp.RoleAdmin)
	if err != nil {
		t.Fatalf("mint admin token: %v", err)
	}

	srv, err := apihttp.New(apihttp.Config{
		Client:           backend,
		Broker:           backend,
		Tokens:           store,
		ServerVersion:    mockServerVersion,
		GRPCVersion:      mockGRPCVersion,
		RateLimit:        apihttp.NewRateLimiter(1e9, 1e9),
		EventHeartbeat:   time.Hour,
		HealthPageBudget: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("build apihttp server: %v", err)
	}

	server := httptest.NewTLSServer(srv.Handler())
	t.Cleanup(server.Close)

	return &mockHarness{
		server: server,
		tokens: map[string]string{
			"reader": reader,
			"runner": runner,
			"admin":  admin,
		},
	}
}

// mockBackend is a complete apihttp.Client and apihttp.Broker. Every method
// takes the lock, so the -race detector sees one consistent in-memory cluster.
type mockBackend struct {
	mu sync.Mutex

	services []v1.Service
	tasks    []v1.Task
	nodes    []v1.Node
	networks []v1.Network
	configs  []v1.Config
	secrets  []v1.Secret

	secretPayloads map[string]string

	nextID int
	now    time.Time
}

func newMockBackend() *mockBackend {
	settings := "listen 8080"

	return &mockBackend{
		services: []v1.Service{{
			ID:        "svc-1",
			Name:      "web",
			Revision:  "1",
			CreatedAt: "2026-01-01T00:00:00Z",
			Labels:    map[string]string{"team": "payments"},
			Spec: v1.ServiceSpec{
				Mode:     "replicated",
				Replicas: int64Ptr(1),
				Task:     v1.TaskSpec{Image: "nginx:alpine"},
			},
			Status: v1.ServiceStatus{DesiredTasks: 1, RunningTasks: 1, Converged: true},
		}, {
			ID:        "svc-2",
			Name:      "api",
			Revision:  "1",
			CreatedAt: "2026-01-02T00:00:00Z",
			Spec: v1.ServiceSpec{
				Mode:     "replicated",
				Replicas: int64Ptr(1),
				Task:     v1.TaskSpec{Image: "ghcr.io/example/api:2"},
			},
			Status: v1.ServiceStatus{DesiredTasks: 1, RunningTasks: 1, Converged: true},
		}},
		tasks: []v1.Task{{
			ID:           "task-a",
			ServiceID:    "svc-1",
			ServiceName:  "web",
			NodeID:       mockNodeA,
			NodeName:     "manager-a",
			Revision:     "1",
			State:        "running",
			DesiredState: "running",
			CreatedAt:    "2026-01-01T00:00:00Z",
			Image:        "nginx:alpine",
		}, {
			ID:           "task-b",
			ServiceID:    "svc-2",
			ServiceName:  "api",
			NodeID:       mockNodeB,
			NodeName:     "worker-b",
			Revision:     "1",
			State:        "running",
			DesiredState: "running",
			CreatedAt:    "2026-01-02T00:00:00Z",
			Image:        "ghcr.io/example/api:2",
		}},
		nodes: []v1.Node{{
			ID:           mockNodeA,
			Hostname:     "manager-a",
			Revision:     "1",
			CreatedAt:    "2026-01-01T00:00:00Z",
			Role:         "manager",
			Membership:   "accepted",
			Availability: "active",
			State:        "ready",
			Address:      "10.0.0.1",
		}},
		networks: []v1.Network{{
			ID:        "net-1",
			Name:      "frontend",
			Revision:  "1",
			CreatedAt: "2026-01-01T00:00:00Z",
			Driver:    "overlay",
		}},
		configs: []v1.Config{{
			ID:        "cfg-1",
			Name:      "settings",
			Revision:  "1",
			CreatedAt: "2026-01-01T00:00:00Z",
			Data:      &settings,
		}},
		secrets: []v1.Secret{{
			ID:        "sec-1",
			Name:      "password",
			Revision:  "1",
			CreatedAt: "2026-01-01T00:00:00Z",
		}},
		secretPayloads: map[string]string{"sec-1": "c2VjcmV0"},
		nextID:         100,
		now:            time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC),
	}
}

func int64Ptr(value int64) *int64 { return &value }

// newID and timestamp keep created objects ordered after the fixtures.
func (b *mockBackend) newID(prefix string) string {
	b.nextID++
	return fmt.Sprintf("%s-%d", prefix, b.nextID)
}

func (b *mockBackend) timestamp() string {
	b.now = b.now.Add(time.Minute)
	return b.now.Format(time.RFC3339)
}

// findIndex resolves a reference the way pkg/client does: exact ID, exact name,
// then a unique ID prefix. Ambiguous prefixes are invalid, not a guess.
func findIndex[T any](items []T, kind, ref string, id, name func(T) string) (int, error) {
	for i, item := range items {
		if id(item) == ref {
			return i, nil
		}
		if name != nil && name(item) == ref {
			return i, nil
		}
	}

	match := -1
	for i, item := range items {
		if !strings.HasPrefix(id(item), ref) {
			continue
		}
		if match >= 0 {
			return -1, output.NewErrorf(output.CodeAmbiguous,
				"%s reference %q matches more than one object", kind, ref)
		}
		match = i
	}
	if match >= 0 {
		return match, nil
	}
	return -1, output.NewErrorf(output.CodeNotFound, "%s %q not found", kind, ref)
}

func nameExists(kind, name string) error {
	return output.NewErrorf(output.CodeConflict, "%s %q already exists", kind, name).
		WithDetails(map[string]any{"reason": "name_exists"})
}

// bareTag strips the quotes and any weak marker from an If-Match value.
func bareTag(value string) string {
	value = strings.TrimPrefix(strings.TrimSpace(value), "W/")
	return strings.Trim(value, `"`)
}

func revisionMatches(ifMatch, revision string) bool {
	return ifMatch == "" || bareTag(ifMatch) == revision
}

func preconditionFailed(ifMatch, revision string) error {
	return output.PreconditionFailed(bareTag(ifMatch), revision)
}

func bumpRevision(revision string) string {
	current, err := strconv.Atoi(revision)
	if err != nil {
		return "1"
	}
	return strconv.Itoa(current + 1)
}

// mergeJSON applies an RFC 7386 merge patch to a decoded document.
func mergeJSON(base, patch any) any {
	overlay, ok := patch.(map[string]any)
	if !ok {
		return patch
	}
	target, ok := base.(map[string]any)
	if !ok {
		target = make(map[string]any, len(overlay))
	}
	for key, value := range overlay {
		if value == nil {
			delete(target, key)
			continue
		}
		target[key] = mergeJSON(target[key], value)
	}
	return target
}

// applyPatch merges patch into item and lets preserve restore the fields a
// patch must not change. It reports a bad patch body as invalid.
func applyPatch[T any](item *T, patch []byte, preserve func(*T)) error {
	current, err := json.Marshal(item)
	if err != nil {
		return output.NewErrorf(output.CodeInternal, "encode current object: %v", err)
	}

	var base any
	if err := json.Unmarshal(current, &base); err != nil {
		return output.NewErrorf(output.CodeInternal, "decode current object: %v", err)
	}
	var overlay any
	if err := json.Unmarshal(patch, &overlay); err != nil {
		return output.NewErrorf(output.CodeInvalid, "invalid JSON body: %v", err)
	}

	merged, err := json.Marshal(mergeJSON(base, overlay))
	if err != nil {
		return output.NewErrorf(output.CodeInternal, "encode merged object: %v", err)
	}

	var updated T
	if err := json.Unmarshal(merged, &updated); err != nil {
		return output.NewErrorf(output.CodeInvalid, "invalid JSON body: %v", err)
	}
	preserve(&updated)
	*item = updated
	return nil
}

// List reads.

func (b *mockBackend) ListServices(context.Context) ([]v1.Service, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]v1.Service(nil), b.services...), nil
}

func (b *mockBackend) GetService(_ context.Context, ref string) (v1.Service, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	index, err := findIndex(b.services, "service", ref,
		func(s v1.Service) string { return s.ID },
		func(s v1.Service) string { return s.Name })
	if err != nil {
		return v1.Service{}, err
	}
	return b.services[index], nil
}

func (b *mockBackend) ListServiceTasks(_ context.Context, ref string) ([]v1.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	index, err := findIndex(b.services, "service", ref,
		func(s v1.Service) string { return s.ID },
		func(s v1.Service) string { return s.Name })
	if err != nil {
		return nil, err
	}
	serviceID := b.services[index].ID

	tasks := make([]v1.Task, 0, len(b.tasks))
	for _, task := range b.tasks {
		if task.ServiceID == serviceID {
			tasks = append(tasks, task)
		}
	}
	return tasks, nil
}

func (b *mockBackend) ListTasks(context.Context) ([]v1.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]v1.Task(nil), b.tasks...), nil
}

func (b *mockBackend) GetTask(_ context.Context, ref string) (v1.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	index, err := findIndex(b.tasks, "task", ref,
		func(t v1.Task) string { return t.ID },
		func(t v1.Task) string { return t.Name })
	if err != nil {
		return v1.Task{}, err
	}
	return b.tasks[index], nil
}

func (b *mockBackend) ListNodes(context.Context) ([]v1.Node, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]v1.Node(nil), b.nodes...), nil
}

func (b *mockBackend) GetNode(_ context.Context, ref string) (v1.Node, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	index, err := findIndex(b.nodes, "node", ref,
		func(n v1.Node) string { return n.ID },
		func(n v1.Node) string { return n.Hostname })
	if err != nil {
		return v1.Node{}, err
	}
	return b.nodes[index], nil
}

func (b *mockBackend) ListNetworks(context.Context) ([]v1.Network, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]v1.Network(nil), b.networks...), nil
}

func (b *mockBackend) GetNetwork(_ context.Context, ref string) (v1.Network, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	index, err := findIndex(b.networks, "network", ref,
		func(n v1.Network) string { return n.ID },
		func(n v1.Network) string { return n.Name })
	if err != nil {
		return v1.Network{}, err
	}
	return b.networks[index], nil
}

func (b *mockBackend) ListConfigs(context.Context) ([]v1.Config, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]v1.Config(nil), b.configs...), nil
}

func (b *mockBackend) GetConfig(_ context.Context, ref string) (v1.Config, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	index, err := findIndex(b.configs, "config", ref,
		func(c v1.Config) string { return c.ID },
		func(c v1.Config) string { return c.Name })
	if err != nil {
		return v1.Config{}, err
	}
	return b.configs[index], nil
}

func (b *mockBackend) ListSecrets(context.Context) ([]v1.Secret, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]v1.Secret(nil), b.secrets...), nil
}

func (b *mockBackend) GetSecret(_ context.Context, ref string) (v1.Secret, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	index, err := findIndex(b.secrets, "secret", ref,
		func(s v1.Secret) string { return s.ID },
		func(s v1.Secret) string { return s.Name })
	if err != nil {
		return v1.Secret{}, err
	}
	return b.secrets[index], nil
}

func (b *mockBackend) ClusterID(context.Context) (string, error) {
	return "cluster-conformance", nil
}

// Service writes.

func (b *mockBackend) CreateService(_ context.Context, write v1.ServiceWrite, _ v1.WriteOptions) (v1.Service, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, service := range b.services {
		if service.Name == write.Name {
			return v1.Service{}, nameExists("service", write.Name)
		}
	}

	spec := v1.ServiceSpec{
		Mode:      write.Spec.Mode,
		Replicas:  write.Spec.Replicas,
		Task:      write.Spec.Task.TaskSpec,
		Update:    write.Spec.Update,
		Rollback:  write.Spec.Rollback,
		Placement: write.Spec.Placement,
	}
	if spec.Mode == "" {
		spec.Mode = "replicated"
	}

	service := v1.Service{
		ID:        b.newID("svc"),
		Name:      write.Name,
		Labels:    write.Labels,
		Revision:  "1",
		CreatedAt: b.timestamp(),
		Spec:      spec,
		Status:    v1.ServiceStatus{DesiredTasks: 1, RunningTasks: 1, Converged: true},
	}
	b.services = append(b.services, service)
	return service, nil
}

func (b *mockBackend) UpdateService(_ context.Context, ref string, patch []byte, opts v1.WriteOptions) (v1.Service, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	index, err := findIndex(b.services, "service", ref,
		func(s v1.Service) string { return s.ID },
		func(s v1.Service) string { return s.Name })
	if err != nil {
		return v1.Service{}, err
	}
	current := b.services[index]
	if !revisionMatches(opts.IfMatch, current.Revision) {
		return v1.Service{}, preconditionFailed(opts.IfMatch, current.Revision)
	}

	err = applyPatch(&b.services[index], patch, func(s *v1.Service) {
		s.ID = current.ID
		s.Name = current.Name
		s.CreatedAt = current.CreatedAt
		s.Revision = bumpRevision(current.Revision)
		s.UpdatedAt = b.timestamp()
	})
	if err != nil {
		return v1.Service{}, err
	}
	return b.services[index], nil
}

func (b *mockBackend) DeleteService(_ context.Context, ref string, opts v1.WriteOptions) (v1.Service, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	index, err := findIndex(b.services, "service", ref,
		func(s v1.Service) string { return s.ID },
		func(s v1.Service) string { return s.Name })
	if err != nil {
		return v1.Service{}, err
	}
	current := b.services[index]
	if !revisionMatches(opts.IfMatch, current.Revision) {
		return v1.Service{}, preconditionFailed(opts.IfMatch, current.Revision)
	}

	b.services = append(b.services[:index], b.services[index+1:]...)
	return current, nil
}

func (b *mockBackend) RollbackService(context.Context, string, v1.WriteOptions) (v1.Service, error) {
	return v1.Service{}, output.NewError(output.CodeNotImplemented,
		"the conformance mock does not serve rollback")
}

// Config writes.

func (b *mockBackend) CreateConfig(_ context.Context, write v1.ConfigWrite, _ v1.WriteOptions) (v1.Config, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, config := range b.configs {
		if config.Name == write.Name {
			return v1.Config{}, nameExists("config", write.Name)
		}
	}

	config := v1.Config{
		ID:        b.newID("cfg"),
		Name:      write.Name,
		Labels:    write.Labels,
		Revision:  "1",
		CreatedAt: b.timestamp(),
	}
	if write.Data != "" {
		data := write.Data
		config.Data = &data
	}
	b.configs = append(b.configs, config)
	return config, nil
}

func (b *mockBackend) UpdateConfig(_ context.Context, ref string, patch []byte, opts v1.WriteOptions) (v1.Config, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	index, err := findIndex(b.configs, "config", ref,
		func(c v1.Config) string { return c.ID },
		func(c v1.Config) string { return c.Name })
	if err != nil {
		return v1.Config{}, err
	}
	current := b.configs[index]
	if !revisionMatches(opts.IfMatch, current.Revision) {
		return v1.Config{}, preconditionFailed(opts.IfMatch, current.Revision)
	}

	err = applyPatch(&b.configs[index], patch, func(c *v1.Config) {
		c.ID = current.ID
		c.Name = current.Name
		c.CreatedAt = current.CreatedAt
		c.Revision = bumpRevision(current.Revision)
		c.UpdatedAt = b.timestamp()
	})
	if err != nil {
		return v1.Config{}, err
	}
	return b.configs[index], nil
}

func (b *mockBackend) DeleteConfig(_ context.Context, ref string, opts v1.WriteOptions) (v1.Config, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	index, err := findIndex(b.configs, "config", ref,
		func(c v1.Config) string { return c.ID },
		func(c v1.Config) string { return c.Name })
	if err != nil {
		return v1.Config{}, err
	}
	current := b.configs[index]
	if !revisionMatches(opts.IfMatch, current.Revision) {
		return v1.Config{}, preconditionFailed(opts.IfMatch, current.Revision)
	}

	b.configs = append(b.configs[:index], b.configs[index+1:]...)
	return current, nil
}

// Secret writes.

func (b *mockBackend) CreateSecret(_ context.Context, write v1.SecretWrite, _ v1.WriteOptions) (v1.Secret, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, secret := range b.secrets {
		if secret.Name == write.Name {
			return v1.Secret{}, nameExists("secret", write.Name)
		}
	}

	secret := v1.Secret{
		ID:        b.newID("sec"),
		Name:      write.Name,
		Labels:    write.Labels,
		Revision:  "1",
		CreatedAt: b.timestamp(),
	}
	if write.Driver != nil {
		secret.Driver = write.Driver.Name
	}
	b.secrets = append(b.secrets, secret)
	b.secretPayloads[secret.ID] = write.Data
	return secret, nil
}

func (b *mockBackend) UpdateSecret(_ context.Context, ref string, patch []byte, opts v1.WriteOptions) (v1.Secret, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	index, err := findIndex(b.secrets, "secret", ref,
		func(s v1.Secret) string { return s.ID },
		func(s v1.Secret) string { return s.Name })
	if err != nil {
		return v1.Secret{}, err
	}
	current := b.secrets[index]
	if !revisionMatches(opts.IfMatch, current.Revision) {
		return v1.Secret{}, preconditionFailed(opts.IfMatch, current.Revision)
	}

	err = applyPatch(&b.secrets[index], patch, func(s *v1.Secret) {
		s.ID = current.ID
		s.Name = current.Name
		s.CreatedAt = current.CreatedAt
		s.Revision = bumpRevision(current.Revision)
		s.UpdatedAt = b.timestamp()
	})
	if err != nil {
		return v1.Secret{}, err
	}
	return b.secrets[index], nil
}

func (b *mockBackend) DeleteSecret(_ context.Context, ref string, opts v1.WriteOptions) (v1.Secret, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	index, err := findIndex(b.secrets, "secret", ref,
		func(s v1.Secret) string { return s.ID },
		func(s v1.Secret) string { return s.Name })
	if err != nil {
		return v1.Secret{}, err
	}
	current := b.secrets[index]
	if !revisionMatches(opts.IfMatch, current.Revision) {
		return v1.Secret{}, preconditionFailed(opts.IfMatch, current.Revision)
	}

	b.secrets = append(b.secrets[:index], b.secrets[index+1:]...)
	delete(b.secretPayloads, current.ID)
	return current, nil
}

// Network and node writes.

func (b *mockBackend) CreateNetwork(_ context.Context, write v1.NetworkWrite, _ v1.WriteOptions) (v1.Network, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, network := range b.networks {
		if network.Name == write.Name {
			return v1.Network{}, nameExists("network", write.Name)
		}
	}

	driver := write.Driver
	if driver == "" {
		driver = "overlay"
	}
	network := v1.Network{
		ID:         b.newID("net"),
		Name:       write.Name,
		Revision:   "1",
		CreatedAt:  b.timestamp(),
		Driver:     driver,
		Labels:     write.Labels,
		Attachable: write.Attachable,
	}
	if write.IPAM != nil {
		network.IPAM = &v1.NetworkIPAM{Subnet: write.IPAM.Subnet, Gateway: write.IPAM.Gateway}
	}
	b.networks = append(b.networks, network)
	return network, nil
}

func (b *mockBackend) DeleteNetwork(_ context.Context, ref string, _ v1.WriteOptions) (v1.Network, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	index, err := findIndex(b.networks, "network", ref,
		func(n v1.Network) string { return n.ID },
		func(n v1.Network) string { return n.Name })
	if err != nil {
		return v1.Network{}, err
	}
	current := b.networks[index]
	b.networks = append(b.networks[:index], b.networks[index+1:]...)
	return current, nil
}

func (b *mockBackend) UpdateNode(_ context.Context, ref string, patch []byte, opts v1.WriteOptions) (v1.Node, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	index, err := findIndex(b.nodes, "node", ref,
		func(n v1.Node) string { return n.ID },
		func(n v1.Node) string { return n.Hostname })
	if err != nil {
		return v1.Node{}, err
	}
	current := b.nodes[index]
	if !revisionMatches(opts.IfMatch, current.Revision) {
		return v1.Node{}, preconditionFailed(opts.IfMatch, current.Revision)
	}

	err = applyPatch(&b.nodes[index], patch, func(n *v1.Node) {
		n.ID = current.ID
		n.CreatedAt = current.CreatedAt
		n.Revision = bumpRevision(current.Revision)
		n.UpdatedAt = b.timestamp()
	})
	if err != nil {
		return v1.Node{}, err
	}
	return b.nodes[index], nil
}

// WatchEvents emits two events and closes the channel, which is the smallest
// finite stream the SSE contract can be read from.
func (b *mockBackend) WatchEvents(context.Context, v1.EventOptions) (<-chan v1.Event, <-chan error, error) {
	events := make(chan v1.Event, 2)
	events <- v1.NewEvent("1", "2026-01-03T00:00:00Z", "service.created",
		v1.EventResource{Kind: v1.KindService, ID: "svc-1", Name: "web"}, "1")
	events <- v1.NewEvent("2", "2026-01-03T00:01:00Z", "service.updated",
		v1.EventResource{Kind: v1.KindService, ID: "svc-1", Name: "web"}, "2")
	close(events)

	// errs must stay open: a closed error channel is also ready to receive, and
	// the handler's select would then end the stream before the second event.
	errs := make(chan error)
	return events, errs, nil
}

// Broker: Proxy serves a finite exec or log stream.

func (b *mockBackend) Proxy(_ context.Context, w http.ResponseWriter, _ *http.Request, _ string, op string) error {
	switch op {
	case "exec":
		w.Header().Set("Content-Type", ndjsonMediaType)
		w.WriteHeader(http.StatusOK)
		return writeFrames(w, v1.StdoutFrame([]byte("hello\n")), v1.ExitFrame(0))
	case "logs":
		w.Header().Set("Content-Type", ndjsonMediaType)
		w.WriteHeader(http.StatusOK)
		return writeFrames(w,
			v1.LogDataFrame(time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC), []byte("hello\n")),
			v1.LogEOFFrame())
	default:
		return output.NewErrorf(output.CodeNotImplemented,
			"the conformance mock has no %q node operation", op)
	}
}

// Health reports a healthy task for node A and a typed failure for node B, which
// makes a health-enriched page a 207 with one partial.
func (b *mockBackend) Health(_ context.Context, task v1.Task) (*v1.TaskHealth, error) {
	if task.NodeID == mockNodeB {
		return nil, output.NewErrorf(output.CodeUnavailable,
			"node %q is unreachable", task.NodeName).
			WithDetails(map[string]any{"nodeId": task.NodeID})
	}
	return &v1.TaskHealth{Status: "healthy", CheckedAt: "2026-01-03T00:00:00Z"}, nil
}

// writeFrames writes an NDJSON body, one frame per line.
func writeFrames(w http.ResponseWriter, frames ...any) error {
	encoder := json.NewEncoder(w)
	for _, frame := range frames {
		if err := encoder.Encode(frame); err != nil {
			return err
		}
	}
	return nil
}
