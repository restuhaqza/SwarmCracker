package apihttp

import (
	"context"
	"net/http"
	"testing"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoleScopes(t *testing.T) {
	all := []Scope{
		ScopeServicesRead, ScopeServicesWrite, ScopeMigrationsWrite,
		ScopeTasksRead, ScopeTasksExec, ScopeTasksFiles, ScopeTasksLogs, ScopeTasksSnapshots,
		ScopeNodesRead, ScopeNodesWrite,
		ScopeNetworksRead, ScopeNetworksWrite,
		ScopeConfigsRead, ScopeConfigsReadValues, ScopeConfigsWrite,
		ScopeSecretsRead, ScopeSecretsWrite,
		ScopeEventsRead, ScopeClusterRead,
		ScopeTokensAdmin, ScopeAuditRead,
	}

	reader := []Scope{
		ScopeServicesRead, ScopeTasksRead, ScopeNodesRead, ScopeNetworksRead,
		ScopeConfigsRead, ScopeSecretsRead, ScopeEventsRead, ScopeTasksLogs,
		ScopeClusterRead,
	}
	runner := append(append([]Scope{}, reader...), ScopeTasksExec, ScopeTasksFiles, ScopeTasksSnapshots)
	operator := append(append([]Scope{}, runner...),
		ScopeServicesWrite, ScopeNetworksWrite, ScopeConfigsWrite,
		ScopeConfigsReadValues, ScopeSecretsWrite)

	cases := []struct {
		role string
		want []Scope
	}{
		{RoleReader, reader},
		{RoleRunner, runner},
		{RoleOperator, operator},
		{RoleAdmin, all},
	}
	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			got, err := RoleScopes(tc.role)
			require.NoError(t, err)
			assert.ElementsMatch(t, tc.want, got)
		})
	}

	for _, role := range []string{"", "Reader", "root", "auditor"} {
		_, err := RoleScopes(role)
		require.Error(t, err, role)
		assert.Equal(t, output.CodeInvalid, requireErrorCode(t, err), role)
	}
}

// TestRoleScopesReturnsACopy keeps one caller from narrowing the role table for
// every other caller.
func TestRoleScopesReturnsACopy(t *testing.T) {
	first, err := RoleScopes(RoleReader)
	require.NoError(t, err)
	require.NotEmpty(t, first)

	first[0] = "mutated"

	second, err := RoleScopes(RoleReader)
	require.NoError(t, err)
	assert.NotEqual(t, Scope("mutated"), second[0])
}

func TestAdminHoldsEveryScope(t *testing.T) {
	scopes, err := RoleScopes(RoleAdmin)
	require.NoError(t, err)

	for _, scope := range scopes {
		assert.True(t, KnownScope(string(scope)), "%s must be a known scope", scope)
	}
	// The role table is the source of truth for the taxonomy: every documented
	// scope must be reachable through admin.
	for _, scope := range []Scope{
		ScopeServicesRead, ScopeServicesWrite, ScopeMigrationsWrite, ScopeTasksRead, ScopeTasksExec,
		ScopeTasksFiles, ScopeTasksLogs, ScopeTasksSnapshots, ScopeNodesRead,
		ScopeNodesWrite, ScopeNetworksRead, ScopeNetworksWrite, ScopeConfigsRead,
		ScopeConfigsReadValues, ScopeConfigsWrite, ScopeSecretsRead, ScopeSecretsWrite,
		ScopeEventsRead, ScopeClusterRead, ScopeTokensAdmin, ScopeAuditRead,
	} {
		assert.Contains(t, scopes, scope)
	}
}

func TestKnownScope(t *testing.T) {
	for _, scope := range []string{
		"services:read", "services:write", "migrations:write", "tasks:read", "tasks:exec", "tasks:files",
		"tasks:logs", "tasks:snapshots", "nodes:read", "nodes:write", "networks:read",
		"networks:write", "configs:read", "configs:read-values", "configs:write",
		"secrets:read", "secrets:write", "events:read", "cluster:read", "tokens:admin",
		"audit:read",
	} {
		assert.True(t, KnownScope(scope), scope)
	}
	for _, scope := range []string{"", "services", "services:delete", "SERVICES:READ", "tasks:exec "} {
		assert.False(t, KnownScope(scope), scope)
	}
}

func TestIdentityHasScope(t *testing.T) {
	identity := Identity{Type: "token", Scopes: []Scope{ScopeServicesRead, ScopeTasksRead}}

	assert.True(t, identity.HasScope(ScopeServicesRead))
	assert.True(t, identity.HasScope(ScopeTasksRead))
	assert.False(t, identity.HasScope(ScopeServicesWrite), "deny by default")
	assert.False(t, identity.HasScope(Scope("unknown:scope")))

	assert.False(t, Identity{}.HasScope(ScopeServicesRead))
	assert.True(t, Identity{Scopes: []Scope{Scope("services:read ")}}.HasScope(Scope("services:read ")),
		"HasScope compares values verbatim; normalization belongs to the token store")
}

