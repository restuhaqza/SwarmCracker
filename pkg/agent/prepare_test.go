package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// installPrepareSeams replaces the seams `prepare` is built from — reaching
// the metadata service, reading the identity from it, and applying it — so the
// contract around them can be tested without a guest to reconfigure or a
// Firecracker metadata service to talk to. It returns what the applier was asked
// to apply.
//
// The hook also releases the freeze a resumed image carries before it applies
// anything (thawAll), so the quiesce seams are installed too. They exist for the
// same reason: a unit test cannot freeze the filesystem it is running on. Without
// them these tests would take the host's own mount table to the real FIFREEZE
// ioctls, where they need CAP_SYS_ADMIN and fail with EPERM on an unprivileged
// runner — a statement about the machine, not about the hook.
func installPrepareSeams(t *testing.T, id Identity, fetchErr, applyErr error) *[]Identity {
	t.Helper()

	// The same fixture and seams the quiesce tests use: one writable ext4 root on
	// /dev/vda, which is the shape of a sandbox's guest.
	withQuiesceSeams(t, ext4Mounts, nil)

	previousReach := reachMetadataService
	previousFetch := fetchIdentity
	previousApply := applyIdentity
	t.Cleanup(func() {
		reachMetadataService = previousReach
		fetchIdentity = previousFetch
		applyIdentity = previousApply
	})

	reached := make([]string, 0, 1)
	reachMetadataService = func(iface string) error {
		reached = append(reached, iface)
		return nil
	}
	fetchIdentity = func(context.Context) (Identity, error) {
		return id, fetchErr
	}

	applied := &[]Identity{}
	applyIdentity = func(_ context.Context, got Identity) (PrepareReport, error) {
		*applied = append(*applied, got)
		if applyErr != nil {
			return PrepareReport{}, applyErr
		}
		return PrepareReport{
			Interface:     got.Interface,
			MAC:           got.MAC,
			Addresses:     []string{got.Address},
			DNS:           got.DNS,
			Hostname:      got.Hostname,
			MachineID:     got.MachineID,
			TokenRotated:  got.Token != "",
			Changed:       []string{PartMAC},
			ClockUnixNano: got.ClockUnixNano,
		}, nil
	}
	return applied
}

