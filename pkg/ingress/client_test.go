package ingress

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTableClient_Fetch(t *testing.T) {
	want := TableFromRoutes(sampleRoutes())
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(want)
	}))
	defer ts.Close()

	got, err := NewTableClient(ts.URL, nil, time.Second).Fetch(context.Background())
	require.NoError(t, err)
	assert.Equal(t, want.Revision, got.Revision)
	assert.Len(t, got.Routes, 2)
}

func TestTableClient_Fetch_StatusError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer ts.Close()

	_, err := NewTableClient(ts.URL, nil, time.Second).Fetch(context.Background())
	assert.Error(t, err)
}

func TestTableClient_Fetch_BadJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer ts.Close()

	_, err := NewTableClient(ts.URL, nil, time.Second).Fetch(context.Background())
	assert.Error(t, err)
}

func TestTableClient_RunProgramsLB(t *testing.T) {
	want := TableFromRoutes(sampleRoutes())
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(want)
	}))
	defer ts.Close()

	lb := &fakeLB{}
	ctrl := NewTableClient(ts.URL, nil, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		ctrl.Run(ctx, lb)
		close(done)
	}()

	require.Eventually(t, func() bool { return lb.count() >= 1 }, 2*time.Second, 10*time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

func TestTableClient_RunKeepsLastOnError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer ts.Close()

	lb := &fakeLB{}
	ctrl := NewTableClient(ts.URL, nil, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		ctrl.Run(ctx, lb)
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done
	assert.Zero(t, lb.count(), "no rules are programmed when the table cannot be fetched")
}
