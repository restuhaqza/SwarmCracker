package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/image"
	"github.com/restuhaqza/swarmcracker/pkg/types"
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

	healthPortFlag := cmd.Flags().Lookup("health-port")
	assert.NotNil(t, healthPortFlag)
	assert.Equal(t, strconv.Itoa(defaultHealthPort), healthPortFlag.DefValue)

	noHealthFlag := cmd.Flags().Lookup("no-health")
	assert.NotNil(t, noHealthFlag)
	assert.Equal(t, "false", noHealthFlag.DefValue)
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
	require.NoError(t, listServiceTasksWithClient(context.Background(), client, &buf, "web", "table", "", false, false, derivedHealthOptions()))

	out := buf.String()
	assert.Contains(t, out, "ID")
	assert.Contains(t, out, "STATUS")
	assert.Contains(t, out, "HEALTH")
	assert.Contains(t, out, "NODE")
	assert.Contains(t, out, "IMAGE")

	got := healthColumn(t, out, "ID STATUS HEALTH NODE IMAGE ERROR")
	assert.Equal(t, []string{"RUNNING", "STARTING", "FAILED", "-"}, got)
}

// fakeServiceCreateClient embeds the generated ControlClient interface and
// records CreateService calls, so tests can assert whether the create path sent
// a request at all.
type fakeServiceCreateClient struct {
	api.ControlClient
	createCalls int
	spec        *api.ServiceSpec
}

func (f *fakeServiceCreateClient) CreateService(_ context.Context, req *api.CreateServiceRequest, _ ...grpc.CallOption) (*api.CreateServiceResponse, error) {
	f.createCalls++
	f.spec = req.Spec
	return &api.CreateServiceResponse{Service: &api.Service{ID: "svc-new"}}, nil
}

// withDockerConfig points DOCKER_CONFIG at a temp dir holding a docker
// config.json with the given auths JSON, so credential lookups are deterministic
// and independent of the machine's real docker config.
func withDockerConfig(t *testing.T, configJSON string) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte(configJSON), 0o600))
	t.Setenv("DOCKER_CONFIG", dir)
}

// TestServiceCreateWithRegistryAuthFlag verifies --with-registry-auth is
// registered on `service create` and documented in its help output.
func TestServiceCreateWithRegistryAuthFlag(t *testing.T) {
	cmd := newServiceCreateCommand()

	flag := cmd.Flags().Lookup("with-registry-auth")
	require.NotNil(t, flag, "service create must register --with-registry-auth")
	assert.Equal(t, "bool", flag.Value.Type())
	assert.Equal(t, "false", flag.DefValue)
	assert.Contains(t, cmd.UsageString(), "--with-registry-auth")
}

// TestCreateServiceWithRegistryAuth_SendsResolvedCredential verifies the create
// path resolves the image's credential and puts the X-Registry-Auth payload on
// the spec it sends.
func TestCreateServiceWithRegistryAuth_SendsResolvedCredential(t *testing.T) {
	withDockerConfig(t, `{"auths":{"registry.test":{"username":"bob","password":"s3cret"}}}`)

	client := &fakeServiceCreateClient{}
	err := createServiceWithClient(context.Background(), client, serviceCreateOptions{
		name:             "web",
		image:            "registry.test/team/app:1",
		replicas:         1,
		withRegistryAuth: true,
	})
	require.NoError(t, err)

	require.Equal(t, 1, client.createCalls, "the create path must send exactly one CreateService request")
	require.NotNil(t, client.spec)
	require.NotNil(t, client.spec.Task.GetContainer().PullOptions)

	cfg, err := image.DecodeRegistryAuth(client.spec.Task.GetContainer().PullOptions.RegistryAuth)
	require.NoError(t, err)
	assert.Equal(t, "bob", cfg.Username)
	assert.Equal(t, "s3cret", cfg.Password)
}

// TestCreateServiceWithoutRegistryAuth_SendsNoPayload verifies that without the
// flag no PullOptions are sent, so the workers keep using their own credentials.
func TestCreateServiceWithoutRegistryAuth_SendsNoPayload(t *testing.T) {
	withDockerConfig(t, `{"auths":{"registry.test":{"username":"bob","password":"s3cret"}}}`)

	client := &fakeServiceCreateClient{}
	require.NoError(t, createServiceWithClient(context.Background(), client, serviceCreateOptions{
		name:     "web",
		image:    "registry.test/team/app:1",
		replicas: 1,
	}))

	require.Equal(t, 1, client.createCalls)
	require.NotNil(t, client.spec)
	assert.Nil(t, client.spec.Task.GetContainer().PullOptions)
}