// helloIdentity is an identity the node could publish for a clone.
func helloIdentity() Identity {
	return Identity{
		Interface:     DefaultIdentityInterface,
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

// TestServeConnPrepareAppliesThePublishedIdentity is the hook's contract on the
// wire: the guest reads the identity its node published, applies it, and answers
// with what it carries afterwards — not with the request echoed back.
func TestServeConnPrepareAppliesThePublishedIdentity(t *testing.T) {
	applied := installPrepareSeams(t, helloIdentity(), nil, nil)

	conn, _ := newPipePair(t)
	before := BootID()
	require.NoError(t, WriteFrame(conn, Request{Op: CapPrepare, ID: "9"}))

	msg := readMessage(t, conn)
	require.Equal(t, "prepared", msg.Type)
	assert.Equal(t, "9", msg.ID, "the reply echoes the request id")
	require.NotNil(t, msg.Prepared)
	assert.Equal(t, DefaultIdentityInterface, msg.Prepared.Interface)
	assert.Equal(t, "06:00:ac:10:00:0b", msg.Prepared.MAC)
	assert.Equal(t, []string{"10.99.0.11/24"}, msg.Prepared.Addresses)
	assert.True(t, msg.Prepared.TokenRotated, "the guest adopted the token it was given")
	assert.NotEmpty(t, msg.Prepared.BootID)
	assert.NotEqual(t, before, msg.Prepared.BootID,
		"the clone reports a boot identity of its own, so a host holding a pre-restore session drops it")

	require.Len(t, *applied, 1)
	assert.Equal(t, helloIdentity(), (*applied)[0], "the guest applied exactly what the node published")
}

// TestServeConnPrepareDefaultsTheInterface proves an identity that names no
// interface is applied to the one the agent configures.
func TestServeConnPrepareDefaultsTheInterface(t *testing.T) {
	id := helloIdentity()
	id.Interface = ""
	applied := installPrepareSeams(t, id, nil, nil)

	conn, _ := newPipePair(t)
	require.NoError(t, WriteFrame(conn, Request{Op: CapPrepare}))
	readMessage(t, conn)

	require.Len(t, *applied, 1)
	assert.Equal(t, DefaultIdentityInterface, (*applied)[0].Interface)
}

// TestServeConnPrepareFailsTypedWhenTheGuestCannotApply is the failure the launch
// contract depends on: the hook runs, the guest cannot carry the identity out, and
// the host is told with a typed code instead of a success that would leave the
// clone wearing its source's identity.
func TestServeConnPrepareFailsTypedWhenTheGuestCannotApply(t *testing.T) {
	installPrepareSeams(t, helloIdentity(), nil, errors.New("no route to the metadata service"))

	conn, _ := newPipePair(t)
	require.NoError(t, WriteFrame(conn, Request{Op: CapPrepare, ID: "3"}))

	msg := readMessage(t, conn)
	assert.Equal(t, "error", msg.Type)
	assert.Equal(t, CodePrepareFailed, msg.ErrorCode)
	assert.Equal(t, "3", msg.ID)
	assert.Contains(t, msg.Error, "no route to the metadata service")

	// The connection stays usable: a caller tears the instance down itself, and the
	// next command on the same session must not read a desynchronised stream.
	require.NoError(t, WriteFrame(conn, Request{Op: CapHello, ID: "4", Version: Version}))
	assert.Equal(t, "hello", readMessage(t, conn).Type)
}

// TestServeConnPrepareFailsTypedWhenThereIsNoIdentity proves an instance whose node
// published nothing is refused rather than reported as prepared.
func TestServeConnPrepareFailsTypedWhenThereIsNoIdentity(t *testing.T) {
	installPrepareSeams(t, Identity{}, errors.New("metadata service: /identity answered 404 Not Found"), nil)

	conn, _ := newPipePair(t)
	require.NoError(t, WriteFrame(conn, Request{Op: CapPrepare}))

	msg := readMessage(t, conn)
	assert.Equal(t, "error", msg.Type)
	assert.Equal(t, CodePrepareFailed, msg.ErrorCode)
	assert.Contains(t, msg.Error, "404")
}

// TestServeConnPrepareRefusesAnUnusableIdentity proves the guest checks what it was
// given before it changes anything: a MAC an interface cannot carry is refused
// instead of half-applied.
func TestServeConnPrepareRefusesAnUnusableIdentity(t *testing.T) {
	id := helloIdentity()
	id.MAC = "01:00:5e:00:00:01" // a group address
	applied := installPrepareSeams(t, id, nil, nil)

	conn, _ := newPipePair(t)
	require.NoError(t, WriteFrame(conn, Request{Op: CapPrepare}))

	msg := readMessage(t, conn)
	assert.Equal(t, "error", msg.Type)
	assert.Equal(t, CodePrepareFailed, msg.ErrorCode)
	assert.Contains(t, msg.Error, "group address")
	assert.Empty(t, *applied, "nothing was applied")
}

// TestServeConnPrepareIsRefusedByAnAgentThatDoesNotAdvertiseIt is the case SBX-042
// exists for: an agent built before the hook answers hello, so a host can see what
// it advertises, and refuses the operation with a typed code and a message naming
// the operation and the capabilities it does have. The hook is never skipped.
func TestServeConnPrepareIsRefusedByAnAgentThatDoesNotAdvertiseIt(t *testing.T) {
	older := []string{CapHello, CapExec, CapPut, CapGet, CapMetrics}
	client, _ := newCappedPipePair(t, older)

	require.NoError(t, WriteFrame(client, Request{Op: CapHello, Version: Version}))
	hello := readMessage(t, client)
	assert.NotContains(t, hello.Caps, CapPrepare, "the older agent's own answer")

	require.NoError(t, WriteFrame(client, Request{Op: CapPrepare, ID: "5"}))
	msg := readMessage(t, client)
	assert.Equal(t, "error", msg.Type)
	assert.Equal(t, CodeUnsupportedOp, msg.ErrorCode)
	assert.Equal(t, "5", msg.ID)
	assert.Contains(t, msg.Error, CapPrepare, "the refusal names the operation")
	assert.Contains(t, msg.Error, CapHello, "and the capabilities the agent does advertise")
}

// TestFetchIdentityReadsTheMetadataServiceV2 pins the read: a session token minted
// with a TTL, the document fetched with it, and the JSON decoded into an identity.
// It is the guest's half of RFC D10 — the identity arrives over MMDS, not over the
// control channel.
func TestFetchIdentityReadsTheMetadataServiceV2(t *testing.T) {
	id := helloIdentity()
	document, err := json.Marshal(id)
	require.NoError(t, err)

	var tokens, reads int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/latest/api/token":
			tokens++
			assert.Equal(t, fmt.Sprint(mmdsTokenTTLSeconds), r.Header.Get("X-metadata-token-ttl-seconds"))
			_, _ = w.Write([]byte("session-token"))
		case r.Method == http.MethodGet && r.URL.Path == MMDSIdentityPath:
			reads++
			assert.Equal(t, "session-token", r.Header.Get("X-metadata-token"))
			_, _ = w.Write(document)
		default:
			t.Errorf("unexpected metadata request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	previousBase, previousClient := mmdsBaseURL, mmdsHTTPClient
	t.Cleanup(func() { mmdsBaseURL, mmdsHTTPClient = previousBase, previousClient })
	mmdsBaseURL = srv.URL
	mmdsHTTPClient = srv.Client()

	got, err := readIdentityMMDS(context.Background())
	require.NoError(t, err)
	assert.Equal(t, id, got)
	assert.Equal(t, 1, tokens)
	assert.Equal(t, 1, reads)
}

// TestFetchIdentityRetriesUntilTheMetadataServiceAnswers is the retry that makes a
// clone's first read survive what Firecracker documents on resume: "both network and
// vsock packet loss can be expected on guests that are resumed from snapshots in
// another Firecracker process". The service here loses the first two attempts the way
// a resumed guest loses its first frames — the connection is closed with no reply —
// and the read still returns the identity.
func TestFetchIdentityRetriesUntilTheMetadataServiceAnswers(t *testing.T) {
	document, err := json.Marshal(helloIdentity())
	require.NoError(t, err)

	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/latest/api/token" {
			if atomic.AddInt32(&attempts, 1) <= 2 {
				hijacker, ok := w.(http.Hijacker)
				require.True(t, ok)
				conn, _, err := hijacker.Hijack()
				require.NoError(t, err)
				_ = conn.Close()
				return
			}
			_, _ = w.Write([]byte("session-token"))
			return
		}
		_, _ = w.Write(document)
	}))
	defer srv.Close()

	previousBase, previousClient := mmdsBaseURL, mmdsHTTPClient
	t.Cleanup(func() { mmdsBaseURL, mmdsHTTPClient = previousBase, previousClient })
	mmdsBaseURL = srv.URL
	mmdsHTTPClient = srv.Client()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	got, err := fetchIdentity(ctx)
	require.NoError(t, err, "a lost first packet must not fail the read")
	assert.Equal(t, helloIdentity(), got)
	assert.Equal(t, int32(3), atomic.LoadInt32(&attempts), "the read is retried until it succeeds")
}

