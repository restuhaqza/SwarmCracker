package broker

// The re-anneal is the only migration operation that runs commands on a node, so
// its tests are about the exact commands and the two answers that are not
// failures: an entry that was already gone, and a flood entry that already
// exists. Everything else - the bridge, the overlay interface name, the peers -
// is this node's own configuration, and no test lets a request name it.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// nodeCommand is one iproute2 command a re-anneal ran.
type nodeCommand struct {
	name string
	args []string
}

// String renders the command the way an operator would type it.
func (c nodeCommand) String() string {
	line := c.name
	for _, arg := range c.args {
		line += " " + arg
	}
	return line
}

// recordNodeCommands replaces the command runner for one test and returns the
// commands that ran. answer may script a node's replies; nil answers success.
func recordNodeCommands(
	t *testing.T, answer func(cmd nodeCommand) ([]byte, error),
) *[]nodeCommand {
	t.Helper()

	original := runNodeNetworkCommand
	var (
		mu    sync.Mutex
		calls []nodeCommand
	)
	runNodeNetworkCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		command := nodeCommand{name: name, args: append([]string(nil), args...)}
		mu.Lock()
		calls = append(calls, command)
		mu.Unlock()
		if answer == nil {
			return nil, nil
		}
		return answer(command)
	}
	t.Cleanup(func() { runNodeNetworkCommand = original })

	return &calls
}

// reannealBody is the document the manager sends.
func reannealBody(t *testing.T, ip, mac string) []byte {
	t.Helper()
	body, err := json.Marshal(migrationReannealRequest{IP: ip, MAC: mac})
	require.NoError(t, err)
	return body
}

// TestReannealForgetsTheGuestAndKeepsTheFlood pins the three steps and the device
// each names: the neighbour and FDB entries on the bridge this node runs the
// guest on, then the all-zero flood entry per peer on the overlay interface the
// bridge derives.
func TestReannealForgetsTheGuestAndKeepsTheFlood(t *testing.T) {
	calls := recordNodeCommands(t, nil)

	handler := NewMigrationReannealHandler(MigrationHandlerConfig{
		BridgeName: "swarm-br0",
		VXLANPeers: []string{"192.168.56.12", "192.168.56.13"},
	})

	rec := serveMigration(t, handler, Task{}, http.MethodPost,
		"/v1/tasks/pending-restore/migration/reanneal",
		reannealBody(t, "10.0.1.7", "AA:FC:11:22:33:44"))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	want := []string{
		"ip neigh del 10.0.1.7 dev swarm-br0",
		// The MAC is normalized: the manager sends back what the node accepted,
		// and the bridge FDB is keyed by the lowercase form.
		"bridge fdb del aa:fc:11:22:33:44 dev swarm-br0",
		"bridge fdb append 00:00:00:00:00:00 dev swarm-br0-vxlan dst 192.168.56.12 self permanent",
		"bridge fdb append 00:00:00:00:00:00 dev swarm-br0-vxlan dst 192.168.56.13 self permanent",
	}
	got := make([]string, 0, len(*calls))
	for _, call := range *calls {
		got = append(got, call.String())
	}
	assert.Equal(t, want, got)
}

// TestReannealTreatsAMissingEntryAsSuccess pins the rule the brief asks for: a
// peer that never learned the guest has nothing to forget, so iproute2's "no such
// file or directory" is the same answer as a successful delete - and the flood
// entries are still re-added, because that is the half that makes the next ARP
// reach the new node.
func TestReannealTreatsAMissingEntryAsSuccess(t *testing.T) {
	recordNodeCommands(t, func(cmd nodeCommand) ([]byte, error) {
		if cmd.args[0] == "fdb" && cmd.args[1] == "append" {
			return nil, nil
		}
		return []byte("RTNETLINK answers: No such file or directory"), fmt.Errorf("exit status 2")
	})

	handler := NewMigrationReannealHandler(MigrationHandlerConfig{
		BridgeName: "swarm-br0",
		VXLANPeers: []string{"192.168.56.12"},
	})

	rec := serveMigration(t, handler, Task{}, http.MethodPost,
		"/v1/tasks/pending-restore/migration/reanneal",
		reannealBody(t, "10.0.1.7", "aa:fc:11:22:33:44"))
	assert.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
}

