package broker

// The re-anneal is the only migration operation that runs commands on a node, so
// its tests are about the exact commands and the answers that are not failures: an
// entry that was already gone, a flood entry that already exists, and a node that
// runs no overlay at all. Everything else - the bridge, the overlay interface name,
// the peers - is this node's own configuration or derived from the attachment's
// network ID, and no test lets a request name a device.

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
	"github.com/restuhaqza/swarmcracker/pkg/types"
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
func reannealBody(t *testing.T, networkID, ip, mac string) []byte {
	t.Helper()
	body, err := json.Marshal(migrationReannealRequest{NetworkID: networkID, IP: ip, MAC: mac})
	require.NoError(t, err)
	return body
}

// reannealCommands renders what ran, for a whole-slice assertion.
func reannealCommands(calls *[]nodeCommand) []string {
	got := make([]string, 0, len(*calls))
	for _, call := range *calls {
		got = append(got, call.String())
	}
	return got
}

// TestReannealForgetsTheGuestOnTheOverlayPort pins the steps and the device each
// names: the neighbour entry and the guest's MAC on the *overlay port* (a bridge FDB
// entry belongs to the port that learned it, so naming the bridge device would
// delete the bridge's own entry and leave the one pointing at the old node), the
// bridge's own entry as an extra, then the all-zero flood entry per peer.
func TestReannealForgetsTheGuestOnTheOverlayPort(t *testing.T) {
	calls := recordNodeCommands(t, nil)

	handler := NewMigrationReannealHandler(MigrationHandlerConfig{
		BridgeName: "swarm-br0",
		VXLANPeers: []string{"192.168.56.12", "192.168.56.13"},
	})

	rec := serveMigration(t, handler, Task{}, http.MethodPost,
		"/v1/tasks/pending-restore/migration/reanneal",
		reannealBody(t, "net-flat", "10.0.1.7", "AA:FC:11:22:33:44"))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	want := []string{
		"ip neigh del 10.0.1.7 dev swarm-br0",
		// The entry that black-holed the guest: its MAC on the overlay port.
		"bridge fdb del aa:fc:11:22:33:44 dev swarm-br0-vxlan",
		// The MAC is normalized: the manager sends back what the node accepted,
		// and the bridge FDB is keyed by the lowercase form.
		"bridge fdb del aa:fc:11:22:33:44 dev swarm-br0",
		"bridge fdb append 00:00:00:00:00:00 dev swarm-br0-vxlan dst 192.168.56.12 self permanent",
		"bridge fdb append 00:00:00:00:00:00 dev swarm-br0-vxlan dst 192.168.56.13 self permanent",
	}
	assert.Equal(t, want, reannealCommands(calls))
}

// TestReannealUsesTheAttachmentNetworksBridge pins the C2 rule: a guest attached to
// a user-defined network was never on the node's flat bridge, so the request's
// network ID is resolved here and the bridge is derived by the same call the restore
// path makes (pkg/network's AttachmentBridgeName). Every command then names that
// bridge, including its overlay interface.
func TestReannealUsesTheAttachmentNetworksBridge(t *testing.T) {
	calls := recordNodeCommands(t, nil)

	// A user-defined overlay network: AttachmentBridgeName derives "br-<id[:12]>",
	// and the overlay interface of a 15-byte bridge name is the kernel-safe digest
	// form (a 15-byte name plus "-vxlan" exceeds IFNAMSIZ).
	userNetwork := &types.Network{
		ID:   "net-0123456789abcdef",
		Spec: types.NetworkSpec{Driver: "overlay", Subnet: "10.77.0.0/24"},
	}
	handler := NewMigrationReannealHandler(MigrationHandlerConfig{
		BridgeName: "swarm-br0",
		Subnet:     "10.0.1.0/24",
		NetworkByID: func(networkID string) (*types.Network, bool) {
			if networkID == userNetwork.ID {
				return userNetwork, true
			}
			return nil, false
		},
	})

	rec := serveMigration(t, handler, Task{}, http.MethodPost,
		"/v1/tasks/pending-restore/migration/reanneal",
		reannealBody(t, userNetwork.ID, "10.77.0.9", "aa:fc:11:22:33:44"))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	assert.Equal(t, []string{
		"ip neigh del 10.77.0.9 dev br-net-01234567",
		"bridge fdb del aa:fc:11:22:33:44 dev vx-82a4b27136fd",
		"bridge fdb del aa:fc:11:22:33:44 dev br-net-01234567",
	}, reannealCommands(calls), "the attachment's network decides the device, not --bridge-name")
}