// TestCreateServiceWithRegistryAuth_MissingCredentialFailsBeforeRPC verifies a
// missing credential is an actionable, ErrNoRegistryCredentials-wrapped error
// and that no CreateService request is sent.
func TestCreateServiceWithRegistryAuth_MissingCredentialFailsBeforeRPC(t *testing.T) {
	// A config without a credential for the image's registry: the lookup fails
	// deterministically regardless of the machine's own docker config.
	withDockerConfig(t, `{"auths":{"other.registry.test":{"username":"x","password":"y"}}}`)

	client := &fakeServiceCreateClient{}
	err := createServiceWithClient(context.Background(), client, serviceCreateOptions{
		name:             "web",
		image:            "registry.test/team/app:1",
		replicas:         1,
		withRegistryAuth: true,
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, image.ErrNoRegistryCredentials)
	assert.Contains(t, err.Error(), "registry.test", "the error must name the registry")
	assert.Contains(t, err.Error(), "docker login registry.test", "the error must name the remedy")
	assert.Zero(t, client.createCalls, "a failed credential resolution must not send a request")
	assert.Nil(t, client.spec)
}

// TestServiceCreateWithRegistryAuthRejectsGolden verifies --with-registry-auth is
// rejected when combined with --golden: golden services are never pulled from a
// registry, so the flag would be accepted-and-inert. The error is returned before
// any RPC (or connection attempt).
func TestServiceCreateWithRegistryAuthRejectsGolden(t *testing.T) {
	cmd := newServiceCreateCommand()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"--name", "web", "--golden", "ubuntu-24.04-docker@1.0.0", "--with-registry-auth"})

	err := cmd.Execute()
	require.Error(t, err, "--with-registry-auth with --golden must fail before any RPC")
	assert.Contains(t, err.Error(), "--with-registry-auth")
	assert.Contains(t, err.Error(), "--golden")
}

// TestResolveRegistryAuth_NonCredentialFailureHasNoLoginHint verifies that a
// failure which is not missing credentials wraps its own cause without
// suggesting `docker login`, which would not fix it. The input has to get past
// both reference parsing and the credential-absence check, or the branch under
// test is never exercised: a `credHelpers` entry naming a helper that does not
// exist makes the keychain fail with an exec error for a parseable registry.test
// ref.
func TestResolveRegistryAuth_NonCredentialFailureHasNoLoginHint(t *testing.T) {
	withDockerConfig(t, `{"credHelpers":{"registry.test":"definitely-not-a-real-helper-xyz"}}`)

	_, err := resolveRegistryAuth("registry.test/team/app:1")
	require.Error(t, err)
	assert.NotErrorIs(t, err, image.ErrNoRegistryCredentials)
	assert.NotContains(t, err.Error(), "docker login", "a non-credential failure must not suggest docker login")
	assert.Contains(t, err.Error(), "--with-registry-auth")
	assert.Contains(t, err.Error(), "definitely-not-a-real-helper-xyz",
		"the underlying credential-helper failure must be surfaced, not replaced")
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
	require.NoError(t, listServiceTasksWithClient(context.Background(), client, &buf, "web", "json", "", false, false, derivedHealthOptions()))

	var got []map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	require.Len(t, got, 2)
	assert.Equal(t, "RUNNING", got[0]["health"])
	assert.Equal(t, "-", got[1]["health"])
}

// fakeServiceUpdateClient embeds the generated ControlClient and records the
// service lookups and the update, so tests can assert the registry-auth
// ordering (no RPC before a failed resolution) and what spec was submitted.
type fakeServiceUpdateClient struct {
	api.ControlClient
	service     *api.Service
	listCalls   int
	getCalls    int
	updateCalls int
	updatedSpec *api.ServiceSpec
}

func (f *fakeServiceUpdateClient) ListServices(context.Context, *api.ListServicesRequest, ...grpc.CallOption) (*api.ListServicesResponse, error) {
	f.listCalls++
	if f.service == nil {
		return &api.ListServicesResponse{}, nil
	}
	return &api.ListServicesResponse{Services: []*api.Service{f.service}}, nil
}

func (f *fakeServiceUpdateClient) GetService(context.Context, *api.GetServiceRequest, ...grpc.CallOption) (*api.GetServiceResponse, error) {
	f.getCalls++
	return &api.GetServiceResponse{Service: f.service}, nil
}

