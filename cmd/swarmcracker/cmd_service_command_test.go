package main

import (
	"io"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildServiceSpec_CarriesCommandAndArgs pins the value the executor later
// forwards to the guest. Before #97 the spec carried these but the daemon
// translator dropped them, so the image's entrypoint ran instead.
func TestBuildServiceSpec_CarriesCommandAndArgs(t *testing.T) {
	spec, err := buildServiceSpec(serviceCreateOptions{
		name:    "job",
		image:   "alpine",
		command: []string{"/bin/sh"},
		args:    []string{"-c", "echo hello world"},
	})
	require.NoError(t, err)

	container := spec.Task.GetContainer()
	require.NotNil(t, container)
	assert.Equal(t, []string{"/bin/sh"}, container.Command)
	assert.Equal(t, []string{"-c", "echo hello world"}, container.Args)

	argv := append(append([]string{}, container.Command...), container.Args...)
	assert.Equal(t, []string{"/bin/sh", "-c", "echo hello world"}, argv,
		"the guest must receive the override exactly as typed")
}

// TestBuildServiceSpec_RejectsCommandWithGolden verifies a golden service never
// accepts --command/--args: golden images boot their own init, so the flags
// would be silently ignored instead of applied.
func TestBuildServiceSpec_RejectsCommandWithGolden(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts serviceCreateOptions
		flag string
	}{
		{
			name: "command",
			opts: serviceCreateOptions{
				name:    "web",
				labels:  []string{types.GoldenLabel + "=ubuntu-24.04-docker@1.0.0"},
				command: []string{"/bin/false"},
			},
			flag: "--command",
		},
		{
			name: "args",
			opts: serviceCreateOptions{
				name:   "web",
				labels: []string{types.GoldenLabel + "=ubuntu-24.04-docker@1.0.0"},
				args:   []string{"--verbose"},
			},
			flag: "--args",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildServiceSpec(tc.opts)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.flag)
			assert.Contains(t, err.Error(), "--golden")
		})
	}
}

// TestBuildServiceSpec_RejectsUncarryableArgv verifies values the kernel
// command line cannot carry fail at create time instead of being altered at
// boot (a trailing newline is stripped by the wrapper's command substitution).
func TestBuildServiceSpec_RejectsUncarryableArgv(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts serviceCreateOptions
		flag string
	}{
		{
			name: "newline in command",
			opts: serviceCreateOptions{name: "web", image: "alpine", command: []string{"echo\n"}},
			flag: "--command",
		},
		{
			name: "nul in args",
			opts: serviceCreateOptions{name: "web", image: "alpine", args: []string{"a\x00b"}},
			flag: "--args",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildServiceSpec(tc.opts)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.flag)
		})
	}
}

// TestServiceCreateCommandPassesArgsFlag guards the flag-to-option path. The
// command's RunE parameter used to be named `args`, shadowing the --args flag
// variable, so every `service create --args ...` stored nothing.
func TestServiceCreateCommandPassesArgsFlag(t *testing.T) {
	var got serviceCreateOptions
	serviceCreateHook = func(opts serviceCreateOptions) error {
		got = opts
		return nil
	}
	t.Cleanup(func() { serviceCreateHook = nil })

	cmd := newServiceCreateCommand()
	cmd.SetArgs([]string{
		"--name", "job",
		"--image", "alpine",
		"--command=/bin/sh",
		"--args=-c",
		"--args=sleep 3600",
	})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	require.NoError(t, cmd.Execute())
	assert.Equal(t, []string{"/bin/sh"}, got.command)
	assert.Equal(t, []string{"-c", "sleep 3600"}, got.args)
}
