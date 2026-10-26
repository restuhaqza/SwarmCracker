package conformance

// This file owns the mock environment: an in-memory apihttp.Client plus a fake
// broker, wired to the real apihttp handler on loopback TLS. It is the CI leg of
// the suite. The lab leg runs the same fixtures against a real manager, so
// nothing here may leak into the fixtures themselves.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
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
	// mockTaskID is the task the stream fixtures exec into. The runner receives
	// it as $task, the same variable the lab run reads from CONFORMANCE_TASK_ID.
	mockTaskID = "task-a"
	// mockMinRefPrefix mirrors pkg/client's minimum ID prefix: shorter than this
	// is invalid, never a prefix match.
	mockMinRefPrefix = 4
)

// mockConsoleBytes is what the mock node writes on an attach session: the
// fixture reads exactly this many bytes (14).
var mockConsoleBytes = []byte("console ready\n")

// expectedMockCases is the number of fixture cases the mock leg must execute. A
// case that silently stops running (a stricter skip policy, a lost file, a typo
// in a case name that a filter no longer matches) is a regression the mock leg
// must fail on, so the count is pinned here and bumped with every new case.
const expectedMockCases = 49

// expectedMockGatedCases is how many of those cases are skipped because the mock
// does not advertise the capability they require. The mock is the shipped
// default — snapshots are off, so every snapshot case is a declared gate, not a
// silent loss. Any other skip is a failure. The files round-trip is not gated:
// the mock broker serves the file route, so those cases have to run.
const expectedMockGatedCases = 5

