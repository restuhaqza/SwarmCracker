package ingress

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type errSource struct{}

func (errSource) Services(context.Context) ([]ServiceSpec, error) {
	return nil, errors.New("boom")
}
func (errSource) Tasks(context.Context) ([]TaskSpec, error) { return nil, nil }

func TestReconcile_PropagatesSourceError(t *testing.T) {
	err := NewController(errSource{}, &fakeLB{}, time.Second).Reconcile(context.Background())
	assert.Error(t, err)
}

func TestNewController_ClampsTinyInterval(t *testing.T) {
	c := NewController(&fakeSource{}, &fakeLB{}, 10*time.Millisecond)
	assert.Equal(t, 2*time.Second, c.interval)
}

func TestController_RunToleratesReconcileErrors(t *testing.T) {
	ctrl := NewController(errSource{}, &fakeLB{}, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		ctrl.Run(ctx)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

func TestController_RunReconcilesThenStops(t *testing.T) {
	src := &fakeSource{
		services: []ServiceSpec{{ID: "svc1", Ports: []PortSpec{{Protocol: "tcp", PublishedPort: 8080, TargetPort: 80}}}},
		tasks:    []TaskSpec{{ServiceID: "svc1", State: TaskStateRunning, IPs: []string{"192.168.127.5"}}},
	}
	lb := &fakeLB{}
	ctrl := NewController(src, lb, time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		ctrl.Run(ctx)
		close(done)
	}()

	require.Eventually(t, func() bool { return lb.calls >= 1 }, 2*time.Second, 10*time.Millisecond,
		"Run must reconcile at least once")
	cancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}