// TestFetchIdentityGivesUpWithItsBudget proves the retries cannot outlive the
// launch's window: a service that never answers ends the read with the budget's
// error, and the error says how many attempts were made.
func TestFetchIdentityGivesUpWithItsBudget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		require.True(t, ok)
		conn, _, err := hijacker.Hijack()
		require.NoError(t, err)
		_ = conn.Close()
	}))
	defer srv.Close()

	previousBase, previousClient := mmdsBaseURL, mmdsHTTPClient
	t.Cleanup(func() { mmdsBaseURL, mmdsHTTPClient = previousBase, previousClient })
	mmdsBaseURL = srv.URL
	mmdsHTTPClient = srv.Client()

	ctx, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
	defer cancel()

	_, err := fetchIdentity(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gave up after")
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

// TestFetchIdentityFailsWhenTheServiceHasNoDocument proves an empty metadata
// service is an error, never an empty identity that would look like "nothing to
// do" and leave the clone as its source.
func TestFetchIdentityFailsWhenTheServiceHasNoDocument(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			_, _ = w.Write([]byte("session-token"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("Resource not found: /identity."))
	}))
	defer srv.Close()

	previousBase, previousClient := mmdsBaseURL, mmdsHTTPClient
	t.Cleanup(func() { mmdsBaseURL, mmdsHTTPClient = previousBase, previousClient })
	mmdsBaseURL = srv.URL
	mmdsHTTPClient = srv.Client()

	_, err := readIdentityMMDS(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

// TestIdentityValidate pins what an identity may say. The guest applies what it is
// given, so anything an interface or a file cannot carry is refused before the
// first change.
func TestIdentityValidate(t *testing.T) {
	valid := helloIdentity()

	tests := []struct {
		name    string
		mutate  func(*Identity)
		wantErr string
	}{
		{name: "a full identity", mutate: func(*Identity) {}},
		{name: "mac only", mutate: func(i *Identity) { *i = Identity{MAC: "06:00:ac:10:00:0b"} }},
		{name: "address only", mutate: func(i *Identity) { *i = Identity{Address: "10.99.0.11/24"} }},
		{
			name:    "a mac that cannot be parsed",
			mutate:  func(i *Identity) { i.MAC = "06:00:ac" },
			wantErr: `mac "06:00:ac"`,
		},
		{
			name:    "a mac that is not 48 bits",
			mutate:  func(i *Identity) { i.MAC = "02:02:03:04:05:06:07:08" },
			wantErr: "not a 48-bit address",
		},
		{
			name:    "a group mac",
			mutate:  func(i *Identity) { i.MAC = "01:00:5e:00:00:01" },
			wantErr: "group address",
		},
		{
			name:    "an address without a prefix",
			mutate:  func(i *Identity) { i.Address = "10.99.0.11" },
			wantErr: "address",
		},
		{
			name:    "a gateway that is not an address",
			mutate:  func(i *Identity) { i.Gateway = "the-router" },
			wantErr: "not an address",
		},
		{
			name:    "a route without a destination",
			mutate:  func(i *Identity) { i.Routes = []Route{{Gateway: "10.99.0.1"}} },
			wantErr: "routes[0]",
		},
		{
			name:    "a nameserver that is not an address",
			mutate:  func(i *Identity) { i.DNS = []string{"resolver"} },
			wantErr: "dns[0]",
		},
		{
			name:    "a machine id of the wrong shape",
			mutate:  func(i *Identity) { i.MachineID = "not-a-machine-id" },
			wantErr: "32 hex characters",
		},
		{
			name:    "entropy that is not hex",
			mutate:  func(i *Identity) { i.Entropy = "zz" },
			wantErr: "entropy",
		},
		{
			name:    "an interface name with a slash",
			mutate:  func(i *Identity) { i.Interface = "net/eth0" },
			wantErr: "interface",
		},
		{
			name:    "a host name sethostname would refuse",
			mutate:  func(i *Identity) { i.Hostname = "not a host name" },
			wantErr: "is not a host name",
		},
		{
			name:    "nothing at all",
			mutate:  func(i *Identity) { *i = Identity{} },
			wantErr: "carries nothing to apply",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := valid
			tc.mutate(&id)
			err := id.Validate()
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestGenerationIDIgnoresWhatChangesEveryBoot proves the generation — what decides
// whether the guest mints a machine id and host keys of its own — is the machine's
// identity and nothing that a node rotates for the same clone. Without that, a
// resume would hand one machine a new machine-id and new SSH host keys.
func TestGenerationIDIgnoresWhatChangesEveryBoot(t *testing.T) {
	base := helloIdentity()

	sameMachine := base
	sameMachine.Token = strings.Repeat("11", 32)
	sameMachine.Entropy = strings.Repeat("22", 32)
	sameMachine.ClockUnixNano = base.ClockUnixNano + 60_000_000_000
	assert.Equal(t, base.GenerationID(), sameMachine.GenerationID(),
		"a rotated token, fresh entropy and a new clock are the same machine")

	for _, change := range []struct {
		name   string
		mutate func(*Identity)
	}{
		{name: "mac", mutate: func(i *Identity) { i.MAC = "06:00:ac:10:00:0c" }},
		{name: "address", mutate: func(i *Identity) { i.Address = "10.99.0.12/24" }},
		{name: "machine id", mutate: func(i *Identity) { i.MachineID = strings.Repeat("ba", 16) }},
		{name: "host name", mutate: func(i *Identity) { i.Hostname = "sbx-c" }},
		{name: "resolver", mutate: func(i *Identity) { i.DNS = []string{"10.99.0.54"} }},
	} {
		t.Run("a different "+change.name, func(t *testing.T) {
			other := base
			change.mutate(&other)
			assert.NotEqual(t, base.GenerationID(), other.GenerationID())
		})
	}
}

// TestClockNeedsSetting pins the one rule that keeps the clock sane without breaking
// it: move a clock that is behind, never one that is ahead, and treat a small
// difference as "already right" so a repeated prepare is a no-op.
func TestClockNeedsSetting(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)

	tests := []struct {
		name string
		want time.Time
		move bool
	}{
		{name: "the guest is behind by a minute", want: now.Add(time.Minute), move: true},
		{name: "the guest is behind by just over the tolerance", want: now.Add(clockTolerance + time.Millisecond), move: true},
		{name: "the guest is behind by less than the tolerance", want: now.Add(clockTolerance - time.Millisecond)},
		{name: "the guest is right on time", want: now},
		{name: "the guest is ahead", want: now.Add(-time.Minute)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.move, clockNeedsSetting(now, tc.want),
				"moving a clock backwards would break every timer the guest holds")
		})
	}
}

// TestDecodeEntropy bounds what an identity may carry into the kernel's pool.
func TestDecodeEntropy(t *testing.T) {
	got, err := decodeEntropy("00ff10")
	require.NoError(t, err)
	assert.Equal(t, []byte{0x00, 0xff, 0x10}, got)

	for _, bad := range []string{"", "0", "zz", strings.Repeat("ab", maxEntropyBytes+1)} {
		_, err := decodeEntropy(bad)
		assert.Error(t, err, "entropy %q must be refused", bad)
	}
}

// TestPrepareGuestReportsThePartsItChangedPlausible proves the report a caller
// reads is the guest's, not the identity echoed back: the seam above answers with a
// report of its own, and that is what reaches the wire.
func TestPrepareGuestReportsWhatTheApplierSaw(t *testing.T) {
	id := helloIdentity()
	installPrepareSeams(t, id, nil, nil)

	report, err := prepareGuest(context.Background())
	require.NoError(t, err)
	assert.Equal(t, id.MAC, report.MAC)
	assert.Equal(t, []string{id.Address}, report.Addresses)
	assert.True(t, report.TokenRotated)
	assert.Contains(t, report.Changed, PartMAC)
	assert.Contains(t, report.Changed, PartBootID)
	assert.NotEmpty(t, report.BootID)
}

// TestIdentityValidateRejectsWhatTheGuestWouldRefuse covers the parts of an identity the
// applier would find out too late: a route gateway the kernel cannot use, a token the
// agent would refuse, a machine id of the wrong shape and a host name sethostname would
// reject. Validation is what makes those a refused launch instead of a guest that
// half-applied its identity.
func TestIdentityValidateRejectsWhatTheGuestWouldRefuse(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Identity)
		wantErr string
	}{
		{
			name:    "a route gateway that is not an address",
			mutate:  func(i *Identity) { i.Routes = []Route{{Destination: "10.99.0.0/24", Gateway: "the-router"}} },
			wantErr: "routes[0] gateway",
		},
		{
			name:    "a token the agent would refuse",
			mutate:  func(i *Identity) { i.Token = "a token with a space" },
			wantErr: "token:",
		},
		{
			name:    "a machine id that is hex but not lowercase",
			mutate:  func(i *Identity) { i.MachineID = strings.ToUpper(i.MachineID) },
			wantErr: "32 hex characters",
		},
		{
			name:    "a host name longer than a kernel accepts",
			mutate:  func(i *Identity) { i.Hostname = strings.Repeat("a", 254) },
			wantErr: "is not a host name",
		},
		{
			name:    "a nameserver that is an address but not a resolver",
			mutate:  func(i *Identity) { i.DNS = []string{"10.97.0.1", "0.0.0.0.0"} },
			wantErr: "dns[1]",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := helloIdentity()
			tc.mutate(&id)

			err := id.Validate()

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// roundTripFunc is a metadata service that answers without a server: the failures below
// are the ones a clone's first read after a resume meets most often, and each one has to
// be an error rather than an empty identity.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// errBody is a response body that fails while it is read.
type errBody struct{ err error }

func (b errBody) Read([]byte) (int, error) { return 0, b.err }
func (b errBody) Close() error             { return nil }

// installMetadataService points the agent at a metadata service of the test's own, and
// puts the real one back afterwards.
func installMetadataService(t *testing.T, transport roundTripFunc) {
	t.Helper()

	previousBase, previousClient := mmdsBaseURL, mmdsHTTPClient
	t.Cleanup(func() { mmdsBaseURL, mmdsHTTPClient = previousBase, previousClient })
	mmdsBaseURL = "http://" + MMDSAddress
	mmdsHTTPClient = &http.Client{Transport: transport}
}

// TestReadIdentityMMDSReportsWhatTheServiceDid covers the reads that must not be mistaken
// for "the node published nothing": a service that refuses, one that answers a status, one
// whose body breaks halfway and one that answers something that is not an identity.
func TestReadIdentityMMDSReportsWhatTheServiceDid(t *testing.T) {
	t.Run("a service that cannot be reached is an error", func(t *testing.T) {
		installMetadataService(t, func(*http.Request) (*http.Response, error) {
			return nil, errors.New("no route to host")
		})

		_, err := readIdentityMMDS(context.Background())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "mint a session token")
	})

	t.Run("a session token the service refuses is an error", func(t *testing.T) {
		installMetadataService(t, func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Status:     "500 Internal Server Error",
				Body:       io.NopCloser(strings.NewReader("no tokens today")),
			}, nil
		})

		_, err := readIdentityMMDS(context.Background())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "minting a session token answered 500 Internal Server Error: no tokens today")
	})

	t.Run("an empty session token is an error", func(t *testing.T) {
		installMetadataService(t, func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader("\n"))}, nil
		})

		_, err := readIdentityMMDS(context.Background())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty session token")
	})

	t.Run("a token body that breaks while it is read is an error", func(t *testing.T) {
		installMetadataService(t, func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: errBody{errors.New("connection reset")}}, nil
		})

		_, err := readIdentityMMDS(context.Background())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "read the session token: connection reset")
	})

	t.Run("a service that answers a status for the identity is an error", func(t *testing.T) {
		installMetadataService(t, func(req *http.Request) (*http.Response, error) {
			if req.Method == http.MethodPut {
				return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader("session-token"))}, nil
			}
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Status:     "404 Not Found",
				Body:       io.NopCloser(strings.NewReader("no such path")),
			}, nil
		})

		_, err := readIdentityMMDS(context.Background())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "/identity answered 404 Not Found: no such path")
	})

	t.Run("a body that breaks while the identity is read is an error", func(t *testing.T) {
		installMetadataService(t, func(req *http.Request) (*http.Response, error) {
			if req.Method == http.MethodPut {
				return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader("session-token"))}, nil
			}
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: errBody{errors.New("connection reset")}}, nil
		})

		_, err := readIdentityMMDS(context.Background())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "read /identity: connection reset")
	})

	t.Run("a document that is not an identity is an error", func(t *testing.T) {
		installMetadataService(t, func(req *http.Request) (*http.Response, error) {
			body := "session-token"
			if req.Method == http.MethodGet {
				body = "not a document"
			}
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body))}, nil
		})

		_, err := readIdentityMMDS(context.Background())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "parse the identity published at /identity")
	})

	t.Run("a base URL that cannot be a request is an error", func(t *testing.T) {
		installMetadataService(t, func(*http.Request) (*http.Response, error) {
			t.Fatal("no request can be built from this base URL")
			return nil, nil
		})
		mmdsBaseURL = "http://169.254.169.254/\x7f"

		_, err := readIdentityMMDS(context.Background())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "build the session token request")
	})
}

