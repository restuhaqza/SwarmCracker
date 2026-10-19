package client

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDefaultConfigFromEnv(t *testing.T) {
	t.Setenv("SWARM_STATE_DIR", "/tmp/swarm-state")
	t.Setenv("SWARM_SOCKET", "/tmp/swarm.sock")

	cfg := DefaultConfig()
	assert.Equal(t, "/tmp/swarm.sock", cfg.SocketPath)
	assert.Equal(t, "/tmp/swarm-state", cfg.StateDir)
	assert.Equal(t, filepath.Join("/tmp/swarm-state", "certificates"), cfg.CertDir)
	assert.Equal(t, 10*time.Second, cfg.Timeout)
}

func TestConfigWithDefaults(t *testing.T) {
	t.Setenv("SWARM_STATE_DIR", "")
	t.Setenv("SWARM_SOCKET", "")

	cfg := Config{StateDir: "/tmp/custom"}.withDefaults()
	assert.Equal(t, "/var/run/swarmkit/swarm.sock", cfg.SocketPath)
	assert.Equal(t, "/tmp/custom", cfg.StateDir)
	assert.Equal(t, "/tmp/custom/certificates", cfg.CertDir)
	assert.Equal(t, 10*time.Second, cfg.Timeout)

	cfg = Config{CertDir: "/tmp/certs", Timeout: time.Second}.withDefaults()
	assert.Equal(t, "/tmp/certs", cfg.CertDir)
	assert.Equal(t, time.Second, cfg.Timeout)
}