// TestReannealUsesANetworkRecordedBridge pins the other half of the derivation: a
// network whose driver config names its bridge is forgotten from that bridge, which
// is where its TAPs actually joined.
func TestReannealUsesANetworkRecordedBridge(t *testing.T) {
	calls := recordNodeCommands(t, nil)

	handler := NewMigrationReannealHandler(MigrationHandlerConfig{
		BridgeName: "swarm-br0",
		NetworkByID: func(string) (*types.Network, bool) {
			return &types.Network{
				ID: "net-backend",
				Spec: types.NetworkSpec{
					Driver:       "bridge",
					Subnet:       "10.88.0.0/24",
					DriverConfig: &types.DriverConfig{Bridge: &types.BridgeConfig{Name: "br-user"}},
				},
			}, true
		},
	})

	rec := serveMigration(t, handler, Task{}, http.MethodPost,
		"/v1/tasks/pending-restore/migration/reanneal",
		reannealBody(t, "net-backend", "10.88.0.4", "aa:fc:11:22:33:44"))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	assert.Equal(t, []string{
		"ip neigh del 10.88.0.4 dev br-user",
		"bridge fdb del aa:fc:11:22:33:44 dev br-user-vxlan",
		"bridge fdb del aa:fc:11:22:33:44 dev br-user",
	}, reannealCommands(calls))
}

// TestReannealKeepsTheFlatBridgeForANetworkOfTheFlatSubnet pins the exception: the
// ingress network is carved from --subnet by design, so its attachments stayed on
// the node's bridge and a re-anneal must not invent a second device for them.
func TestReannealKeepsTheFlatBridgeForANetworkOfTheFlatSubnet(t *testing.T) {
	calls := recordNodeCommands(t, nil)

	handler := NewMigrationReannealHandler(MigrationHandlerConfig{
		BridgeName: "swarm-br0",
		Subnet:     "10.0.1.0/24",
		NetworkByID: func(string) (*types.Network, bool) {
			return &types.Network{
				ID:   "net-ingress-0123456789",
				Spec: types.NetworkSpec{Driver: "overlay", Subnet: "10.0.1.0/24"},
			}, true
		},
	})

	rec := serveMigration(t, handler, Task{}, http.MethodPost,
		"/v1/tasks/pending-restore/migration/reanneal",
		reannealBody(t, "net-ingress-0123456789", "10.0.1.7", "aa:fc:11:22:33:44"))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Equal(t, "ip neigh del 10.0.1.7 dev swarm-br0", (*calls)[0].String())
}

// TestReannealTreatsAMissingEntryAsSuccess pins the rule the brief asks for: a peer
// that never learned the guest has nothing to forget, so iproute2's "no such file or
// directory" is the same answer as a successful delete - and the flood entries are
// still re-added, because that is the half that makes the next ARP reach the new
// node.
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
		reannealBody(t, "net-flat", "10.0.1.7", "aa:fc:11:22:33:44"))
	assert.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
}

// TestReannealTreatsAnAlreadyGoneNeighbourAsSuccess pins iproute2's second answer
// for an absent entry: a neighbour that is already gone is "No such process".
func TestReannealTreatsAnAlreadyGoneNeighbourAsSuccess(t *testing.T) {
	recordNodeCommands(t, func(cmd nodeCommand) ([]byte, error) {
		if cmd.args[0] == "neigh" {
			return []byte("RTNETLINK answers: No such process"), fmt.Errorf("exit status 2")
		}
		return nil, nil
	})

	handler := NewMigrationReannealHandler(MigrationHandlerConfig{BridgeName: "swarm-br0"})
	rec := serveMigration(t, handler, Task{}, http.MethodPost,
		"/v1/tasks/pending-restore/migration/reanneal",
		reannealBody(t, "net-flat", "10.0.1.7", "aa:fc:11:22:33:44"))
	assert.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
}

// TestReannealDoesNotReadAnyOtherWordAsAbsent pins the narrowed rule: only
// iproute2's two answers for an entry that is not there are success. "not found" and
// "does not exist" also appear in answers about devices and sysctls, and a node that
// read one of those as absent would report an entry gone that it never touched.
func TestReannealDoesNotReadAnyOtherWordAsAbsent(t *testing.T) {
	for _, answer := range []string{
		"RTNETLINK answers: No such device",
		"Object \"fdb\" is unknown, try \"bridge help\".",
		"entry not found",
		"the bridge does not exist",
	} {
		t.Run(answer, func(t *testing.T) {
			recordNodeCommands(t, func(cmd nodeCommand) ([]byte, error) {
				if cmd.args[0] == "neigh" {
					return []byte(answer), fmt.Errorf("exit status 2")
				}
				return nil, nil
			})

			handler := NewMigrationReannealHandler(MigrationHandlerConfig{BridgeName: "swarm-br0"})
			rec := serveMigration(t, handler, Task{}, http.MethodPost,
				"/v1/tasks/pending-restore/migration/reanneal",
				reannealBody(t, "net-flat", "10.0.1.7", "aa:fc:11:22:33:44"))

			require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
			doc := decodeMigrationError(t, rec)
			assert.Equal(t, string(output.CodeInternal), doc.Error.Code)
			assert.Equal(t, "reanneal_failed", doc.Error.Details["reason"])
		})
	}
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
		reannealBody(t, "net-flat", "10.0.1.7", "aa:fc:11:22:33:44"))
	assert.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
}