// TestConformanceMock executes every fixture against the real handler with an
// in-memory backend. Mutations always run: this backend is thrown away. The only
// allowed skip is a declared capability gate: every other skip is either a broken
// fixture or a policy that no longer fits the mock, and both must be visible.
func TestConformanceMock(t *testing.T) {
	harness := newMockHarness(t)

	info := fetchVersion(t, harness.server.Client(), harness.server.URL, harness.tokens["reader"])
	t.Logf("mock handler: %s", describeCapabilities(info))

	summary := Run(t, Env{
		BaseURL:       harness.server.URL,
		Client:        harness.server.Client(),
		Tokens:        harness.tokens,
		Vars:          map[string]string{"task": mockTaskID},
		Capabilities:  info.Capabilities,
		AllowMutation: true,
	}, "fixtures")

	if unexpected := summary.Skipped - summary.Gated; unexpected != 0 {
		t.Fatalf("%d of %d mock cases were skipped without a capability gate; the mock run executes every case it can",
			unexpected, summary.Cases)
	}
	if summary.Gated != expectedMockGatedCases {
		t.Fatalf("%d cases were capability-gated, want %d; bump expectedMockGatedCases with the fixture change",
			summary.Gated, expectedMockGatedCases)
	}
	if summary.Cases != expectedMockCases {
		t.Fatalf("%d cases ran, want %d; bump expectedMockCases with the fixture change", summary.Cases, expectedMockCases)
	}
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
	// A runner must carry a selector. mockTaskID is the only task the exec
	// fixture uses, so the selector names exactly that task.
	runner, _, err := store.CreateWithOptions("conformance-runner", apihttp.RoleRunner,
		apihttp.TokenOptions{TaskSelectors: &apihttp.TaskSelectors{TaskIDs: []string{mockTaskID}}})
	if err != nil {
		t.Fatalf("mint runner token: %v", err)
	}
	admin, _, err := store.Create("conformance-admin", apihttp.RoleAdmin)
	if err != nil {
		t.Fatalf("mint admin token: %v", err)
	}

	audit, err := apihttp.OpenAuditStore(filepath.Join(t.TempDir(), "audit.jsonl"), 0)
	if err != nil {
		t.Fatalf("open audit store: %v", err)
	}

	srv, err := apihttp.New(apihttp.Config{
		Client:           backend,
		Broker:           backend,
		Tokens:           store,
		Audit:            audit,
		Idempotency:      apihttp.NewIdempotencyStore(0),
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

	// files is the guest filesystem the file routes read and write, keyed by the
	// cleaned path the caller named. It is what makes the raw round-trip real:
	// the PUT bytes are stored and a later GET has to return them unchanged.
	files map[string][]byte

	nextID int
	now    time.Time
}

func newMockBackend() *mockBackend {
	settings := "listen 8080"

	return &mockBackend{
		services: []v1.Service{{
			ID:        "svc-web-1",
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
			ID:        "svc-api-2",
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
			ID:           mockTaskID,
			ServiceID:    "svc-web-1",
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
			ServiceID:    "svc-api-2",
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
		files:  make(map[string][]byte),
		nextID: 100,
		now:    time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC),
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
// then a unique ID prefix of at least mockMinRefPrefix characters. Ambiguous
// prefixes are invalid, not a guess, and name their candidates.
func findIndex[T any](items []T, kind, ref string, id, name func(T) string) (int, error) {
	for i, item := range items {
		if id(item) == ref {
			return i, nil
		}
		if name != nil && name(item) == ref {
			return i, nil
		}
	}

	if len(ref) < mockMinRefPrefix {
		return -1, output.NewErrorf(output.CodeInvalid,
			"%s reference %q is too short: use an exact id or name, or at least %d characters of an id",
			kind, ref, mockMinRefPrefix)
	}

	match := -1
	candidates := make([]map[string]string, 0, 2)
	for i, item := range items {
		if !strings.HasPrefix(id(item), ref) {
			continue
		}
		match = i
		candidate := map[string]string{"id": id(item)}
		if name != nil {
			candidate["name"] = name(item)
		}
		candidates = append(candidates, candidate)
	}

	switch {
	case match < 0:
		return -1, output.NewErrorf(output.CodeNotFound, "%s %q not found", kind, ref)
	case len(candidates) > 1:
		return -1, output.NewErrorf(output.CodeAmbiguous,
			"%s reference %q matches %d %ss", kind, ref, len(candidates), kind).
			WithDetails(map[string]any{"candidates": candidates})
	default:
		return match, nil
	}
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
		v1.EventResource{Kind: v1.KindService, ID: "svc-web-1", Name: "web"}, "1")
	events <- v1.NewEvent("2", "2026-01-03T00:01:00Z", "service.updated",
		v1.EventResource{Kind: v1.KindService, ID: "svc-web-1", Name: "web"}, "2")
	close(events)

	// errs must stay open: a closed error channel is also ready to receive, and
	// the handler's select would then end the stream before the second event.
	errs := make(chan error)
	return events, errs, nil
}

// Broker: Proxy serves a finite exec, log, file or attach stream.

func (b *mockBackend) Proxy(_ context.Context, w http.ResponseWriter, r *http.Request, _ string, op string) error {
	switch op {
	case "files":
		return b.serveFile(w, r)
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
	case "attach":
		// The console relay is raw bytes, not frames: the fixture reads exactly
		// this many.
		w.Header().Set("Content-Type", octetStreamMediaType)
		w.WriteHeader(http.StatusOK)
		_, err := w.Write(mockConsoleBytes)
		return err
	default:
		return output.NewErrorf(output.CodeNotImplemented,
			"the conformance mock has no %q node operation", op)
	}
}

// serveFile is the node half of the raw file round-trip: a PUT stores the bytes
// it receives, a GET returns those exact bytes, and a path the guest does not
// have is a typed not_found the manager forwards unchanged.
func (b *mockBackend) serveFile(w http.ResponseWriter, r *http.Request) error {
	target := path.Clean(r.URL.Query().Get("path"))

	if r.Method == http.MethodPut {
		// Read before taking the lock: the upload is the caller's body, not
		// backend state, and a slow upload must not hold the cluster.
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return output.NewErrorf(output.CodeInvalid, "read the upload: %v", err)
		}
		b.mu.Lock()
		b.files[target] = body
		b.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return nil
	}

	b.mu.Lock()
	content, ok := b.files[target]
	b.mu.Unlock()

	if !ok {
		return output.NewErrorf(output.CodeNotFound, "file %q not found on the guest", target)
	}
	w.Header().Set("Content-Type", octetStreamMediaType)
	w.WriteHeader(http.StatusOK)
	_, err := w.Write(content)
	return err
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

// TestFindIndexResolvesReferences pins the resolution order the fixture set
// depends on: exact ID, exact name, then a unique prefix of at least four
// characters. An ambiguous prefix names its candidates instead of guessing.
func TestFindIndexResolvesReferences(t *testing.T) {
	services := []v1.Service{
		{ID: "svc-web-1", Name: "web"},
		{ID: "svc-api-2", Name: "api"},
	}
	id := func(s v1.Service) string { return s.ID }
	name := func(s v1.Service) string { return s.Name }

	tests := []struct {
		name       string
		ref        string
		want       int
		code       output.Code
		candidates []string
	}{
		{name: "exact id", ref: "svc-api-2", want: 1},
		{name: "exact name", ref: "web", want: 0},
		{name: "unique id prefix", ref: "svc-web", want: 0},
		{name: "ambiguous id prefix", ref: "svc-", want: -1, code: output.CodeAmbiguous,
			candidates: []string{"web", "api"}},
		{name: "prefix shorter than four characters", ref: "svc", want: -1, code: output.CodeInvalid},
		{name: "unknown reference", ref: "nope", want: -1, code: output.CodeNotFound},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			index, err := findIndex(services, "service", tc.ref, id, name)
			if tc.code == "" {
				if err != nil || index != tc.want {
					t.Fatalf("findIndex(%q) = %d, %v; want %d", tc.ref, index, err, tc.want)
				}
				return
			}

			var typed *output.Error
			if !errors.As(err, &typed) || typed.Code != tc.code {
				t.Fatalf("findIndex(%q) = %v; want code %q", tc.ref, err, tc.code)
			}
			if tc.candidates == nil {
				return
			}

			got, ok := typed.Details["candidates"].([]map[string]string)
			if !ok || len(got) != len(tc.candidates) {
				t.Fatalf("candidates = %#v, want %d with names %v", typed.Details["candidates"], len(tc.candidates), tc.candidates)
			}
			for i, want := range tc.candidates {
				if got[i]["name"] != want {
					t.Fatalf("candidate %d name = %q, want %q", i, got[i]["name"], want)
				}
			}
		})
	}
}
