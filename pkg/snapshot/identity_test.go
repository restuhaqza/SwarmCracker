package snapshot

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
)

// cloneIdentity is an identity a node could publish for a clone: everything that
// makes it a different machine from the checkpoint's source.
func cloneIdentity() agent.Identity {
	return agent.Identity{
		Interface:     agent.DefaultIdentityInterface,
		MAC:           "06:00:ac:10:00:0b",
		Address:       "10.99.0.11/24",
		Gateway:       "10.99.0.1",
		DNS:           []string{"10.99.0.53"},
		Hostname:      "sbx-b",
		MachineID:     strings.Repeat("ab", 16),
		Token:         strings.Repeat("cd", 32),
		Entropy:       strings.Repeat("ef", 32),
		ClockUnixNano: 1_700_000_000_000_000_000,
	}
}

// TestIdentityProblem pins when a launch refuses a request that carries an
// identity. Every refusal here happens before anything is created, and the two
// required parts — MAC and address — are the ones the host can prove from the
// guest's report: a launch that could not prove them would be a launch that cannot
// tell a clone from its source.
func TestIdentityProblem(t *testing.T) {
	net := []NetworkInterface{{IfaceID: "eth0", HostTap: "tap-b", GuestMAC: "06:00:ac:10:00:01"}}

	tests := []struct {
		name    string
		network []NetworkInterface
		id      *agent.Identity
		wantErr string
	}{
		{name: "a launch with no identity has no rules", network: nil, id: nil},
		{
			name: "an identity with the interface it is reached over",
			network: net, id: ptrTo(cloneIdentity()),
		},
		{
			name: "an identity on another interface",
			network: net,
			id:      &agent.Identity{MAC: "06:00:ac:10:00:0b", Address: "10.99.0.11/24", Interface: "eth1"},
			wantErr: `interface "eth1"`,
		},
		{
			name: "an identity with no interface to be reached over",
			network: nil,
			id:      ptrTo(cloneIdentity()),
			wantErr: "read through the instance's own interface",
		},
		{
			name: "an identity with no mac",
			network: net,
			id:      &agent.Identity{Address: "10.99.0.11/24"},
			wantErr: "must name the MAC",
		},
		{
			name: "an identity with no address",
			network: net,
			id:      &agent.Identity{MAC: "06:00:ac:10:00:0b"},
			wantErr: "must name the address",
		},
		{
			name: "an identity the guest could not apply",
			network: net,
			id:      &agent.Identity{MAC: "nonsense", Address: "10.99.0.11/24"},
			wantErr: "identity:",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := identityProblem(tc.network, tc.id)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestNetworkProblem pins the interface list a launcher hands the VMM.
func TestNetworkProblem(t *testing.T) {
	require.NoError(t, networkProblem(nil))
	require.NoError(t, networkProblem([]NetworkInterface{{IfaceID: "eth0", HostTap: "tap0"}}))

	err := networkProblem([]NetworkInterface{{HostTap: "tap0"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "iface id")

	err = networkProblem([]NetworkInterface{{IfaceID: "eth0"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "host tap")

	err = networkProblem([]NetworkInterface{{IfaceID: "eth0", HostTap: "a"}, {IfaceID: "eth0", HostTap: "b"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "named twice")
}

// TestReportSatisfies is the rule that keeps the hook from being a formality: the
// guest's own report has to show every part the node published, or the launch
// failed. A part the identity leaves empty is the guest's to mint and is not
// checked.
func TestReportSatisfies(t *testing.T) {
	id := cloneIdentity()
	matching := agent.PrepareReport{
		Interface:     id.Interface,
		MAC:           strings.ToUpper(id.MAC), // the report is compared case-insensitively
		Addresses:     []string{"10.99.0.11/24", "169.254.1.1/16"},
		Routes:        []string{"default via 10.99.0.1 dev eth0", "10.99.0.0/24 dev eth0 scope link"},
		DNS:           []string{"10.99.0.53"},
		Hostname:      id.Hostname,
		MachineID:     id.MachineID,
		TokenRotated:  true,
		BootID:        "boot-clone",
		ClockUnixNano: id.ClockUnixNano,
	}
	require.NoError(t, reportSatisfies(id, matching))

	tests := []struct {
		name    string
		mutate  func(*agent.PrepareReport)
		wantErr string
	}{
		{
			name:    "the guest kept its source's mac",
			mutate:  func(r *agent.PrepareReport) { r.MAC = "06:00:ac:10:00:01" },
			wantErr: "carries MAC",
		},
		{
			name:    "the guest kept its source's address",
			mutate:  func(r *agent.PrepareReport) { r.Addresses = []string{"172.16.0.2/30"} },
			wantErr: "carries addresses",
		},
		{
			name:    "the guest kept its source's machine id",
			mutate:  func(r *agent.PrepareReport) { r.MachineID = strings.Repeat("cd", 16) },
			wantErr: "carries machine id",
		},
		{
			name:    "the guest kept its source's host name",
			mutate:  func(r *agent.PrepareReport) { r.Hostname = "ubuntu-fc-uvm" },
			wantErr: "host name",
		},
		{
			name:    "the guest did not adopt the token",
			mutate:  func(r *agent.PrepareReport) { r.TokenRotated = false },
			wantErr: "did not adopt the token",
		},
		{
			name:    "the guest resolves through another server",
			mutate:  func(r *agent.PrepareReport) { r.DNS = []string{"172.31.0.2"} },
			wantErr: "resolves through",
		},
		{
			name:    "the guest kept its source's gateway",
			mutate:  func(r *agent.PrepareReport) { r.Routes = []string{"default via 172.16.0.1 dev eth0"} },
			wantErr: "do not use the gateway",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			report := matching
			report.Addresses = append([]string(nil), matching.Addresses...)
			tc.mutate(&report)

			err := reportSatisfies(id, report)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}

	// An identity that names nothing but the two required parts is satisfied by a
	// report that carries them, whatever else the guest minted for itself.
	require.NoError(t, reportSatisfies(
		agent.Identity{MAC: id.MAC, Address: id.Address},
		agent.PrepareReport{MAC: id.MAC, Addresses: []string{id.Address}, MachineID: "whatever"}))

	// A prefix that differs is a different address, not a match.
	err := reportSatisfies(agent.Identity{Address: "10.99.0.11/24"},
		agent.PrepareReport{Addresses: []string{"10.99.0.11/30"}})
	require.Error(t, err)
}

// TestRunPrepareIsRefusedByAnAgentWithoutTheHook is SBX-042's own test at the host
// end: the agent answered the handshake and does not advertise the operation, so
// the launch fails with ErrPrepareUnsupported and a message that names the protocol
// and what the agent did advertise. The hook is never skipped.
func TestRunPrepareIsRefusedByAnAgentWithoutTheHook(t *testing.T) {
	installPrepareDial(t, fakePrepareClient{
		hello: agent.Hello{
			Version: agent.Version,
			Caps:    []string{agent.CapHello, agent.CapExec, agent.CapPut, agent.CapGet, agent.CapMetrics},
			BootID:  "boot-old",
		},
	}, nil)

	_, err := runPrepare(context.Background(), "sbx-1", "/run/firecracker/agent.vsock", "tok", cloneIdentity(), time.Second)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPrepareUnsupported)
	assert.NotErrorIs(t, err, ErrPrepareFailed)
	assert.Contains(t, err.Error(), "sbx-1")
	assert.Contains(t, err.Error(), "protocol "+agent.Version)
	assert.Contains(t, err.Error(), agent.CapHello, "the message names what the agent does advertise")
	assert.Contains(t, err.Error(), agent.CapPrepare, "and the operation it does not")
	assert.Contains(t, err.Error(), "predates the post-restore hook")
}

// TestRunPrepareFailsWhenTheHookFails proves a hook that ran and failed is a failed
// launch: the caller is told, the error is typed, and the guest's own message comes
// with it.
func TestRunPrepareFailsWhenTheHookFails(t *testing.T) {
	installPrepareDial(t, fakePrepareClient{
		hello: agent.Hello{Version: agent.Version, Caps: agent.Capabilities(), BootID: "boot-b"},
	}, &agent.Error{Code: agent.CodePrepareFailed, Message: "metadata service: /identity answered 404 Not Found"})

	_, err := runPrepare(context.Background(), "sbx-1", "/run/firecracker/agent.vsock", "tok", cloneIdentity(), time.Second)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPrepareFailed)
	assert.Contains(t, err.Error(), "404")
}

// TestRunPrepareFailsWhenTheGuestDidNotAdoptTheIdentity proves the host checks the
// guest's own report rather than trusting that the request was carried out: a guest
// that answered successfully while still wearing its source's identity fails the
// launch.
func TestRunPrepareFailsWhenTheGuestDidNotAdoptTheIdentity(t *testing.T) {
	installPrepareDial(t, fakePrepareClient{
		hello: agent.Hello{Version: agent.Version, Caps: agent.Capabilities(), BootID: "boot-b"},
		report: agent.PrepareReport{
			MAC:       "06:00:ac:10:00:01",
			Addresses: []string{"172.16.0.2/30"},
			Changed:   []string{agent.PartBootID},
		},
	}, nil)

	_, err := runPrepare(context.Background(), "sbx-1", "/run/firecracker/agent.vsock", "tok", cloneIdentity(), time.Second)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPrepareFailed)
	assert.Contains(t, err.Error(), "did not adopt the identity it was given")
}

// TestRunPrepareReturnsWhatTheGuestAdopted is the happy path: the report crosses
// back to the caller, so a controller reads the instance's identity from the launch.
func TestRunPrepareReturnsWhatTheGuestAdopted(t *testing.T) {
	report := agent.PrepareReport{
		Interface:     agent.DefaultIdentityInterface,
		MAC:           cloneIdentity().MAC,
		Addresses:     []string{cloneIdentity().Address},
		Routes:        []string{"default via 10.99.0.1 dev eth0"},
		DNS:           cloneIdentity().DNS,
		Hostname:      cloneIdentity().Hostname,
		MachineID:     cloneIdentity().MachineID,
		TokenRotated:  true,
		BootID:        "boot-clone",
		ClockUnixNano: 1_700_000_000_000_000_000,
	}
	client := fakePrepareClient{
		hello:  agent.Hello{Version: agent.Version, Caps: agent.Capabilities(), BootID: "boot-source"},
		report: report,
	}
	dialed := installPrepareDial(t, client, nil)

	got, err := runPrepare(context.Background(), "sbx-1", "/run/firecracker/agent.vsock", "tok", cloneIdentity(), time.Second)
	require.NoError(t, err)
	assert.Equal(t, report, got)
	assert.Equal(t, 1, *dialed, "the hook dials the guest once")
}

// installPrepareDial replaces the hook's dial with a client that answers what the
// test configured, and returns how many times it was dialled.
func installPrepareDial(t *testing.T, client fakePrepareClient, err error) *int {
	t.Helper()

	previous := prepareDial
	t.Cleanup(func() { prepareDial = previous })

	dialed := new(int)
	dial := func(context.Context, string, uint32, string) (prepareClient, error) {
		*dialed++
		if err != nil {
			return nil, err
		}
		return client, nil
	}
	prepareDial = dial
	return dialed
}

// TestPublishIdentityUsesThePathTheGuestReads pins the two halves of the identity
// channel against each other: the document is published under the key the guest
// fetches (agent.MMDSIdentityPath), as a JSON *string*, because Firecracker answers
// an object node with a list of its keys rather than with the object.
func TestPublishIdentityUsesThePathTheGuestReads(t *testing.T) {
	api := newFakeAPI(t)
	id := cloneIdentity()

	require.NoError(t, publishIdentity(context.Background(), api.socket, &id))

	require.Len(t, api.calls, 1)
	assert.Equal(t, apiCall{method: http.MethodPut, path: "/mmds", body: map[string]interface{}{
		strings.TrimPrefix(agent.MMDSIdentityPath, "/"): mustJSON(t, id),
	}}, api.calls[0])

	// A launch with no identity publishes nothing at all.
	api.calls = nil
	require.NoError(t, publishIdentity(context.Background(), api.socket, nil))
	assert.Empty(t, api.calls)
}

// TestConfigureMetadataServiceNamesTheInterfaces pins what a cold boot asks for:
// the version the guest speaks and the interfaces the service answers on. It is one
// call, before the guest exists, because Firecracker freezes the configuration when
// the microVM starts.
func TestConfigureMetadataServiceNamesTheInterfaces(t *testing.T) {
	api := newFakeAPI(t)

	require.NoError(t, configureMetadataService(context.Background(), api.socket,
		[]NetworkInterface{{IfaceID: "eth0", HostTap: "t0"}, {IfaceID: "eth1", HostTap: "t1"}}))

	require.Len(t, api.calls, 1)
	assert.Equal(t, apiCall{method: http.MethodPut, path: "/mmds/config", body: map[string]interface{}{
		"version":            mmdsVersion,
		"network_interfaces": []interface{}{"eth0", "eth1"},
	}}, api.calls[0])

	// A launch with no interfaces asks for nothing.
	api.calls = nil
	require.NoError(t, configureMetadataService(context.Background(), api.socket, nil))
	assert.Empty(t, api.calls)
}

// TestBootJailedGuestConfiguresTheNetworkBeforeTheGuestStarts pins the cold path's
// order: the interface the identity is read through is attached, the metadata
// service is configured and the clone's identity published, and only then does the
// guest start — a guest that came up first would read a service nothing had written
// to yet.
func TestBootJailedGuestConfiguresTheNetworkBeforeTheGuestStarts(t *testing.T) {
	api := newFakeAPI(t)
	id := cloneIdentity()

	require.NoError(t, bootJailedGuest(context.Background(), api.socket, LaunchRequest{
		VCPUs:     2,
		MemoryMB:  256,
		VsockPath: jailedTestVsockPath,
		Net:       []NetworkInterface{{IfaceID: "eth0", HostTap: "sbx-tap", GuestMAC: "06:00:ac:10:00:01"}},
		Identity:  &id,
	}))

	paths := make([]string, 0, len(api.calls))
	for _, call := range api.calls {
		paths = append(paths, call.path)
	}
	assert.Equal(t, []string{
		"/network-interfaces/eth0",
		"/boot-source",
		"/drives/rootfs",
		"/machine-config",
		"/vsock",
		"/mmds/config",
		"/mmds",
		"/actions",
	}, paths, "the metadata service is written before the guest is started")

	assert.Equal(t, map[string]interface{}{
		"iface_id":      "eth0",
		"host_dev_name": "sbx-tap",
		"guest_mac":     "06:00:ac:10:00:01",
	}, api.calls[0].body)
}

// TestJailedLauncherPublishesTheIdentityWhileTheGuestIsStopped is the restore path:
// the checkpoint is loaded with the guest still stopped, the clone's identity is
// published, and only then is the guest resumed and the hook run. Every clone of one
// checkpoint publishes its own identity, because the metadata document does not
// survive the checkpoint.
func TestJailedLauncherPublishesTheIdentityWhileTheGuestIsStopped(t *testing.T) {
	requireJailPlatform(t)

	launcher, fake := jailedTestLauncher(t)
	id := cloneIdentity()
	fake.hello = agent.Hello{Version: agent.Version, Caps: agent.Capabilities(), BootID: "boot-source"}
	fake.hookHello = fake.hello
	fake.hookReport = agent.PrepareReport{
		Interface: id.Interface, MAC: id.MAC, Addresses: []string{id.Address},
		Routes: []string{"default via 10.99.0.1 dev eth0"},
		DNS: id.DNS, Hostname: id.Hostname, MachineID: id.MachineID,
		TokenRotated: true, BootID: "boot-clone",
	}
	fake.install(t)

	req := needJailedRestoreRequest(t, launcher)
	req.Net = []NetworkInterface{{IfaceID: "eth0", HostTap: "sbx-tap", GuestMAC: "06:00:ac:10:00:01"}}
	req.Identity = &id
	req.AgentToken = strings.Repeat("9a", 32)
	launcher.JailerPath = fake.jailerScript(t, launcher, req.InstanceID)

	launched, err := launcher.Start(context.Background(), req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = launched.Stop(context.Background()) })

	require.Len(t, fake.published, 1, "the launch published the clone's identity")
	assert.Equal(t, &id, fake.published[0].identity)
	assert.Equal(t, launched.APISocket, fake.published[0].socket, "published to this instance's own VMM")
	assert.Empty(t, fake.mmdsConfigs,
		"a restore inherits the metadata configuration from the checkpoint; only the document is rewritten")

	require.Len(t, fake.loads, 1)
	assert.Equal(t, req.Net, fake.loads[0].network, "the clone opens its own host devices, not the recorded ones")
	require.Len(t, fake.resumes, 1, "the guest runs after the identity is published")
	assert.Len(t, fake.hookDials, 1, "the hook dialled the guest")
	require.NotNil(t, launched.Prepared, "the launch reports what the guest adopted")
	assert.Equal(t, "boot-clone", launched.Prepared.BootID)
	assert.Equal(t, id.MAC, launched.Prepared.MAC)
	assert.Equal(t, req.AgentToken, fake.hookDials[0].token,
		"the hook presents the same per-boot token the readiness check did")
}

// TestJailedLauncherFailsTheLaunchWhenTheGuestCannotAdopt is the failure a node must
// never paper over: the guest cannot take the identity, so the launch fails with a
// typed error and leaves nothing behind — no process, no jail, no cgroup and no uid.
func TestJailedLauncherFailsTheLaunchWhenTheGuestCannotAdopt(t *testing.T) {
	requireJailPlatform(t)

	launcher, fake := jailedTestLauncher(t)
	id := cloneIdentity()
	fake.hello = agent.Hello{Version: agent.Version, Caps: agent.Capabilities(), BootID: "boot-source"}
	fake.hookHello = fake.hello
	fake.hookReport = agent.PrepareReport{MAC: "06:00:ac:10:00:01", Addresses: []string{"172.16.0.2/30"}}
	fake.install(t)

	req := needJailedRestoreRequest(t, launcher)
	req.Net = []NetworkInterface{{IfaceID: "eth0", HostTap: "sbx-tap"}}
	req.Identity = &id
	launcher.JailerPath = fake.jailerScript(t, launcher, req.InstanceID)

	launched, err := launcher.Start(context.Background(), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPrepareFailed)
	assert.Contains(t, err.Error(), "carries MAC")
	assert.Nil(t, launched.Stop, "a failed launch hands back no handle to a process it already killed")

	assert.False(t, processAlive(pidFromFake(t, fake, req.InstanceID)), "the failed launch stopped its VMM")
	assert.NoDirExists(t, launcher.JailRoot(req.InstanceID), "a failed launch leaves no jail")
	assert.NoDirExists(t, filepath.Join(launcher.cgroupRoot, "sbxtest", req.InstanceID))
	assert.False(t, launcher.UIDs.Leased(3000), "a failed launch gives its uid back")
}

// TestJailedLauncherRefusesAnIdentityItCannotApply proves the refusal happens before
// anything is created: a request whose identity has no interface to be read through
// creates no process, no jail and no uid lease.
func TestJailedLauncherRefusesAnIdentityItCannotApply(t *testing.T) {
	launcher, _ := jailedTestLauncher(t)
	id := cloneIdentity()

	req := needJailedRestoreRequest(t, launcher)
	req.Identity = &id // and no Net, so the metadata service is unreachable

	_, err := launcher.Start(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read through the instance's own interface")
	assert.NoDirExists(t, launcher.JailRoot(req.InstanceID))
	assert.False(t, launcher.UIDs.Leased(3000))
}

// TestDirectLauncherPublishesTheIdentityWhileTheGuestIsStopped proves the same
// contract holds on the dev path: it is a property of the request, not of how the
// VMM was started.
func TestDirectLauncherPublishesTheIdentityWhileTheGuestIsStopped(t *testing.T) {
	id := cloneIdentity()
	hello := agent.Hello{Version: agent.Version, Caps: agent.Capabilities(), BootID: "boot-source"}
	fake := &fakeDirectVMM{createSocket: true, hello: hello}
	fake.install(t)
	installPrepareDial(t, fakePrepareClient{hello: hello, report: agent.PrepareReport{
		Interface: id.Interface, MAC: id.MAC, Addresses: []string{id.Address},
		Routes: []string{"default via 10.99.0.1 dev eth0"},
		DNS: id.DNS, Hostname: id.Hostname, MachineID: id.MachineID, TokenRotated: true,
	}}, nil)

	req := needRestoreRequest()
	req.Net = []NetworkInterface{{IfaceID: "eth0", HostTap: "sbx-tap"}}
	req.Identity = &id
	launcher := DirectLauncher{SocketDir: t.TempDir(), LogDir: t.TempDir()}

	launched, err := launcher.Start(context.Background(), req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = launched.Stop(context.Background()) })

	require.Len(t, fake.pausedLoads, 1, "a launch with an identity loads the checkpoint without running the guest")
	assert.Empty(t, fake.loads, "and not in one call that would run the guest first")
	require.Len(t, fake.publishes, 1)
	assert.Equal(t, &id, fake.publishes[0])
	assert.Equal(t, req.Net, fake.pausedLoads[0].network)
	require.Len(t, fake.resumesGuest, 1, "the guest runs after the identity is published")
	require.NotNil(t, launched.Prepared)
	assert.Equal(t, id.MAC, launched.Prepared.MAC)
}

// TestDirectLauncherLoadsInOneCallWithoutAnIdentity pins that the pre-sandbox path
// is untouched: a launch with nothing to apply takes the single call that resumes
// the guest.
func TestDirectLauncherLoadsInOneCallWithoutAnIdentity(t *testing.T) {
	hello := agent.Hello{Version: agent.Version, Caps: agent.Capabilities(), BootID: "boot-source"}
	fake := &fakeDirectVMM{createSocket: true, hello: hello}
	fake.install(t)

	req := needRestoreRequest()
	launcher := DirectLauncher{SocketDir: t.TempDir(), LogDir: t.TempDir()}

	launched, err := launcher.Start(context.Background(), req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = launched.Stop(context.Background()) })

	require.Len(t, fake.loads, 1)
	assert.Empty(t, fake.pausedLoads)
	assert.Empty(t, fake.publishes)
	assert.Nil(t, launched.Prepared)
}

// mustJSON is the JSON *string* a document is published as.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	body, err := json.Marshal(v)
	require.NoError(t, err)
	return string(body)
}

// ptrTo is a pointer to a copy of v, for the table above.
func ptrTo(v agent.Identity) *agent.Identity { return &v }
