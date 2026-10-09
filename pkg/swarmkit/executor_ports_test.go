package swarmkit

import (
	"context"
	"errors"
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newPortController(label string, nm *MockNetworkManager) *Controller {
	return &Controller{
		task: &api.Task{
			ID: "task-1",
			ServiceAnnotations: api.Annotations{
				Labels: map[string]string{types.PublishLabel: label},
			},
		},
		config:     &Config{},
		networkMgr: nm,
		logger:     zerolog.Nop(),
	}
}

func TestPortStatus_MapsPublishedPorts(t *testing.T) {
	ctrl := &Controller{
		task:   &api.Task{ID: "task-1"},
		config: &Config{},
		publishedPorts: []types.PublishedPort{
			{Protocol: "tcp", TargetPort: 80, PublishedPort: 8080},
			{Protocol: "udp", TargetPort: 53, PublishedPort: 5353},
		},
	}

	st, err := ctrl.PortStatus(context.Background())
	require.NoError(t, err)
	require.Len(t, st.Ports, 2)
	assert.Equal(t, api.ProtocolTCP, st.Ports[0].Protocol)
	assert.Equal(t, uint32(8080), st.Ports[0].PublishedPort)
	assert.Equal(t, uint32(80), st.Ports[0].TargetPort)
	assert.Equal(t, api.PublishModeHost, st.Ports[0].PublishMode)
	assert.Equal(t, api.ProtocolUDP, st.Ports[1].Protocol)
}

func TestPortStatus_EmptyWithoutPorts(t *testing.T) {
	ctrl := &Controller{task: &api.Task{ID: "task-1"}, config: &Config{}}
	st, err := ctrl.PortStatus(context.Background())
	require.NoError(t, err)
	assert.Empty(t, st.Ports)
}

func TestPublishPorts_FromLabel(t *testing.T) {
	var gotTask, gotIP string
	var gotPorts []types.PublishedPort
	nm := &MockNetworkManager{
		GetTapIPFunc: func(string) (string, error) { return "192.168.127.7", nil },
		PublishPortsFunc: func(taskID, guestIP string, ports []types.PublishedPort) error {
			gotTask, gotIP, gotPorts = taskID, guestIP, ports
			return nil
		},
	}
	ctrl := newPortController("8080:80/tcp,5353:53/udp", nm)

	require.NoError(t, ctrl.publishPorts(&types.Task{ID: "task-1"}))
	assert.Equal(t, "task-1", gotTask)
	assert.Equal(t, "192.168.127.7", gotIP)
	assert.Equal(t, []types.PublishedPort{
		{Protocol: "tcp", TargetPort: 80, PublishedPort: 8080},
		{Protocol: "udp", TargetPort: 53, PublishedPort: 5353},
	}, gotPorts)
	assert.Equal(t, gotPorts, ctrl.publishedPorts)
}

func TestPublishPorts_FallsBackToTaskAddress(t *testing.T) {
	var gotIP string
	nm := &MockNetworkManager{
		GetTapIPFunc: func(string) (string, error) { return "", errors.New("no tap") },
		PublishPortsFunc: func(_, guestIP string, _ []types.PublishedPort) error {
			gotIP = guestIP
			return nil
		},
	}
	ctrl := newPortController("8080:80", nm)
	task := &types.Task{
		ID:       "task-1",
		Networks: []types.NetworkAttachment{{Addresses: []string{"192.168.127.42/24"}}},
	}

	require.NoError(t, ctrl.publishPorts(task))
	assert.Equal(t, "192.168.127.42", gotIP)
}

func TestPublishPorts_NoLabelIsNoOp(t *testing.T) {
	called := false
	nm := &MockNetworkManager{
		PublishPortsFunc: func(string, string, []types.PublishedPort) error { called = true; return nil },
	}
	ctrl := newPortController("", nm)
	require.NoError(t, ctrl.publishPorts(&types.Task{ID: "task-1"}))
	assert.False(t, called)
}

func TestPublishPorts_InvalidLabelIgnored(t *testing.T) {
	called := false
	nm := &MockNetworkManager{
		PublishPortsFunc: func(string, string, []types.PublishedPort) error { called = true; return nil },
	}
	ctrl := newPortController("not-a-port", nm)
	require.NoError(t, ctrl.publishPorts(&types.Task{ID: "task-1"}))
	assert.False(t, called)
}

func TestPublishPorts_ErrorPropagates(t *testing.T) {
	nm := &MockNetworkManager{
		GetTapIPFunc:     func(string) (string, error) { return "192.168.127.7", nil },
		PublishPortsFunc: func(string, string, []types.PublishedPort) error { return errors.New("collision") },
	}
	ctrl := newPortController("8080:80", nm)
	err := ctrl.publishPorts(&types.Task{ID: "task-1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to publish ports")
}

func TestUnpublishPorts_CallsManager(t *testing.T) {
	var gotTask string
	nm := &MockNetworkManager{
		UnpublishPortsFunc: func(taskID string, _ []types.PublishedPort) error {
			gotTask = taskID
			return nil
		},
	}
	ctrl := newPortController("8080:80", nm)
	ctrl.publishedPorts = []types.PublishedPort{{Protocol: "tcp", TargetPort: 80, PublishedPort: 8080}}

	ctrl.unpublishPorts("task-1")
	assert.Equal(t, "task-1", gotTask)
}

func TestUnpublishPorts_NoLabelNoPortsIsNoOp(t *testing.T) {
	called := false
	nm := &MockNetworkManager{
		UnpublishPortsFunc: func(string, []types.PublishedPort) error { called = true; return nil },
	}
	ctrl := newPortController("", nm)
	ctrl.unpublishPorts("task-1")
	assert.False(t, called)
}
