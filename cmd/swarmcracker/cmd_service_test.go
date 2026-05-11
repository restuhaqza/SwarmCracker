package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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