func TestTaskSelectorsEmpty(t *testing.T) {
	assert.True(t, TaskSelectors{}.Empty())
	assert.True(t, (&TaskSelectors{}).Empty())
	assert.False(t, TaskSelectors{ServiceIDs: []string{"svc-1"}}.Empty())
	assert.False(t, TaskSelectors{TaskIDs: []string{"task-1"}}.Empty())
	assert.False(t, TaskSelectors{Labels: map[string]string{"team": "payments"}}.Empty())
}

func TestTaskSelectorsMatches(t *testing.T) {
	labels := map[string]string{"team": "payments", "tier": "gold"}

	cases := []struct {
		name      string
		selectors TaskSelectors
		serviceID string
		taskID    string
		labels    map[string]string
		want      bool
	}{
		{
			name:      "empty matches nothing",
			selectors: TaskSelectors{},
			serviceID: "svc-1", taskID: "task-1", labels: labels,
			want: false,
		},
		{
			name:      "service id match",
			selectors: TaskSelectors{ServiceIDs: []string{"svc-1", "svc-2"}},
			serviceID: "svc-2", taskID: "task-1", labels: labels,
			want: true,
		},
		{
			name:      "service id mismatch",
			selectors: TaskSelectors{ServiceIDs: []string{"svc-2"}},
			serviceID: "svc-1", taskID: "task-1", labels: labels,
			want: false,
		},
		{
			name:      "task id match",
			selectors: TaskSelectors{TaskIDs: []string{"task-1"}},
			serviceID: "svc-1", taskID: "task-1", labels: labels,
			want: true,
		},
		{
			name:      "task id mismatch",
			selectors: TaskSelectors{TaskIDs: []string{"task-2"}},
			serviceID: "svc-1", taskID: "task-1", labels: labels,
			want: false,
		},
		{
			name:      "labels all match",
			selectors: TaskSelectors{Labels: map[string]string{"team": "payments", "tier": "gold"}},
			serviceID: "svc-1", taskID: "task-1", labels: labels,
			want: true,
		},
		{
			name:      "one label differs",
			selectors: TaskSelectors{Labels: map[string]string{"team": "payments", "tier": "silver"}},
			serviceID: "svc-1", taskID: "task-1", labels: labels,
			want: false,
		},
		{
			name:      "missing label",
			selectors: TaskSelectors{Labels: map[string]string{"zone": "a"}},
			serviceID: "svc-1", taskID: "task-1", labels: labels,
			want: false,
		},
		{
			name: "dimensions are ANDed",
			selectors: TaskSelectors{
				ServiceIDs: []string{"svc-1"},
				TaskIDs:    []string{"task-2"},
			},
			serviceID: "svc-1", taskID: "task-1", labels: labels,
			want: false,
		},
		{
			name: "all dimensions match",
			selectors: TaskSelectors{
				ServiceIDs: []string{"svc-1"},
				TaskIDs:    []string{"task-1"},
				Labels:     map[string]string{"team": "payments"},
			},
			serviceID: "svc-1", taskID: "task-1", labels: labels,
			want: true,
		},
		{
			name:      "nil labels map never matches a label selector",
			selectors: TaskSelectors{Labels: map[string]string{"team": "payments"}},
			serviceID: "svc-1", taskID: "task-1", labels: nil,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.selectors.Matches(tc.serviceID, tc.taskID, tc.labels))
		})
	}
}

