package ingress

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeAddresses(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"empty", nil, nil},
		{"strips cidr, dedupes and sorts", []string{"192.168.127.6/24", "192.168.127.5/24", "192.168.127.6/24"}, []string{"192.168.127.5", "192.168.127.6"}},
		{"drops non-addresses", []string{"not-an-ip", "10.0.0.2"}, []string{"10.0.0.2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, normalizeAddresses(tt.in))
		})
	}
}

// TestSidecarAddressSource covers the node's view of its own tasks: the
// per-task sidecar is written when a VM starts and removed when it stops, so it
// is exactly the set of tasks this node runs.
func TestSidecarAddressSource(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, scruntime.WriteVMMetadata(dir, &scruntime.VMMetadata{
		ID:          "task-b",
		IPAddresses: []string{"192.168.127.6/24"},
	}))
	require.NoError(t, scruntime.WriteVMMetadata(dir, &scruntime.VMMetadata{
		ID:          "task-a",
		IPAddresses: []string{"192.168.127.5/24", "10.10.0.9/24"},
	}))
	// A task with no address yet is not reported: there is nothing to resolve.
	require.NoError(t, scruntime.WriteVMMetadata(dir, &scruntime.VMMetadata{ID: "task-c"}))

	got := SidecarAddressSource{SocketDir: dir}.LocalTaskAddresses()

	require.Len(t, got, 2, "a task with no address is skipped")
	assert.Equal(t, "task-a", got[0].TaskID, "reports are ordered")
	assert.Equal(t, []string{"10.10.0.9", "192.168.127.5"}, got[0].IPs)
	assert.Equal(t, []string{"192.168.127.6"}, got[1].IPs)
}

func TestSidecarAddressSource_EmptyDir(t *testing.T) {
	assert.Empty(t, SidecarAddressSource{}.LocalTaskAddresses())
	assert.Empty(t, SidecarAddressSource{SocketDir: t.TempDir()}.LocalTaskAddresses())
}

// TestReportStore covers the manager's side: reports are keyed by task, a
// refreshed report replaces the previous one, and a node that stops reporting
// stops contributing addresses.
func TestReportStore(t *testing.T) {
	now := time.Unix(0, 0)
	store := NewReportStore(20 * time.Second)
	store.now = func() time.Time { return now }

	store.Put(TaskReport{NodeID: "node-1", Tasks: []TaskAddress{
		{TaskID: "t1", IPs: []string{"192.168.127.5/24"}},
		{TaskID: "t2", IPs: []string{"192.168.127.6/24"}},
	}})
	store.Put(TaskReport{NodeID: "node-2", Tasks: []TaskAddress{
		{TaskID: "t3", IPs: []string{"192.168.127.7/24"}},
	}})
	assert.Equal(t, map[string][]string{
		"t1": {"192.168.127.5"},
		"t2": {"192.168.127.6"},
		"t3": {"192.168.127.7"},
	}, store.Addresses())

	// A task that stopped on node-1 disappears from the next report, and the
	// refresh keeps that node alive.
	now = now.Add(10 * time.Second)
	store.Put(TaskReport{NodeID: "node-1", Tasks: []TaskAddress{
		{TaskID: "t1", IPs: []string{"192.168.127.5/24"}},
	}})
	assert.Equal(t, map[string][]string{
		"t1": {"192.168.127.5"},
		"t3": {"192.168.127.7"},
	}, store.Addresses())

	// Once a node stops reporting, its addresses expire: a record must never
	// point at a node nobody can reach.
	now = now.Add(15 * time.Second) // node-2's report (t=0) is now stale, node-1's (t=10) is not
	assert.Equal(t, map[string][]string{"t1": {"192.168.127.5"}}, store.Addresses(),
		"only the report refreshed within the TTL survives")
	now = now.Add(21 * time.Second)
	assert.Empty(t, store.Addresses())
}

func TestReportStore_NilIsSafe(t *testing.T) {
	var store *ReportStore
	store.Put(TaskReport{NodeID: "n"})
	assert.Nil(t, store.Addresses())
}

// TestReporter_PublishesTaskAddresses covers the node side of the channel: the
// node posts its own addresses, and the identity is left to the certificate.
func TestReporter_PublishesTaskAddresses(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, scruntime.WriteVMMetadata(dir, &scruntime.VMMetadata{
		ID:          "task-a",
		IPAddresses: []string{"192.168.127.5/24"},
	}))

	bodies := make(chan []byte, 4)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		buf, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		require.NoError(t, err)
		bodies <- buf
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	rep := NewReporter(ts.URL, nil, SidecarAddressSource{SocketDir: dir}, time.Second)
	rep.reportOnce(context.Background())

	select {
	case body := <-bodies:
		var got TaskReport
		require.NoError(t, json.Unmarshal(body, &got))
		assert.Empty(t, got.NodeID, "the node does not claim an identity; the certificate carries it")
		require.Len(t, got.Tasks, 1)
		assert.Equal(t, "task-a", got.Tasks[0].TaskID)
		assert.Equal(t, []string{"192.168.127.5"}, got.Tasks[0].IPs)
	case <-time.After(3 * time.Second):
		t.Fatal("no report was published")
	}
}

// TestReporter_UnreachableManagerIsSurvivable asserts a failed report does not
// panic or block: the manager expires stale reports, so missing one is safe.
func TestReporter_UnreachableManagerIsSurvivable(t *testing.T) {
	rep := NewReporter("https://127.0.0.1:1/v1/ingress/report", nil, SidecarAddressSource{}, time.Second)
	assert.NotPanics(t, func() { rep.reportOnce(context.Background()) })
}

// TestReporter_RunStopsOnCancel asserts the publishing loop is tied to the
// daemon's lifetime.
func TestReporter_RunStopsOnCancel(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		NewReporter(ts.URL, nil, SidecarAddressSource{}, time.Second).Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}
