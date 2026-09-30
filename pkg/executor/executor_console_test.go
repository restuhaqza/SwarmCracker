package executor

import (
	"bytes"
	"io"
	"testing"

	"github.com/restuhaqza/swarmcracker/test/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// consoleVMM is a mock VMM manager that also implements types.ConsoleRedirector.
type consoleVMM struct {
	*mocks.MockVMMManager
	writer io.Writer
}

func (c *consoleVMM) SetConsoleWriter(w io.Writer) { c.writer = w }

func TestSetConsoleWriter_Redirects(t *testing.T) {
	vmm := &consoleVMM{MockVMMManager: mocks.NewMockVMMManager()}
	exec, err := NewFirecrackerExecutor(
		&Config{}, vmm, mocks.NewMockTaskTranslator(),
		mocks.NewMockImagePreparer(), mocks.NewMockNetworkManager(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = exec.Close() })

	buf := &bytes.Buffer{}
	require.NoError(t, exec.SetConsoleWriter(buf))
	assert.Same(t, buf, vmm.writer)
}

func TestSetConsoleWriter_UnsupportedManager(t *testing.T) {
	exec, err := NewFirecrackerExecutor(
		&Config{}, mocks.NewMockVMMManager(), mocks.NewMockTaskTranslator(),
		mocks.NewMockImagePreparer(), mocks.NewMockNetworkManager(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = exec.Close() })

	err = exec.SetConsoleWriter(io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "console redirection")
}