// TestReannealTreatsAnExistingFloodEntryAsSuccess pins idempotence: the flood
// entry is re-added per peer, and the kernel's "File exists" means it never left.
func TestReannealTreatsAnExistingFloodEntryAsSuccess(t *testing.T) {
	recordNodeCommands(t, func(cmd nodeCommand) ([]byte, error) {
		if cmd.args[0] == "fdb" && cmd.args[1] == "append" {
			return []byte("RTNETLINK answers: File exists"), fmt.Errorf("exit status 2")
		}
		return nil, nil
	})

	handler := NewMigrationReannealHandler(MigrationHandlerConfig{
		BridgeName: "swarm-br0",
		VXLANPeers: []string{"192.168.56.12"},
	})

	rec := serveMigration(t, handler, Task{}, http.MethodPost,
		"/v1/tasks/pending-restore/migration/reanneal",
		reannealBody(t, "10.0.1.7", "aa:fc:11:22:33:44"))
	assert.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
}

// TestReannealReportsANodeThatCannotForget pins the one failure that is reported:
// a bridge that is not there is this node's misconfiguration, not an entry that
// was already gone, so the manager is told which peer did not re-anneal.
func TestReannealReportsANodeThatCannotForget(t *testing.T) {
	calls := recordNodeCommands(t, func(nodeCommand) ([]byte, error) {
		return []byte(`Cannot find device "swarm-br0"`), fmt.Errorf("exit status 1")
	})

	handler := NewMigrationReannealHandler(MigrationHandlerConfig{BridgeName: "swarm-br0"})
	rec := serveMigration(t, handler, Task{}, http.MethodPost,
		"/v1/tasks/pending-restore/migration/reanneal",
		reannealBody(t, "10.0.1.7", "aa:fc:11:22:33:44"))

	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	doc := decodeMigrationError(t, rec)
	assert.Equal(t, string(output.CodeUnavailable), doc.Error.Code)
	assert.Equal(t, "reanneal_failed", doc.Error.Details["reason"])
	assert.Len(t, *calls, 1, "the first failed delete stops the rest")
}

// TestReannealSkipsTheOverlayWithoutPeers pins the non-overlay node: it forgets the
// guest on its own bridge and has no flood entry to keep.
func TestReannealSkipsTheOverlayWithoutPeers(t *testing.T) {
	calls := recordNodeCommands(t, nil)

	handler := NewMigrationReannealHandler(MigrationHandlerConfig{BridgeName: "swarm-br0"})
	rec := serveMigration(t, handler, Task{}, http.MethodPost,
		"/v1/tasks/pending-restore/migration/reanneal",
		reannealBody(t, "10.0.1.7", "aa:fc:11:22:33:44"))

	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Len(t, *calls, 2, "the two deletes, and no overlay command")
}

