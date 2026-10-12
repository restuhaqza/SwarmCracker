package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// TestParseMemory tests the memory parsing function
func TestParseMemory(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int64
		hasError bool
	}{
		{
			name:     "empty string",
			input:    "",
			expected: 0,
			hasError: false,
		},
		{
			name:     "bytes only",
			input:    "1024",
			expected: 1024,
			hasError: false,
		},
		{
			name:     "kilobytes lowercase",
			input:    "1k",
			expected: 1024,
			hasError: false,
		},
		{
			name:     "kilobytes uppercase",
			input:    "1K",
			expected: 1024,
			hasError: false,
		},
		{
			name:     "megabytes lowercase",
			input:    "512m",
			expected: 512 * 1024 * 1024,
			hasError: false,
		},
		{
			name:     "megabytes uppercase",
			input:    "512M",
			expected: 512 * 1024 * 1024,
			hasError: false,
		},
		{
			name:     "gigabytes lowercase",
			input:    "1g",
			expected: 1024 * 1024 * 1024,
			hasError: false,
		},
		{
			name:     "gigabytes uppercase",
			input:    "1G",
			expected: 1024 * 1024 * 1024,
			hasError: false,
		},
		{
			name:     "gigabytes suffix",
			input:    "2GB",
			expected: 2 * 1024 * 1024 * 1024,
			hasError: false,
		},
		{
			name:     "megabytes suffix",
			input:    "256MB",
			expected: 256 * 1024 * 1024,
			hasError: false,
		},
		{
			name:     "invalid format",
			input:    "abc",
			expected: 0,
			hasError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseMemory(tt.input)
			if tt.hasError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

// TestParseLabels tests the label parsing function
func TestParseLabels(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected map[string]string
	}{
		{
			name:     "empty input",
			input:    []string{},
			expected: map[string]string{},
		},
		{
			name:     "single label",
			input:    []string{"key=value"},
			expected: map[string]string{"key": "value"},
		},
		{
			name:     "multiple labels",
			input:    []string{"key1=value1", "key2=value2"},
			expected: map[string]string{"key1": "value1", "key2": "value2"},
		},
		{
			name:     "label with equals in value",
			input:    []string{"key=value=with=equals"},
			expected: map[string]string{"key": "value=with=equals"},
		},
		{
			name:     "label without value",
			input:    []string{"key"},
			expected: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseLabels(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestNewServiceCommand tests command structure
func TestNewServiceCommand(t *testing.T) {
	cmd := newServiceCommand()

	assert.Equal(t, "service", cmd.Use)
	assert.Equal(t, "Manage SwarmCracker services", cmd.Short)
	assert.True(t, strings.Contains(cmd.Long, "Services are replicated"))

	// Check subcommands exist
	subcommands := cmd.Commands()
	assert.Equal(t, 7, len(subcommands))

	// Verify expected subcommand names exist (order may vary)
	foundSubcmds := make(map[string]bool)
	for _, subcmd := range subcommands {
		foundSubcmds[subcmd.Name()] = true
	}
	expectedSubcmds := []string{"ls", "inspect", "ps", "create", "update", "scale", "rm"}
	for _, expected := range expectedSubcmds {
		assert.True(t, foundSubcmds[expected], "Expected subcommand '%s' not found", expected)
	}
}

// TestServiceCreateCommandFlags tests the create command flags
func TestServiceCreateCommandFlags(t *testing.T) {
	cmd := newServiceCreateCommand()

	// Check required flags exist
	nameFlag := cmd.Flags().Lookup("name")
	assert.NotNil(t, nameFlag)
	assert.Equal(t, "", nameFlag.DefValue)

	imageFlag := cmd.Flags().Lookup("image")
	assert.NotNil(t, imageFlag)
	assert.Equal(t, "", imageFlag.DefValue)

	replicasFlag := cmd.Flags().Lookup("replicas")
	assert.NotNil(t, replicasFlag)
	assert.Equal(t, "1", replicasFlag.DefValue)

	cpuFlag := cmd.Flags().Lookup("cpu")
	assert.NotNil(t, cpuFlag)
	assert.Equal(t, "0", cpuFlag.DefValue)

	memoryFlag := cmd.Flags().Lookup("memory")
	assert.NotNil(t, memoryFlag)
	assert.Equal(t, "", memoryFlag.DefValue)

	goldenFlag := cmd.Flags().Lookup("golden")
	assert.NotNil(t, goldenFlag)
	assert.Equal(t, "", goldenFlag.DefValue)

	// Container healthcheck flags (#39).
	for _, name := range []string{"health-cmd", "health-interval", "health-timeout", "health-retries", "health-start-period"} {
		assert.NotNil(t, cmd.Flags().Lookup(name), "service create should register --%s", name)
	}
}

// TestGoldenPlaceholderImage tests the placeholder image used for golden services
func TestGoldenPlaceholderImage(t *testing.T) {
	tests := []struct {
		ref  string
		want string
	}{
		{"ubuntu-24.04-docker@1.0.0", "swarmcracker/golden:ubuntu-24.04-docker-1.0.0"},
		{"ubuntu-24.04-docker", "swarmcracker/golden:ubuntu-24.04-docker"},
		{"org/alpine@2.0.0", "swarmcracker/golden:org-alpine-2.0.0"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, goldenPlaceholderImage(tt.ref))
	}
}

// TestServiceUpdateCommandFlags tests the update command flags
func TestServiceUpdateCommandFlags(t *testing.T) {
	cmd := newServiceUpdateCommand()

	// Check flags exist
	replicasFlag := cmd.Flags().Lookup("replicas")
	assert.NotNil(t, replicasFlag)
	assert.Equal(t, "0", replicasFlag.DefValue)

	cpuLimitFlag := cmd.Flags().Lookup("cpu-limit")
	assert.NotNil(t, cpuLimitFlag)
	assert.Equal(t, "0", cpuLimitFlag.DefValue)

	memoryLimitFlag := cmd.Flags().Lookup("memory-limit")
	assert.NotNil(t, memoryLimitFlag)
	assert.Equal(t, "", memoryLimitFlag.DefValue)

	imageFlag := cmd.Flags().Lookup("image")
	assert.NotNil(t, imageFlag)
	assert.Equal(t, "", imageFlag.DefValue)

	envAddFlag := cmd.Flags().Lookup("env-add")
	assert.NotNil(t, envAddFlag)

	envRmFlag := cmd.Flags().Lookup("env-rm")
	assert.NotNil(t, envRmFlag)

	forceFlag := cmd.Flags().Lookup("force")
	assert.NotNil(t, forceFlag)
	assert.Equal(t, "false", forceFlag.DefValue)

	// Container healthcheck flags (#39).
	for _, name := range []string{"health-cmd", "health-interval", "health-timeout", "health-retries", "health-start-period"} {
		assert.NotNil(t, cmd.Flags().Lookup(name), "service update should register --%s", name)
	}
}

// TestServiceListCommandFlags tests the list command flags
func TestServiceListCommandFlags(t *testing.T) {
	cmd := newServiceListCommand()

	formatFlag := cmd.Flags().Lookup("format")
	assert.NotNil(t, formatFlag)
	assert.Equal(t, "table", formatFlag.DefValue)

	filterFlag := cmd.Flags().Lookup("filter")
	assert.NotNil(t, filterFlag)
	assert.Equal(t, "", filterFlag.DefValue)

	quietFlag := cmd.Flags().Lookup("quiet")
	assert.NotNil(t, quietFlag)
	assert.Equal(t, "false", quietFlag.DefValue)
}

// TestServiceInspectCommandFlags tests the inspect command flags
func TestServiceInspectCommandFlags(t *testing.T) {
	cmd := newServiceInspectCommand()

	assert.Equal(t, "inspect <service-id>", cmd.Use)
	// Cobra uses Args validator (cobra.ExactArgs), not ValidArgs for arg count
	assert.NotNil(t, cmd.Args)

	formatFlag := cmd.Flags().Lookup("format")
	assert.NotNil(t, formatFlag)
	assert.Equal(t, "json", formatFlag.DefValue)

	prettyFlag := cmd.Flags().Lookup("pretty")
	assert.NotNil(t, prettyFlag)
	assert.Equal(t, "true", prettyFlag.DefValue)
}

// TestServicePSCommandFlags tests the ps command flags
func TestServicePSCommandFlags(t *testing.T) {
	cmd := newServicePSCommand()

	assert.Equal(t, "ps <service-id>", cmd.Use)
	// Cobra uses Args validator (cobra.ExactArgs), not ValidArgs for arg count
	assert.NotNil(t, cmd.Args)

	formatFlag := cmd.Flags().Lookup("format")
	assert.NotNil(t, formatFlag)
	assert.Equal(t, "table", formatFlag.DefValue)

	filterFlag := cmd.Flags().Lookup("filter")
	assert.NotNil(t, filterFlag)
	assert.Equal(t, "", filterFlag.DefValue)

	quietFlag := cmd.Flags().Lookup("quiet")
	assert.NotNil(t, quietFlag)
	assert.Equal(t, "false", quietFlag.DefValue)

	noTruncFlag := cmd.Flags().Lookup("no-trunc")
	assert.NotNil(t, noTruncFlag)
	assert.Equal(t, "false", noTruncFlag.DefValue)
}

// TestServiceScaleCommand tests the scale command
func TestServiceScaleCommand(t *testing.T) {
	cmd := newServiceScaleCommand()

	assert.Equal(t, "scale <service-id> <replicas>", cmd.Use)
	assert.Equal(t, "Scale a service", cmd.Short)
	// Cobra uses Args validator (cobra.ExactArgs), not ValidArgs for arg count
	assert.NotNil(t, cmd.Args)
}

// TestServiceRemoveCommandFlags tests the remove command flags
func TestServiceRemoveCommandFlags(t *testing.T) {
	cmd := newServiceRemoveCommand()

	assert.Equal(t, "rm <service-id>", cmd.Use)
	// Cobra uses Args validator (cobra.ExactArgs), not ValidArgs for arg count
	assert.NotNil(t, cmd.Args)

	forceFlag := cmd.Flags().Lookup("force")
	assert.NotNil(t, forceFlag)
	assert.Equal(t, "false", forceFlag.DefValue)

	// Check aliases
	assert.Equal(t, []string{"remove"}, cmd.Aliases)
}

// TestMemoryParsingEdgeCases tests edge cases in memory parsing
func TestMemoryParsingEdgeCases(t *testing.T) {
	// Test whitespace handling
	result, err := parseMemory("  512M  ")
	assert.NoError(t, err)
	assert.Equal(t, int64(512*1024*1024), result)

	// Test mixed case
	result, err = parseMemory("1Gb")
	assert.NoError(t, err)
	assert.Equal(t, int64(1024*1024*1024), result)

	// Test large values
	result, err = parseMemory("100G")
	assert.NoError(t, err)
	assert.Equal(t, int64(100*1024*1024*1024), result)
}

// TestServiceCommandHelp tests that help output works
func TestServiceCommandHelp(t *testing.T) {
	cmd := newServiceCommand()

	// Cobra adds help flags automatically to all commands
	// Test that the help functionality works by checking help template
	assert.NotEmpty(t, cmd.HelpTemplate())

	// Test each subcommand has help template
	for _, subcmd := range cmd.Commands() {
		assert.NotEmpty(t, subcmd.HelpTemplate(), "Subcommand %s should have help template", subcmd.Use)
	}
}

// TestServiceCommandExamples tests that examples are provided
func TestServiceCommandExamples(t *testing.T) {
	createCmd := newServiceCreateCommand()
	assert.NotEmpty(t, createCmd.Example)

	updateCmd := newServiceUpdateCommand()
	assert.NotEmpty(t, updateCmd.Example)

	scaleCmd := newServiceScaleCommand()
	assert.NotEmpty(t, scaleCmd.Example)

	inspectCmd := newServiceInspectCommand()
	assert.NotEmpty(t, inspectCmd.Example)

	psCmd := newServicePSCommand()
	assert.NotEmpty(t, psCmd.Example)
}

// TestHealthFlagsExplicitZeroWiring verifies the cobra plumbing: the per-flag
// Changed bits captured by healthOptionsFromFlags let an explicit zero be told
// apart from an unset flag, so buildHealthcheck rejects it instead of silently
// applying a Docker default. Exercised against the real create command's flags
// without a cluster.
func TestHealthFlagsExplicitZeroWiring(t *testing.T) {
	build := func(t *testing.T, sets map[string]string) healthOptions {
		t.Helper()
		cmd := newServiceCreateCommand()
		for name, val := range sets {
			require.NoError(t, cmd.Flags().Set(name, val))
		}
		cmdStr, _ := cmd.Flags().GetString("health-cmd")
		interval, _ := cmd.Flags().GetString("health-interval")
		timeout, _ := cmd.Flags().GetString("health-timeout")
		retries, _ := cmd.Flags().GetInt("health-retries")
		startPeriod, _ := cmd.Flags().GetString("health-start-period")
		return healthOptionsFromFlags(cmd, cmdStr, interval, timeout, retries, startPeriod)
	}

	t.Run("explicit retries zero is rejected", func(t *testing.T) {
		_, err := buildHealthcheck(build(t, map[string]string{"health-cmd": "true", "health-retries": "0"}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--health-retries")
	})

	t.Run("explicit interval zero is rejected", func(t *testing.T) {
		_, err := buildHealthcheck(build(t, map[string]string{"health-cmd": "true", "health-interval": "0s"}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--health-interval")
	})

	t.Run("explicit timeout zero is rejected", func(t *testing.T) {
		_, err := buildHealthcheck(build(t, map[string]string{"health-cmd": "true", "health-timeout": "0s"}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--health-timeout")
	})

	t.Run("explicit retries without cmd is rejected", func(t *testing.T) {
		_, err := buildHealthcheck(build(t, map[string]string{"health-retries": "3"}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--health-cmd")
	})

	t.Run("valid explicit values are honored", func(t *testing.T) {
		hc, err := buildHealthcheck(build(t, map[string]string{"health-cmd": "true", "health-retries": "5", "health-interval": "7s"}))
		require.NoError(t, err)
		require.NotNil(t, hc)
		assert.Equal(t, int32(5), hc.Retries)
		assert.Equal(t, 7*time.Second, dur(t, hc.Interval))
	})

	t.Run("unset keeps Docker defaults", func(t *testing.T) {
		hc, err := buildHealthcheck(build(t, map[string]string{"health-cmd": "true"}))
		require.NoError(t, err)
		require.NotNil(t, hc)
		assert.Equal(t, int32(3), hc.Retries)
		assert.Equal(t, 30*time.Second, dur(t, hc.Interval))
		assert.Equal(t, 30*time.Second, dur(t, hc.Timeout))
		assert.Equal(t, time.Duration(0), dur(t, hc.StartPeriod))
	})
}

// fakeServiceTaskClient embeds the generated ControlClient interface and
// overrides only the RPCs `service ps` uses.
type fakeServiceTaskClient struct {
	api.ControlClient
	services []*api.Service
	tasks    []*api.Task
}

func (f *fakeServiceTaskClient) ListServices(context.Context, *api.ListServicesRequest, ...grpc.CallOption) (*api.ListServicesResponse, error) {
	return &api.ListServicesResponse{Services: f.services}, nil
}

func (f *fakeServiceTaskClient) ListTasks(context.Context, *api.ListTasksRequest, ...grpc.CallOption) (*api.ListTasksResponse, error) {
	return &api.ListTasksResponse{Tasks: f.tasks}, nil
}

// TestListServiceTasksHealthColumn verifies `service ps` renders a HEALTH
// column with the derived values, keeps the other columns, and honours
// truncation.
func TestListServiceTasksHealthColumn(t *testing.T) {
	client := &fakeServiceTaskClient{
		services: []*api.Service{{ID: "svc-1", Spec: api.ServiceSpec{Annotations: api.Annotations{Name: "web"}}}},
		tasks: []*api.Task{
			healthcheckedTask("task-healthy-000000000000", api.TaskStateRunning),
			healthcheckedTask("task-starting-00000000000", api.TaskStateStarting),
			healthcheckedTask("task-failed-0000000000000", api.TaskStateFailed),
			plainTask("task-plain-000000000000000", api.TaskStateRunning),
		},
	}
	// The tasks above belong to svc-1 so they survive the service filter.
	for _, task := range client.tasks {
		task.ServiceID = "svc-1"
	}

	var buf bytes.Buffer
	require.NoError(t, listServiceTasksWithClient(context.Background(), client, &buf, "web", "table", "", false, false))

	out := buf.String()
	assert.Contains(t, out, "ID")
	assert.Contains(t, out, "STATUS")
	assert.Contains(t, out, "HEALTH")
	assert.Contains(t, out, "NODE")
	assert.Contains(t, out, "IMAGE")

	got := healthColumn(t, out, "ID STATUS HEALTH NODE IMAGE")
	assert.Equal(t, []string{"RUNNING", "STARTING", "FAILED", "-"}, got)
}

// TestListServiceTasksJSONHealth verifies the `service ps --format json` output
// carries the derived health for each task.
func TestListServiceTasksJSONHealth(t *testing.T) {
	client := &fakeServiceTaskClient{
		services: []*api.Service{{ID: "svc-1", Spec: api.ServiceSpec{Annotations: api.Annotations{Name: "web"}}}},
		tasks: []*api.Task{
			healthcheckedTask("task-healthy-000000000000", api.TaskStateRunning),
			plainTask("task-plain-000000000000000", api.TaskStateRunning),
		},
	}
	for _, task := range client.tasks {
		task.ServiceID = "svc-1"
	}

	var buf bytes.Buffer
	require.NoError(t, listServiceTasksWithClient(context.Background(), client, &buf, "web", "json", "", false, false))

	var got []map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	require.Len(t, got, 2)
	assert.Equal(t, "RUNNING", got[0]["health"])
	assert.Equal(t, "-", got[1]["health"])
}
