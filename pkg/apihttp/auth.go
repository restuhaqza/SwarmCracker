// This file owns authorization: the scope taxonomy, role expansion, task
// selectors and the per-request Identity.
//
// Every route names the scope it needs; HasScope is the only check a handler
// performs. Deny by default is the rule: a scope that is not on the identity is
// a 403, and an unknown role never loads from the token store at all.
package apihttp

import (
	"context"

	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// Role names. A token role expands to a scope set at mint time and on load of a
// phase-1 token file.
const (
	// RoleReader reads the cluster and nothing else: dashboards and read-only CI.
	RoleReader = "reader"
	// RoleRunner is a reader that may also exec, transfer files and snapshot
	// tasks inside the VMs it was granted through a task selector.
	RoleRunner = "runner"
	// RoleOperator is a runner that may also write services, networks, configs
	// and secrets: deploys.
	RoleOperator = "operator"
	// RoleAdmin holds every scope, including node writes, token administration
	// and the audit log.
	RoleAdmin = "admin"
)

// Scope is one authorization unit. The set is closed: a token may only carry a
// scope RoleScopes produced for its role.
type Scope string

// The v1 scope taxonomy.
const (
	ScopeServicesRead      Scope = "services:read"
	ScopeServicesWrite     Scope = "services:write"
	ScopeMigrationsWrite   Scope = "migrations:write"
	ScopeTasksRead         Scope = "tasks:read"
	ScopeTasksExec         Scope = "tasks:exec"
	ScopeTasksFiles        Scope = "tasks:files"
	ScopeTasksLogs         Scope = "tasks:logs"
	ScopeTasksSnapshots    Scope = "tasks:snapshots"
	ScopeNodesRead         Scope = "nodes:read"
	ScopeNodesWrite        Scope = "nodes:write"
	ScopeNetworksRead      Scope = "networks:read"
	ScopeNetworksWrite     Scope = "networks:write"
	ScopeConfigsRead       Scope = "configs:read"
	ScopeConfigsReadValues Scope = "configs:read-values"
	ScopeConfigsWrite      Scope = "configs:write"
	ScopeSecretsRead       Scope = "secrets:read"
	ScopeSecretsWrite      Scope = "secrets:write"
	ScopeEventsRead        Scope = "events:read"
	ScopeClusterRead       Scope = "cluster:read"
	ScopeTokensAdmin       Scope = "tokens:admin"
	ScopeAuditRead         Scope = "audit:read"
)

// readerScopes is the base of every role. Order is the documentation order: it
// is what `api token list` prints for a token that was never narrowed.
var readerScopes = []Scope{
	ScopeServicesRead,
	ScopeTasksRead,
	ScopeTasksLogs,
	ScopeNodesRead,
	ScopeNetworksRead,
	ScopeConfigsRead,
	ScopeSecretsRead,
	ScopeEventsRead,
	ScopeClusterRead,
}

var runnerScopes = concatScopes(readerScopes,
	ScopeTasksExec, ScopeTasksFiles, ScopeTasksSnapshots)

var operatorScopes = concatScopes(runnerScopes,
	ScopeServicesWrite, ScopeNetworksWrite, ScopeConfigsWrite,
	ScopeConfigsReadValues, ScopeSecretsWrite)

var adminScopes = concatScopes(operatorScopes,
	ScopeNodesWrite, ScopeTokensAdmin, ScopeAuditRead,
	// A migration replaces a service's task and moves a guest's memory image
	// between hosts, so it outranks a deploy: admin only.
	ScopeMigrationsWrite)

// roleScopes is the role → scope expansion table.
var roleScopes = map[string][]Scope{
	RoleReader:   readerScopes,
	RoleRunner:   runnerScopes,
	RoleOperator: operatorScopes,
	RoleAdmin:    adminScopes,
}

// concatScopes returns a new slice so no role table aliases another.
func concatScopes(base []Scope, extra ...Scope) []Scope {
	out := make([]Scope, 0, len(base)+len(extra))
	out = append(out, base...)
	return append(out, extra...)
}

// RoleScopes expands a role to its scope set. The table is copied on the way
// out, so a caller cannot narrow a role for another caller. An unknown role is
// invalid: fail closed rather than issue a token with an empty scope set.
func RoleScopes(role string) ([]Scope, error) {
	scopes, ok := roleScopes[role]
	if !ok {
		return nil, output.NewErrorf(output.CodeInvalid,
			"unsupported token role %q (want %q, %q, %q or %q)",
			role, RoleReader, RoleRunner, RoleOperator, RoleAdmin)
	}
	return append([]Scope(nil), scopes...), nil
}

// KnownScope reports whether s is part of the v1 taxonomy.
func KnownScope(s string) bool {
	for _, scope := range adminScopes {
		if string(scope) == s {
			return true
		}
	}
	return false
}

// TaskSelectors narrows a token to specific tasks. A nil *TaskSelectors means
// "no restriction" for an operator or admin; a runner must carry at least one
// entry.
//
// Selectors gate the task-scoped operations — exec, files and snapshots — not the
// read routes: a runner still lists every task tasks:read covers.
//
// The three dimensions are ANDed and an entry matches on its own: a service ID
// list matches any of its members, a task ID list likewise, and every label must
// match.
type TaskSelectors struct {
	ServiceIDs []string          `json:"serviceIds,omitempty"`
	TaskIDs    []string          `json:"taskIds,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
}

// Empty reports whether the selector restricts nothing.
func (s TaskSelectors) Empty() bool {
	return len(s.ServiceIDs) == 0 && len(s.TaskIDs) == 0 && len(s.Labels) == 0
}

// Matches reports whether the task belongs to the selector. An empty selector
// matches nothing: this method is the fail-closed primitive phase-3 handlers
// call, so "no restriction" must be decided before calling it.
func (s TaskSelectors) Matches(serviceID, taskID string, serviceLabels map[string]string) bool {
	if len(s.ServiceIDs) > 0 && !containsString(s.ServiceIDs, serviceID) {
		return false
	}
	if len(s.TaskIDs) > 0 && !containsString(s.TaskIDs, taskID) {
		return false
	}
	if len(s.Labels) > 0 {
		for key, want := range s.Labels {
			if got, ok := serviceLabels[key]; !ok || got != want {
				return false
			}
		}
		return true
	}

	return !s.Empty()
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// Identity is the authenticated caller of one request.
type Identity struct {
	// Type is "token" (TCP, bearer authenticated) or "local" (Unix socket,
	// trusted by file mode).
	Type string
	// TokenID and Name identify the token; both are empty for a local request.
	TokenID string
	Name    string
	Role    string
	// Scopes is exactly what the token store issued: the role expansion at mint
	// time, narrowed by TokenOptions.Scopes when the operator asked for less.
	Scopes []Scope
	// Selectors restricts which tasks a runner may touch. Nil means "all".
	Selectors *TaskSelectors
}

// HasScope is the single authorization check handlers perform.
func (i Identity) HasScope(scope Scope) bool {
	for _, have := range i.Scopes {
		if have == scope {
			return true
		}
	}
	return false
}

// AuthorizeTask decides whether this identity may act on one task. Phase-2
// routes do not call it; it is the contract the phase-3 exec, files and snapshot
// handlers use.
//
// A runner without a selector fails closed, because tasks:exec alone would be
// cluster-wide root inside every guest. An operator or admin without a selector
// means all tasks.
func (i Identity) AuthorizeTask(serviceID, taskID string, serviceLabels map[string]string) error {
	empty := i.Selectors == nil || i.Selectors.Empty()
	if i.Role == RoleRunner && empty {
		return selectorRequired()
	}
	if empty {
		return nil
	}
	if i.Selectors.Matches(serviceID, taskID, serviceLabels) {
		return nil
	}

	return output.NewError(output.CodeForbidden,
		"token task selectors do not match this task").
		WithDetails(map[string]any{"reason": "selector_mismatch"})
}

// LocalIdentity is the identity of a request over the trusted Unix socket: the
// socket's file mode is the credential, so it is a local administrator.
func LocalIdentity() Identity {
	return Identity{
		Type:   "local",
		Role:   RoleAdmin,
		Scopes: append([]Scope(nil), adminScopes...),
	}
}

// identityKey is the context key for the request Identity.
type identityKey struct{}

// withIdentity stores identity on ctx.
func withIdentity(ctx context.Context, identity Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, identity)
}

// IdentityFrom returns the Identity the middleware stored for this request.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(identityKey{}).(Identity)
	return identity, ok
}

// forbiddenScope is the 403 for a token that lacks scope.
func forbiddenScope(scope Scope) error {
	return output.NewError(output.CodeForbidden,
		"token lacks the scope required by this route").
		WithDetails(map[string]any{"requiredScope": string(scope)})
}

// selectorRequired is the 403 for a runner token that carries no selector.
func selectorRequired() error {
	return output.NewError(output.CodeForbidden,
		"runner tokens must carry a task selector").
		WithDetails(map[string]any{"reason": "selector_required"})
}