// TestFetchIdentityGivesUpWhenTheRequestIsAlreadyOver covers the budget the host sets: a
// request that is over before the first attempt is not a read at all, and the guest has to
// report that rather than spin.
func TestFetchIdentityGivesUpWhenTheRequestIsAlreadyOver(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := fetchIdentity(ctx)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "the request was over before the first read: context canceled")
	assert.NotContains(t, err.Error(), "%!w", "a failure a caller reads must not carry fmt's placeholder for a missing cause")
}

// TestPrepareGuestReportsAReadThatFailed proves the hook does not turn "the node published
// nothing" into "the guest carries nothing": the failure is the caller's, and it names the
// metadata service.
func TestPrepareGuestReportsAReadThatFailed(t *testing.T) {
	installPrepareSeams(t, Identity{}, errors.New("the node published no identity"), nil)

	_, err := prepareGuest(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "the node published no identity")
}

// TestPrepareGuestReportsAnApplierThatFailed proves the whole hook fails closed: an
// applier that cannot carry the identity out is not reported as a guest that carries it.
func TestPrepareGuestReportsAnApplierThatFailed(t *testing.T) {
	installPrepareSeams(t, helloIdentity(), nil, errors.New("no route to the metadata service"))

	_, err := prepareGuest(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no route to the metadata service")
}

// TestPrepareGuestReportsAChannelItCannotReach covers the first step of the hook: a guest
// whose interface cannot be brought up and given a way to the metadata service has no
// identity to read, and the caller is told which step failed rather than that the identity
// was empty.
func TestPrepareGuestReportsAChannelItCannotReach(t *testing.T) {
	previous := reachMetadataService
	t.Cleanup(func() { reachMetadataService = previous })
	reachMetadataService = func(string) error { return errors.New("no such device") }

	_, err := prepareGuest(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "reach the metadata service on eth0: no such device")
}

// TestGenerationIDIsTheMachinesIdentity covers what the generation is computed from: a
// clone that is routed differently is a different machine and has to mint its own machine
// id and host keys, while the same clone keeps them across resumes.
func TestGenerationIDIsTheMachinesIdentity(t *testing.T) {
	base := helloIdentity()
	routed := base
	routed.Routes = []Route{{Destination: "10.100.0.0/24", Gateway: "10.97.0.254"}}

	assert.NotEqual(t, base.GenerationID(), routed.GenerationID(),
		"a clone whose routes differ is a different machine")
	assert.NotEqual(t, base.GenerationID(),
		func() string {
			copied := base
			copied.Routes = []Route{{Destination: "10.100.0.0/24"}}
			return copied.GenerationID()
		}(),
		"a route with no next hop is not the same route")

	same := base
	same.Routes = nil
	assert.Equal(t, base.GenerationID(), same.GenerationID(),
		"the same machine is the same machine")
}

// TestAFailedReleaseFailsThePrepareBeforeItWrites pins the hook's own failure: a resumed
// guest whose filesystems will not be released is refused here, naming the operation, rather
// than hanging on the first write the hook makes — and nothing of the identity is applied,
// because an instance whose writers are blocked is not one that was prepared.
func TestAFailedReleaseFailsThePrepareBeforeItWrites(t *testing.T) {
	applied := installPrepareSeams(t, helloIdentity(), nil, nil)

	// The guest cannot release the mount its image was frozen on.
	frozenErr := errors.New("quiesce /: ioctl: operation not permitted")
	original := thawFilesystem
	thawFilesystem = func(string) (bool, error) { return false, frozenErr }
	t.Cleanup(func() { thawFilesystem = original })

	conn, _ := newPipePair(t)
	require.NoError(t, WriteFrame(conn, Request{Op: CapPrepare, ID: "7"}))

	msg := readMessage(t, conn)
	require.Equal(t, "error", msg.Type)
	assert.Equal(t, CodePrepareFailed, msg.ErrorCode)
	assert.Contains(t, msg.Error, "release the freeze")
	assert.Contains(t, msg.Error, "/")
	assert.Empty(t, *applied, "a hook that could not release the freeze applies nothing")
}
