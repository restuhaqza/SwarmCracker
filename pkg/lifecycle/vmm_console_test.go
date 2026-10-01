package lifecycle

import (
	"bytes"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVMMManager_ConsoleStdio_DefaultsToProcessStdio(t *testing.T) {
	m := NewVMMManager(&ManagerConfig{SocketDir: t.TempDir()}).(*VMMManager)

	out, errOut := m.consoleStdio()
	assert.Same(t, os.Stdout, out)
	assert.Same(t, os.Stderr, errOut)
}

func TestVMMManager_ConsoleStdio_Redirects(t *testing.T) {
	m := NewVMMManager(&ManagerConfig{SocketDir: t.TempDir()}).(*VMMManager)

	buf := &bytes.Buffer{}
	m.SetConsoleWriter(buf)

	out, errOut := m.consoleStdio()
	assert.Same(t, buf, out, "stdout must go to the redirected writer")
	assert.Same(t, buf, errOut, "stderr must go to the redirected writer")
}