// TestReannealRefusesABadRequest pins the body rules: the document carries the two
// values the node cannot infer, and anything else - a missing field, an address
// that is not one, a MAC that is not 48 bits, a field this build does not know -
// is refused before a command runs.
func TestReannealRefusesABadRequest(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		param string
	}{
		{name: "no body", body: `{}`, param: "ip"},
		{name: "no mac", body: `{"ip":"10.0.1.7"}`, param: "mac"},
		{name: "no ip", body: `{"mac":"aa:fc:11:22:33:44"}`, param: "ip"},
		{name: "not an ip", body: `{"ip":"ten","mac":"aa:fc:11:22:33:44"}`, param: "ip"},
		{name: "not a 48-bit mac", body: `{"ip":"10.0.1.7","mac":"aa:fc:11:22:33"}`, param: "mac"},
		{name: "not a mac", body: `{"ip":"10.0.1.7","mac":"nope"}`, param: "mac"},
		{name: "unknown field", body: `{"ip":"10.0.1.7","mac":"aa:fc:11:22:33:44","bridge":"br0"}`},
		{name: "not json", body: `[]`},
		{name: "two documents", body: `{"ip":"10.0.1.7","mac":"aa:fc:11:22:33:44"}{}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := recordNodeCommands(t, nil)
			handler := NewMigrationReannealHandler(MigrationHandlerConfig{BridgeName: "swarm-br0"})

			rec := serveMigration(t, handler, Task{}, http.MethodPost,
				"/v1/tasks/pending-restore/migration/reanneal", []byte(tc.body))
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

			doc := decodeMigrationError(t, rec)
			assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
			if tc.param != "" {
				assert.Equal(t, tc.param, doc.Error.Details["parameter"])
			}
			assert.Empty(t, *calls, "a refused re-anneal runs no command")
		})
	}
}

// TestReannealNeedsABridge pins the unconfigured node: it cannot say which device
// a guest was on, so it must not answer as though it had forgotten one.
func TestReannealNeedsABridge(t *testing.T) {
	calls := recordNodeCommands(t, nil)

	handler := NewMigrationReannealHandler(MigrationHandlerConfig{})
	rec := serveMigration(t, handler, Task{}, http.MethodPost,
		"/v1/tasks/pending-restore/migration/reanneal",
		reannealBody(t, "10.0.1.7", "aa:fc:11:22:33:44"))

	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	doc := decodeMigrationError(t, rec)
	assert.Equal(t, string(output.CodeUnavailable), doc.Error.Code)
	assert.Equal(t, "bridge_unconfigured", doc.Error.Details["reason"])
	assert.Empty(t, *calls)
}

// TestReannealRefusesAnotherMethod pins the operation's one method through the
// same spec the server routes with.
func TestReannealRefusesAnotherMethod(t *testing.T) {
	handler := NewMigrationReannealHandler(MigrationHandlerConfig{BridgeName: "swarm-br0"})

	rec := serveMigration(t, handler, Task{}, http.MethodGet,
		"/v1/tasks/pending-restore/migration/reanneal", nil)
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code, rec.Body.String())
	assert.Equal(t, http.MethodPost, rec.Header().Get("Allow"))
}

// TestReannealIsArtifactScoped pins the routing rule: the task in the path is not
// resolved, because the guest's owner changed when it moved.
func TestReannealIsArtifactScoped(t *testing.T) {
	spec, known := operationSpecs[MigrationReannealOperation]
	require.True(t, known, "the operation is registered")
	assert.True(t, spec.artifactScoped, "the task in the route is never resolved")
	assert.True(t, spec.allows(http.MethodPost))
}

// TestManagerReannealSendsTheGuestToThePeer pins the manager side: the node, the
// guest and the MAC cross as one POST to the placeholder route, and the node's
// typed refusal is the caller's error.
func TestManagerReannealSendsTheGuestToThePeer(t *testing.T) {
	pki := newTestPKI(t)

	var (
		gotPath, gotMethod, gotContentType string
		gotBody                            migrationReannealRequest
	)
	_, baseURL := startTestBroker(t, pki, &fakeTaskSource{}, map[string]Handler{
		MigrationReannealOperation: func(_ context.Context, _ Task, w http.ResponseWriter, r *http.Request) {
			gotPath, gotMethod = r.URL.Path, r.Method
			gotContentType = r.Header.Get("Content-Type")
			require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
			w.WriteHeader(http.StatusNoContent)
		},
	})

	mgr := newMigrationTestManager(t, pki, baseURL, 0)
	require.NoError(t, mgr.Reanneal(context.Background(), "node-2", "10.0.1.7", "AA:FC:11:22:33:44"))

	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, "/v1/tasks/"+migrationDirectiveRouteTask+"/"+MigrationReannealOperation, gotPath)
	assert.Equal(t, migrationPrepareMediaType, gotContentType)
	assert.Equal(t, "10.0.1.7", gotBody.IP)
	assert.Equal(t, "aa:fc:11:22:33:44", gotBody.MAC, "the node accepts the MAC in one form")
}

// TestManagerReannealRefusesWhatIsNotAGuest pins the two shapes the manager can
// check before it dials a peer, so a malformed value is not refused eight times.
func TestManagerReannealRefusesWhatIsNotAGuest(t *testing.T) {
	pki := newTestPKI(t)
	var called bool
	_, baseURL := startTestBroker(t, pki, &fakeTaskSource{}, map[string]Handler{
		MigrationReannealOperation: func(_ context.Context, _ Task, _ http.ResponseWriter, _ *http.Request) {
			called = true
		},
	})
	mgr := newMigrationTestManager(t, pki, baseURL, 0)

	for _, tc := range []struct{ ip, mac string }{
		{ip: "ten", mac: "aa:fc:11:22:33:44"},
		{ip: "10.0.1.7", mac: "aa:fc:11:22:33"},
	} {
		err := mgr.Reanneal(context.Background(), "node-2", tc.ip, tc.mac)
		var typed *output.Error
		require.ErrorAs(t, err, &typed)
		assert.Equal(t, output.CodeInvalid, typed.Code)
	}
	assert.False(t, called, "a malformed guest never becomes a request")
}