func TestAuthorizeTask(t *testing.T) {
	labels := map[string]string{"team": "payments"}

	admin := Identity{Type: "token", Role: RoleAdmin, Scopes: []Scope{ScopeTasksExec}}
	operator := Identity{Type: "token", Role: RoleOperator, Scopes: []Scope{ScopeTasksExec}}
	runnerScoped := Identity{
		Type: "token", Role: RoleRunner, Scopes: []Scope{ScopeTasksExec},
		Selectors: &TaskSelectors{ServiceIDs: []string{"svc-1"}},
	}

	t.Run("admin without selectors may touch every task", func(t *testing.T) {
		require.NoError(t, admin.AuthorizeTask("svc-any", "task-any", nil))
	})

	t.Run("operator without selectors may touch every task", func(t *testing.T) {
		require.NoError(t, operator.AuthorizeTask("svc-any", "task-any", nil))
	})

	t.Run("nil selectors on a non-runner mean all tasks", func(t *testing.T) {
		identity := Identity{Type: "token", Role: RoleOperator, Selectors: nil}
		require.NoError(t, identity.AuthorizeTask("svc-any", "task-any", nil))
	})

	t.Run("matching selector authorizes", func(t *testing.T) {
		require.NoError(t, runnerScoped.AuthorizeTask("svc-1", "task-9", labels))
	})

	t.Run("non-matching selector is forbidden", func(t *testing.T) {
		err := runnerScoped.AuthorizeTask("svc-2", "task-9", labels)
		require.Error(t, err)
		assert.Equal(t, output.CodeForbidden, requireErrorCode(t, err))

		var typed *output.Error
		require.ErrorAs(t, err, &typed)
		assert.Equal(t, "selector_mismatch", typed.Details["reason"])
	})

	t.Run("runner with nil selectors fails closed", func(t *testing.T) {
		identity := Identity{Type: "token", Role: RoleRunner}
		err := identity.AuthorizeTask("svc-1", "task-1", labels)
		require.Error(t, err)
		assert.Equal(t, output.CodeForbidden, requireErrorCode(t, err))

		var typed *output.Error
		require.ErrorAs(t, err, &typed)
		assert.Equal(t, "selector_required", typed.Details["reason"])
	})

	t.Run("runner with empty selectors fails closed", func(t *testing.T) {
		identity := Identity{Type: "token", Role: RoleRunner, Selectors: &TaskSelectors{}}
		err := identity.AuthorizeTask("svc-1", "task-1", labels)
		require.Error(t, err)

		var typed *output.Error
		require.ErrorAs(t, err, &typed)
		assert.Equal(t, "selector_required", typed.Details["reason"])
	})
}

func TestLocalIdentity(t *testing.T) {
	identity := LocalIdentity()

	assert.Equal(t, "local", identity.Type)
	assert.Equal(t, RoleAdmin, identity.Role)
	assert.Empty(t, identity.TokenID)
	assert.Nil(t, identity.Selectors)

	scopes, err := RoleScopes(RoleAdmin)
	require.NoError(t, err)
	assert.ElementsMatch(t, scopes, identity.Scopes)

	for _, scope := range scopes {
		assert.True(t, identity.HasScope(scope), string(scope))
	}
	assert.NoError(t, identity.AuthorizeTask("svc-any", "task-any", nil))
}

func TestIdentityFromContext(t *testing.T) {
	_, ok := IdentityFrom(context.Background())
	assert.False(t, ok)

	want := Identity{Type: "token", TokenID: "tok_01234567", Name: "ci", Role: RoleReader}
	got, ok := IdentityFrom(withIdentity(context.Background(), want))
	require.True(t, ok)
	assert.Equal(t, want, got)
}

// TestForbiddenScopeDetails pins the one detail field a client can rely on when
// a token lacks a scope.
func TestForbiddenScopeDetails(t *testing.T) {
	err := forbiddenScope(ScopeAuditRead)

	require.Equal(t, output.CodeForbidden, requireErrorCode(t, err))

	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, "audit:read", typed.Details["requiredScope"])
	assert.NotContains(t, typed.Message, "audit:read", "the message must stay human, the detail is the contract")
}

// TestTaskScopedRoutesDenyWithoutIdentity pins deny-by-default at the route
// boundary for the task-scoped operations: a request with no token is 401, and a
// token that lacks the operation's scope is 403 before any handler runs. The
// handlers never see a zero identity, because the boundary refuses it first.
func TestTaskScopedRoutesDenyWithoutIdentity(t *testing.T) {
	api := newTestAPI(t, &fakeClient{
		task: v1.Task{ID: "task-1", ServiceID: "svc-1", NodeID: "node-1"},
	})

	cases := []struct {
		name   string
		method string
		target string
	}{
		{"exec", http.MethodPost, "/v1/tasks/task-1/exec"},
		{"files", http.MethodGet, "/v1/tasks/task-1/files?path=/etc/hostname"},
		{"snapshots", http.MethodGet, "/v1/tasks/task-1/snapshots"},
	}

	for _, tc := range cases {
		t.Run(tc.name+" without a token", func(t *testing.T) {
			rec := api.do(t, tc.method, tc.target)
			require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
			assert.Equal(t, string(output.CodeUnauthenticated), decodeError(t, rec).Error.Code)
		})
		t.Run(tc.name+" without the scope", func(t *testing.T) {
			// The suite's default token is a reader: it has no exec, files or
			// snapshots scope.
			rec := api.do(t, tc.method, tc.target, withToken(api.token))
			require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			assert.Equal(t, string(output.CodeForbidden), decodeError(t, rec).Error.Code)
		})
	}
}