func (f *fakeServiceUpdateClient) UpdateService(_ context.Context, req *api.UpdateServiceRequest, _ ...grpc.CallOption) (*api.UpdateServiceResponse, error) {
	f.updateCalls++
	f.updatedSpec = req.Spec
	return &api.UpdateServiceResponse{}, nil
}

// serviceWithRegistryAuth builds a one-container service carrying the given
// X-Registry-Auth payload (empty leaves PullOptions nil).
func serviceWithRegistryAuth(t *testing.T, registryAuth string) *api.Service {
	t.Helper()
	container := &api.ContainerSpec{Image: "registry.test/team/app:1"}
	if registryAuth != "" {
		container.PullOptions = &api.ContainerSpec_PullOptions{RegistryAuth: registryAuth}
	}
	return &api.Service{
		ID: "svc-1",
		Spec: api.ServiceSpec{
			Annotations: api.Annotations{Name: "web"},
			Task: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{Container: container},
			},
		},
	}
}

// TestServiceUpdateWithRegistryAuthFlag verifies --with-registry-auth is
// registered on `service update` and documented in its help output.
func TestServiceUpdateWithRegistryAuthFlag(t *testing.T) {
	cmd := newServiceUpdateCommand()

	flag := cmd.Flags().Lookup("with-registry-auth")
	require.NotNil(t, flag, "service update must register --with-registry-auth")
	assert.Equal(t, "bool", flag.Value.Type())
	assert.Equal(t, "false", flag.DefValue)
	assert.Contains(t, cmd.UsageString(), "--with-registry-auth")
}

// TestUpdateServiceWithoutRegistryAuth_PreservesCredential verifies an update
// that does not mention --with-registry-auth keeps the credential already on
// the service instead of silently dropping it.
func TestUpdateServiceWithoutRegistryAuth_PreservesCredential(t *testing.T) {
	withDockerConfig(t, `{"auths":{}}`)

	client := &fakeServiceUpdateClient{service: serviceWithRegistryAuth(t, "existing-payload")}
	require.NoError(t, updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
		replicas:    2,
		replicasSet: true,
	}))

	require.Equal(t, 1, client.updateCalls)
	require.NotNil(t, client.updatedSpec)
	require.NotNil(t, client.updatedSpec.Task.GetContainer().PullOptions)
	assert.Equal(t, "existing-payload", client.updatedSpec.Task.GetContainer().PullOptions.RegistryAuth)
}

// TestUpdateServiceWithImageChangeWithoutFlag_PreservesOldRegistryCredential
// covers the surprise the docs warn about: `--image` names a different registry
// but --with-registry-auth is omitted, so the previous registry's payload is
// carried forward through svc.Spec.Copy() and the next pull fails with a 401.
// Asserting on the spec sent to UpdateService pins that behaviour.
func TestUpdateServiceWithImageChangeWithoutFlag_PreservesOldRegistryCredential(t *testing.T) {
	withDockerConfig(t, `{"auths":{"other.registry.test":{"username":"bob","password":"s3cret"}}}`)

	client := &fakeServiceUpdateClient{service: serviceWithRegistryAuth(t, "previous-registry-payload")}
	require.NoError(t, updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
		image: "other.registry.test/team/app:1",
	}))

	require.Equal(t, 1, client.updateCalls)
	require.NotNil(t, client.updatedSpec)
	container := client.updatedSpec.Task.GetContainer()
	require.NotNil(t, container)
	assert.Equal(t, "other.registry.test/team/app:1", container.Image)
	require.NotNil(t, container.PullOptions,
		"omitting --with-registry-auth must not drop the credential the spec already carries")
	assert.Equal(t, "previous-registry-payload", container.PullOptions.RegistryAuth,
		"the previous registry's payload is carried forward to the new image's registry")
}

// TestUpdateServiceWithRegistryAuth_SendsResolvedCredential verifies the update
// path resolves the target image's credential and replaces the payload on the
// spec it sends.
func TestUpdateServiceWithRegistryAuth_SendsResolvedCredential(t *testing.T) {
	withDockerConfig(t, `{"auths":{"registry.test":{"username":"bob","password":"s3cret"}}}`)

	client := &fakeServiceUpdateClient{service: serviceWithRegistryAuth(t, "old-payload")}
	err := updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
		image:               "registry.test/team/app:2",
		withRegistryAuth:    true,
		withRegistryAuthSet: true,
	})
	require.NoError(t, err)

	require.Equal(t, 1, client.updateCalls)
	require.NotNil(t, client.updatedSpec)
	require.NotNil(t, client.updatedSpec.Task.GetContainer().PullOptions)

	cfg, err := image.DecodeRegistryAuth(client.updatedSpec.Task.GetContainer().PullOptions.RegistryAuth)
	require.NoError(t, err)
	assert.Equal(t, "bob", cfg.Username)
	assert.Equal(t, "s3cret", cfg.Password)
}