// TestReannealToleratesANodeWithoutAnOverlay pins the one device answer that is not
// an absent entry and is still not a failure: a node that runs no overlay has no
// overlay interface at all, so it holds no port entry to forget either.
func TestReannealToleratesANodeWithoutAnOverlay(t *testing.T) {
	calls := recordNodeCommands(t, func(cmd nodeCommand) ([]byte, error) {
		if cmd.name == "bridge" && cmd.args[1] == "del" && cmd.args[4] == "swarm-br0-vxlan" {
			return []byte(`Cannot find device "swarm-br0-vxlan"`), fmt.Errorf("exit status 1")
		}
		return nil, nil
	})

	handler := NewMigrationReannealHandler(MigrationHandlerConfig{BridgeName: "swarm-br0"})
	rec := serveMigration(t, handler, Task{}, http.MethodPost,
		"/v1/tasks/pending-restore/migration/reanneal",
		reannealBody(t, "net-flat", "10.0.1.7", "aa:fc:11:22:33:44"))

	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Len(t, *calls, 3, "the neighbour delete, the overlay delete and the extra")
}

// TestReannealLeavesTheBridgesOwnEntryAlone pins the extra's status: the bridge's
// own entry is not the one that carried the guest, so a node that cannot delete it
// has still re-annealed and says so.
func TestReannealLeavesTheBridgesOwnEntryAlone(t *testing.T) {
	calls := recordNodeCommands(t, func(cmd nodeCommand) ([]byte, error) {
		if cmd.name == "bridge" && cmd.args[1] == "del" && cmd.args[4] == "swarm-br0" {
			return []byte(`Cannot find device "swarm-br0"`), fmt.Errorf("exit status 1")
		}
		return nil, nil
	})

	handler := NewMigrationReannealHandler(MigrationHandlerConfig{
		BridgeName: "swarm-br0",
		VXLANPeers: []string{"192.168.56.12"},
	})
	rec := serveMigration(t, handler, Task{}, http.MethodPost,
		"/v1/tasks/pending-restore/migration/reanneal",
		reannealBody(t, "net-flat", "10.0.1.7", "aa:fc:11:22:33:44"))

	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Len(t, *calls, 4, "the flood entry was still re-added")
}

// TestReannealReportsANodeThatCannotForget pins the one failure that is reported: a
// bridge that is not there is this node's misconfiguration, not an entry that was
// already gone, so the manager is told which node did not re-anneal - as an internal
// failure, because the same command would fail the same way and nothing retries it.
func TestReannealReportsANodeThatCannotForget(t *testing.T) {
	calls := recordNodeCommands(t, func(nodeCommand) ([]byte, error) {
		return []byte(`Cannot find device "swarm-br0"`), fmt.Errorf("exit status 1")
	})

	handler := NewMigrationReannealHandler(MigrationHandlerConfig{BridgeName: "swarm-br0"})
	rec := serveMigration(t, handler, Task{}, http.MethodPost,
		"/v1/tasks/pending-restore/migration/reanneal",
		reannealBody(t, "net-flat", "10.0.1.7", "aa:fc:11:22:33:44"))

	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	doc := decodeMigrationError(t, rec)
	assert.Equal(t, string(output.CodeInternal), doc.Error.Code)
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
		reannealBody(t, "net-flat", "10.0.1.7", "aa:fc:11:22:33:44"))

	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Len(t, *calls, 3, "the neighbour delete, the overlay delete and the extra, and no flood append")
}

