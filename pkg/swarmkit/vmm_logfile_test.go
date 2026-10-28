package swarmkit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVMMManagerOpenLogFileDisabled verifies that no log file is created when
// no log directory is configured.
func TestVMMManagerOpenLogFileDisabled(t *testing.T) {
	vmm := &VMMManager{logger: zerolog.Nop()}
	f, err := vmm.openLogFile("vm")
	require.NoError(t, err)
	assert.Nil(t, f, "no log dir should disable file logging")
}

// TestVMMManagerLogFileLifecycle verifies a VM's console log file is created,
// written and closed under the <log-dir>/<task-id>.log convention.
func TestVMMManagerLogFileLifecycle(t *testing.T) {
	dir := t.TempDir()
	vmm := &VMMManager{logDir: dir, logger: zerolog.Nop()}

	f, err := vmm.openLogFile("task-1")
	require.NoError(t, err)
	require.NotNil(t, f)
	vmm.setLogFile("task-1", f)

	path := filepath.Join(dir, "task-1.log")
	_, err = os.Stat(path)
	require.NoError(t, err, "log file should be created")

	_, err = f.WriteString("guest output\n")
	require.NoError(t, err)

	vmm.closeLogFile("task-1")
	_, err = f.WriteString("after close\n")
	assert.Error(t, err, "log file should be closed")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "guest output\n", string(data))
}

// TestVMMManagerCloseConsoleClosesLogFile verifies console teardown also closes
// the associated log file, so no descriptors leak after a VM is removed.
func TestVMMManagerCloseConsoleClosesLogFile(t *testing.T) {
	dir := t.TempDir()
	vmm := &VMMManager{logDir: dir, logger: zerolog.Nop()}

	f, err := vmm.openLogFile("task-2")
	require.NoError(t, err)
	vmm.setLogFile("task-2", f)

	vmm.closeConsole("task-2")
	_, err = f.WriteString("x")
	assert.Error(t, err, "closeConsole should close the log file")
}

// TestVMMManagerOpenLogFileAppendsOnRestart pins the retention rule inside one
// task: a restart keeps the earlier console output and records a boundary, so a
// crash investigation never loses the evidence that came before it.
func TestVMMManagerOpenLogFileAppendsOnRestart(t *testing.T) {
	dir := t.TempDir()
	vmm := &VMMManager{logDir: dir, logger: zerolog.Nop()}

	first, err := vmm.openLogFile("task-restart")
	require.NoError(t, err)
	_, err = first.WriteString("first run output\n")
	require.NoError(t, err)
	vmm.closeLogFile("task-restart")

	second, err := vmm.openLogFile("task-restart")
	require.NoError(t, err)
	_, err = second.WriteString("second run output\n")
	require.NoError(t, err)
	vmm.closeLogFile("task-restart")

	data, err := os.ReadFile(filepath.Join(dir, "task-restart.log"))
	require.NoError(t, err)
	text := string(data)

	assert.Contains(t, text, "first run output", "a restart must not truncate earlier output")
	assert.Contains(t, text, "second run output")
	assert.Contains(t, text, "console restarted at", "a restart must be marked, not silent")
	assert.Less(t, strings.Index(text, "first run output"), strings.Index(text, "second run output"),
		"output stays in the order it happened")
}

// TestVMMManagerOpenLogFileNoMarkerOnFirstBoot pins that an empty log gets no
// boundary line: a first boot has nothing to separate.
func TestVMMManagerOpenLogFileNoMarkerOnFirstBoot(t *testing.T) {
	dir := t.TempDir()
	vmm := &VMMManager{logDir: dir, logger: zerolog.Nop()}

	f, err := vmm.openLogFile("task-fresh")
	require.NoError(t, err)
	require.NotNil(t, f)
	vmm.closeLogFile("task-fresh")

	data, err := os.ReadFile(filepath.Join(dir, "task-fresh.log"))
	require.NoError(t, err)
	assert.Empty(t, string(data))
}