// TestUpdateServiceWithRegistryAuthFalse_ClearsCredential verifies an explicit
// --with-registry-auth=false drops the credential from the updated spec.
func TestUpdateServiceWithRegistryAuthFalse_ClearsCredential(t *testing.T) {
	withDockerConfig(t, `{"auths":{}}`)

	client := &fakeServiceUpdateClient{service: serviceWithRegistryAuth(t, "old-payload")}
	require.NoError(t, updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
		withRegistryAuth:    false,
		withRegistryAuthSet: true,
	}))

	require.Equal(t, 1, client.updateCalls)
	require.NotNil(t, client.updatedSpec)
	assert.Nil(t, client.updatedSpec.Task.GetContainer().PullOptions)
}

// TestUpdateServiceWithRegistryAuth_MissingCredentialFailsBeforeUpdateRPC verifies a
// missing credential is an actionable, ErrNoRegistryCredentials-wrapped error
// and that the update RPC is never sent. The service lookup does run: the
// golden rejection needs the fetched spec and therefore precedes the
// resolution, but it is a read that mutates nothing.
func TestUpdateServiceWithRegistryAuth_MissingCredentialFailsBeforeUpdateRPC(t *testing.T) {
	withDockerConfig(t, `{"auths":{"other.registry.test":{"username":"x","password":"y"}}}`)

	client := &fakeServiceUpdateClient{service: serviceWithRegistryAuth(t, "old-payload")}
	err := updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
		image:               "registry.test/team/app:2",
		withRegistryAuth:    true,
		withRegistryAuthSet: true,
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, image.ErrNoRegistryCredentials)
	assert.Contains(t, err.Error(), "registry.test", "the error must name the registry")
	assert.Contains(t, err.Error(), "docker login registry.test", "the error must name the remedy")
	assert.Equal(t, 1, client.getCalls, "the service must be fetched to decide whether it is golden")
	assert.Zero(t, client.updateCalls, "a failed resolution must not send the update RPC")
	assert.Nil(t, client.updatedSpec)
}

// TestUpdateServiceWithRegistryAuth_RequiresImage verifies setting credentials
// without --image fails before any RPC: the credential is resolved for the
// target image, which the caller must therefore name.
func TestUpdateServiceWithRegistryAuth_RequiresImage(t *testing.T) {
	withDockerConfig(t, `{"auths":{"registry.test":{"username":"bob","password":"s3cret"}}}`)

	client := &fakeServiceUpdateClient{service: serviceWithRegistryAuth(t, "old-payload")}
	err := updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
		withRegistryAuth:    true,
		withRegistryAuthSet: true,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "--image")
	assert.Zero(t, client.listCalls)
	assert.Zero(t, client.updateCalls)
}

// TestUpdateServiceWithRegistryAuth_RejectsRollback verifies --with-registry-auth
// cannot be combined with --rollback, which ignores other flags, so the flag is
// never accepted-and-ignored.
func TestUpdateServiceWithRegistryAuth_RejectsRollback(t *testing.T) {
	client := &fakeServiceUpdateClient{service: serviceWithRegistryAuth(t, "old-payload")}
	err := updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
		image:               "registry.test/team/app:2",
		withRegistryAuth:    true,
		withRegistryAuthSet: true,
		rollbackAction:      true,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "--rollback")
	assert.Zero(t, client.listCalls)
	assert.Zero(t, client.updateCalls)
}

// TestUpdateServiceWithRegistryAuth_RejectsGolden verifies setting credentials
// on a golden service fails: golden images are not pulled from a registry, so
// the flag would otherwise be stored and never consumed.
func TestUpdateServiceWithRegistryAuth_RejectsGolden(t *testing.T) {
	withDockerConfig(t, `{"auths":{"registry.test":{"username":"bob","password":"s3cret"}}}`)

	svc := serviceWithRegistryAuth(t, "old-payload")
	svc.Spec.Annotations.Labels = map[string]string{types.GoldenLabel: "ubuntu-24.04-docker@1.0.0"}
	client := &fakeServiceUpdateClient{service: svc}

	err := updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
		image:               "registry.test/team/app:2",
		withRegistryAuth:    true,
		withRegistryAuthSet: true,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "--with-registry-auth")
	assert.Contains(t, err.Error(), "golden")
	assert.Zero(t, client.updateCalls)
}