// TestReannealRefusesABadRequest pins the body rules: the document carries the three
// values the node cannot infer, and anything else - a missing field, an address that
// is not one, a MAC that is not 48 bits, a network ID that is a path, a field this
// build does not know - is refused before a command runs.
func TestReannealRefusesABadRequest(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		param string
	}{
		{name: "no body", body: `{}`, param: "networkId"},
		{name: "no network", body: `{"ip":"10.0.1.7","mac":"aa:fc:11:22:33:44"}`, param: "networkId"},
		{name: "a network path", body: `{"networkId":"net/1","ip":"10.0.1.7","mac":"aa:fc:11:22:33:44"}`, param: "networkId"},
		{name: "a relative network", body: `{"networkId":"..","ip":"10.0.1.7","mac":"aa:fc:11:22:33:44"}`, param: "networkId"},
		{name: "no mac", body: `{"networkId":"net-1","ip":"10.0.1.7"}`, param: "mac"},
		{name: "no ip", body: `{"networkId":"net-1","mac":"aa:fc:11:22:33:44"}`, param: "ip"},
		{name: "not an ip", body: `{"networkId":"net-1","ip":"ten","mac":"aa:fc:11:22:33:44"}`, param: "ip"},
		{name: "not a 48-bit mac", body: `{"networkId":"net-1","ip":"10.0.1.7","mac":"aa:fc:11:22:33"}`, param: "mac"},
		{name: "not a mac", body: `{"networkId":"net-1","ip":"10.0.1.7","mac":"nope"}`, param: "mac"},
		{name: "unknown field", body: `{"networkId":"net-1","ip":"10.0.1.7","mac":"aa:fc:11:22:33:44","bridge":"br0"}`},
		{name: "not json", body: `[]`},
		{name: "two documents", body: `{"networkId":"net-1","ip":"10.0.1.7","mac":"aa:fc:11:22:33:44"}{}`},
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
		reannealBody(t, "net-1", "10.0.1.7", "aa:fc:11:22:33:44"))

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

// TestMigrationArtifactScopedOperationsAreCounted pins the roster the server's own
// comment describes: export is the only task-scoped migration operation, and the
// other six answer from the IDs they carry.
func TestMigrationArtifactScopedOperationsAreCounted(t *testing.T) {
	migration := []string{
		MigrationExportOperation,
		MigrationFileOperation,
		MigrationStageOperation,
		MigrationDiscardOperation,
		MigrationPrepareOperation,
		MigrationUnprepareOperation,
		MigrationReannealOperation,
	}
	scoped := 0
	for _, operation := range migration {
		if operationSpecs[operation].artifactScoped {
			scoped++
		}
	}
	assert.Equal(t, 6, scoped, "six migration operations are artifact-scoped")
	assert.Len(t, migration, 7, "seven migration operations are registered")
}

// TestManagerReannealSendsTheGuestToThePeer pins the manager side: the node, the
// attachment's network, the guest and the MAC cross as one POST to the placeholder
// route, and the node's typed refusal is the caller's error.
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
	require.NoError(t, mgr.Reanneal(context.Background(), "node-2", "net-user-1", "10.0.1.7", "AA:FC:11:22:33:44"))

	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, "/v1/tasks/"+migrationDirectiveRouteTask+"/"+MigrationReannealOperation, gotPath)
	assert.Equal(t, migrationPrepareMediaType, gotContentType)
	assert.Equal(t, "net-user-1", gotBody.NetworkID)
	assert.Equal(t, "10.0.1.7", gotBody.IP)
	assert.Equal(t, "aa:fc:11:22:33:44", gotBody.MAC, "the node accepts the MAC in one form")
}

// TestManagerReannealRefusesWhatIsNotAGuest pins the three shapes the manager can
// check before it dials a node, so a malformed value is not refused by every node in
// turn.
func TestManagerReannealRefusesWhatIsNotAGuest(t *testing.T) {
	pki := newTestPKI(t)
	var called bool
	_, baseURL := startTestBroker(t, pki, &fakeTaskSource{}, map[string]Handler{
		MigrationReannealOperation: func(_ context.Context, _ Task, _ http.ResponseWriter, _ *http.Request) {
			called = true
		},
	})
	mgr := newMigrationTestManager(t, pki, baseURL, 0)

	for _, tc := range []struct{ network, ip, mac string }{
		{network: "", ip: "10.0.1.7", mac: "aa:fc:11:22:33:44"},
		{network: "net/1", ip: "10.0.1.7", mac: "aa:fc:11:22:33:44"},
		{network: "net-1", ip: "ten", mac: "aa:fc:11:22:33:44"},
		{network: "net-1", ip: "10.0.1.7", mac: "aa:fc:11:22:33"},
	} {
		err := mgr.Reanneal(context.Background(), "node-2", tc.network, tc.ip, tc.mac)
		var typed *output.Error
		require.ErrorAs(t, err, &typed)
		assert.Equal(t, output.CodeInvalid, typed.Code)
	}
	assert.False(t, called, "a malformed guest never becomes a request")
}
