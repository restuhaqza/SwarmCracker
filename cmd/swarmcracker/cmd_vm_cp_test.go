package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVMCpCommandStructure(t *testing.T) {
	cmd := newVMCpCommand()
	require.Equal(t, "cp", cmd.Name())

	for _, name := range []string{"socket-dir", "mode"} {
		assert.NotNilf(t, cmd.Flags().Lookup(name), "flag %q not found", name)
	}
	assert.Equal(t, "0644", cmd.Flags().Lookup("mode").DefValue)
	assert.Equal(t, defaultConsoleSocketDir, cmd.Flags().Lookup("socket-dir").DefValue)

	require.NotNil(t, cmd.Args)
	assert.NoError(t, cmd.Args(cmd, []string{"a", "b"}))
	assert.Error(t, cmd.Args(cmd, []string{"a"}))
	assert.Error(t, cmd.Args(cmd, []string{"a", "b", "c"}))
}

func TestParseFileMode(t *testing.T) {
	cases := map[string]uint32{
		"0644":  0o644,
		"644":   0o644,
		"0o755": 0o755,
		"600":   0o600,
	}
	for in, want := range cases {
		got, err := parseFileMode(in)
		require.NoErrorf(t, err, "parseFileMode(%q)", in)
		assert.Equalf(t, want, got, "parseFileMode(%q)", in)
	}

	_, err := parseFileMode("not-a-mode")
	require.Error(t, err)
}

func TestSplitCopyArg(t *testing.T) {
	ref, path, ok := splitCopyArg("myvm:/tmp/x")
	assert.True(t, ok)
	assert.Equal(t, "myvm", ref)
	assert.Equal(t, "/tmp/x", path)

	ref, path, ok = splitCopyArg("/tmp/local")
	assert.False(t, ok)
	assert.Empty(t, ref)
	assert.Equal(t, "/tmp/local", path)

	_, _, ok = splitCopyArg(":/tmp/x")
	assert.False(t, ok)

	_, _, ok = splitCopyArg("myvm:")
	assert.False(t, ok)
}

func TestVMCpRejectsBothOrNeitherPrefixed(t *testing.T) {
	err := copyFileToOrFromVM(context.Background(), "/nonexistent", "a:/x", "b:/y", 0o644)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one")

	err = copyFileToOrFromVM(context.Background(), "/nonexistent", "/x", "/y", 0o644)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one")
}

func TestVMCpHostToGuest(t *testing.T) {
	vm := startFakeVM(t, "vm-cp-up", nil)

	local := filepath.Join(t.TempDir(), "local.txt")
	require.NoError(t, os.WriteFile(local, []byte("upload payload"), 0o644))

	guest := filepath.Join(t.TempDir(), "guest.txt")
	require.NoError(t, copyFileToOrFromVM(context.Background(), vm.SocketDir, local, "vm-cp-up:"+guest, 0o600))

	got, err := os.ReadFile(guest)
	require.NoError(t, err)
	assert.Equal(t, "upload payload", string(got))

	info, err := os.Stat(guest)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestVMCpGuestToHost(t *testing.T) {
	vm := startFakeVM(t, "vm-cp-down", nil)

	guest := filepath.Join(t.TempDir(), "guest.txt")
	require.NoError(t, os.WriteFile(guest, []byte("download payload"), 0o644))

	local := filepath.Join(t.TempDir(), "local.txt")
	require.NoError(t, copyFileToOrFromVM(context.Background(), vm.SocketDir, "vm-cp-down:"+guest, local, 0o644))

	got, err := os.ReadFile(local)
	require.NoError(t, err)
	assert.Equal(t, "download payload", string(got))
}

func TestVMCpRejectsDirectorySource(t *testing.T) {
	vm := startFakeVM(t, "vm-cp-dir", nil)

	err := copyFileToOrFromVM(context.Background(), vm.SocketDir, t.TempDir(), "vm-cp-dir:/tmp/dest", 0o644)
	require.ErrorIs(t, err, errCopyDirectory)
}

func TestVMCpRejectsDirectoryDestination(t *testing.T) {
	vm := startFakeVM(t, "vm-cp-dirdst", nil)

	guest := filepath.Join(t.TempDir(), "guest.txt")
	require.NoError(t, os.WriteFile(guest, []byte("data"), 0o644))

	err := copyFileToOrFromVM(context.Background(), vm.SocketDir, "vm-cp-dirdst:"+guest, t.TempDir(), 0o644)
	require.ErrorIs(t, err, errCopyDirectory)
}

// TestVMCpGuestToHostMissingGuestFileKeepsDestination verifies that a failed
// guest->host transfer does not truncate a pre-existing local destination: the
// guest file is missing, so the agent replies with an error frame and the local
// file must be left exactly as it was.
func TestVMCpGuestToHostMissingGuestFileKeepsDestination(t *testing.T) {
	vm := startFakeVM(t, "vm-cp-miss", nil)

	missing := filepath.Join(t.TempDir(), "does-not-exist.txt")
	dir := t.TempDir()
	local := filepath.Join(dir, "local.txt")
	require.NoError(t, os.WriteFile(local, []byte("precious"), 0o644))

	err := copyFileToOrFromVM(context.Background(), vm.SocketDir, "vm-cp-miss:"+missing, local, 0o644)
	require.Error(t, err)

	got, rerr := os.ReadFile(local)
	require.NoError(t, rerr)
	assert.Equal(t, "precious", string(got))

	entries, derr := os.ReadDir(dir)
	require.NoError(t, derr)
	require.Len(t, entries, 1)
	assert.Equal(t, "local.txt", entries[0].Name())
}

func TestVMCpCommandEndToEnd(t *testing.T) {
	vm := startFakeVM(t, "vm-cp-cli", nil)

	local := filepath.Join(t.TempDir(), "cli.txt")
	require.NoError(t, os.WriteFile(local, []byte("cli payload"), 0o644))
	guest := filepath.Join(t.TempDir(), "cli-guest.txt")

	cmd := newVMCpCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--socket-dir", vm.SocketDir, local, "vm-cp-cli:" + guest})

	require.NoError(t, cmd.Execute())

	got, err := os.ReadFile(guest)
	require.NoError(t, err)
	assert.Equal(t, "cli payload", string(got))
}