// TestUpdateServiceWithRegistryAuth_RejectsGoldenBeforeResolvingCredential
// pins the ordering fix: the golden decision is made from the fetched service
// and must come before the credential is resolved. With no credential stored
// for the image's registry, a resolution-first ordering would report
// ErrNoRegistryCredentials instead of the more helpful golden error.
func TestUpdateServiceWithRegistryAuth_RejectsGoldenBeforeResolvingCredential(t *testing.T) {
	// No credential for registry.test, so a resolution attempted first fails.
	withDockerConfig(t, `{"auths":{"other.registry.test":{"username":"x","password":"y"}}}`)

	svc := serviceWithRegistryAuth(t, "old-payload")
	svc.Spec.Annotations.Labels = map[string]string{types.GoldenLabel: "ubuntu-24.04-docker@1.0.0"}
	client := &fakeServiceUpdateClient{service: svc}

	err := updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
		image:               "registry.test/team/app:2",
		withRegistryAuth:    true,
		withRegistryAuthSet: true,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "--with-registry-auth")
	assert.Contains(t, err.Error(), "golden")
	assert.NotErrorIs(t, err, image.ErrNoRegistryCredentials,
		"the golden service must be rejected before the credential is resolved")
	assert.Zero(t, client.updateCalls)
}

// TestServiceBalloonFlagRegistered verifies --balloon-mib is registered on both
// service commands, so the flag a user types is never an unknown-flag error and
// is documented in their help output.
func TestServiceBalloonFlagRegistered(t *testing.T) {
	create := newServiceCreateCommand()
	flag := create.Flags().Lookup("balloon-mib")
	require.NotNil(t, flag, "service create must register --balloon-mib")
	assert.Equal(t, "int", flag.Value.Type())
	assert.Equal(t, "0", flag.DefValue)
	assert.Contains(t, create.UsageString(), "--balloon-mib")

	update := newServiceUpdateCommand()
	flag = update.Flags().Lookup("balloon-mib")
	require.NotNil(t, flag, "service update must register --balloon-mib")
	assert.Equal(t, "int", flag.Value.Type())
	assert.Equal(t, "0", flag.DefValue)
	assert.Contains(t, update.UsageString(), "--balloon-mib")
}

// TestCreateService_BalloonLabel verifies --balloon-mib reaches the spec the
// create path submits, that an unset flag adds nothing, and that an invalid
// target fails before the create RPC.
func TestCreateService_BalloonLabel(t *testing.T) {
	t.Run("target reaches the spec", func(t *testing.T) {
		client := &fakeServiceCreateClient{}
		require.NoError(t, createServiceWithClient(context.Background(), client, serviceCreateOptions{
			name: "web", image: "nginx", replicas: 1,
			memory: "1G", balloonMib: 256, balloonSet: true,
		}))

		require.Equal(t, 1, client.createCalls)
		require.NotNil(t, client.spec)
		assert.Equal(t, "256", client.spec.Annotations.Labels[types.BalloonLabel])
	})

	t.Run("no flag leaves no label", func(t *testing.T) {
		client := &fakeServiceCreateClient{}
		require.NoError(t, createServiceWithClient(context.Background(), client, serviceCreateOptions{
			name: "web", image: "nginx", replicas: 1,
		}))

		require.Equal(t, 1, client.createCalls)
		_, ok := client.spec.Annotations.Labels[types.BalloonLabel]
		assert.False(t, ok, "an unset --balloon-mib must not add a label")
	})

	t.Run("invalid target sends no RPC", func(t *testing.T) {
		client := &fakeServiceCreateClient{}
		err := createServiceWithClient(context.Background(), client, serviceCreateOptions{
			name: "web", image: "nginx", replicas: 1,
			balloonMib: -1, balloonSet: true,
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "--balloon-mib -1")
		assert.Zero(t, client.createCalls, "an invalid balloon must not send the create RPC")
		assert.Nil(t, client.spec)
	})
}

// serviceWithBalloon builds a one-container service carrying an optional
// swarmcracker.balloon label and, when memoryBytes is positive, a memory limit.
func serviceWithBalloon(label string, memoryBytes int64) *api.Service {
	labels := map[string]string{}
	if label != "" {
		labels[types.BalloonLabel] = label
	}
	svc := &api.Service{
		ID: "svc-1",
		Spec: api.ServiceSpec{
			Annotations: api.Annotations{Name: "web", Labels: labels},
			Task: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{Container: &api.ContainerSpec{Image: "nginx"}},
			},
		},
	}
	if memoryBytes > 0 {
		svc.Spec.Task.Resources = &api.ResourceRequirements{
			Limits: &api.Resources{MemoryBytes: memoryBytes},
		}
	}
	return svc
}

// TestUpdateServiceBalloon_TriState verifies --balloon-mib is tri-state on
// `service update`: absent preserves the current label, a value sets it, and an
// explicit 0 removes it. It also pins the validation against the updated
// service's memory, before the update RPC.
func TestUpdateServiceBalloon_TriState(t *testing.T) {
	t.Run("absent preserves the current label", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithBalloon("256", 0)}
		require.NoError(t, updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			replicas: 2, replicasSet: true,
		}))

		require.Equal(t, 1, client.updateCalls)
		assert.Equal(t, "256", client.updatedSpec.Annotations.Labels[types.BalloonLabel])
	})

	t.Run("value sets the label", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithBalloon("256", 0)}
		require.NoError(t, updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			balloonMib: 128, balloonSet: true,
		}))

		require.Equal(t, 1, client.updateCalls)
		assert.Equal(t, "128", client.updatedSpec.Annotations.Labels[types.BalloonLabel])
	})

	t.Run("explicit zero removes the label", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithBalloon("256", 0)}
		require.NoError(t, updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			balloonMib: 0, balloonSet: true,
		}))

		require.Equal(t, 1, client.updateCalls)
		_, ok := client.updatedSpec.Annotations.Labels[types.BalloonLabel]
		assert.False(t, ok, "an explicit --balloon-mib 0 must remove the label")
	})

	t.Run("validated against the memory given with the update", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithBalloon("", 0)}
		require.NoError(t, updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			memoryLimit: "1G", balloonMib: 512, balloonSet: true,
		}))

		require.Equal(t, 1, client.updateCalls)
		assert.Equal(t, "512", client.updatedSpec.Annotations.Labels[types.BalloonLabel])
	})

	t.Run("target not smaller than the service memory is rejected before the RPC", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithBalloon("", 512*1024*1024)}
		err := updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			balloonMib: 512, balloonSet: true,
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "--balloon-mib 512")
		assert.Contains(t, err.Error(), "memory size 512")
		assert.Zero(t, client.updateCalls, "an invalid target must not send the update RPC")
		assert.Nil(t, client.updatedSpec)
	})

	t.Run("negative target is rejected", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithBalloon("", 0)}
		err := updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			balloonMib: -1, balloonSet: true,
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "--balloon-mib -1")
		assert.Zero(t, client.updateCalls)
	})
}

// serviceWithReservationOnly builds a one-container service whose size lives
// only in Resources.Reservations — the shape an API client can create, which
// this CLI itself never does (it writes Limits).
func serviceWithReservationOnly(memoryBytes int64) *api.Service {
	svc := serviceWithBalloon("", 0)
	svc.Spec.Task.Resources = &api.ResourceRequirements{
		Reservations: &api.Resources{MemoryBytes: memoryBytes},
	}
	return svc
}

// TestUpdateServiceBalloon_ReservationOnlyMemory pins that `service update`
// validates --balloon-mib against the effective size of the current spec,
// including a size that lives only in Resources.Reservations. Before the fix
// this path read Limits alone and fell back to the 512 MiB default, so it could
// accept a target the node (which resolves Reservations too) would refuse.
func TestUpdateServiceBalloon_ReservationOnlyMemory(t *testing.T) {
	t.Run("target above the reserved size is rejected before the RPC", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithReservationOnly(1024 * 1024 * 1024)}
		err := updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			balloonMib: 2048, balloonSet: true,
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "--balloon-mib 2048")
		assert.Contains(t, err.Error(), "memory size 1024")
		assert.Zero(t, client.updateCalls, "an invalid target must not send the update RPC")
		assert.Nil(t, client.updatedSpec)
	})

	t.Run("target below the reserved size is accepted", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithReservationOnly(1024 * 1024 * 1024)}
		require.NoError(t, updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			balloonMib: 512, balloonSet: true,
		}))

		require.Equal(t, 1, client.updateCalls)
		assert.Equal(t, "512", client.updatedSpec.Annotations.Labels[types.BalloonLabel])
	})
}
