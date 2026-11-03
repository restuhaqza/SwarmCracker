package agent

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"time"
)

// AgentVersion is the guest agent's release version, reported in every hello
// reply. The Makefile overrides it at build time with -X.
var AgentVersion = "0.0.0-dev"

// BuildID identifies the exact agent build, normally the git commit, and is
// reported in every hello reply so a host can tell two builds of the same
// version apart. The Makefile overrides it at build time with -X.
var BuildID = "unknown"

var (
	bootMu sync.RWMutex
	bootID = newBootID()
)

// BootID returns this agent instance's boot identity.
//
// It is minted when the agent process starts, so it differs for every boot of
// the same template, and it is republished on every hello reply. A host that
// already holds a session compares it: a different value means the guest it is
// talking to is not the instance that session describes, so the session is
// dropped instead of being applied to a stranger.
func BootID() string {
	bootMu.RLock()
	defer bootMu.RUnlock()
	return bootID
}

// RotateBootID mints a new boot identity, installs it, and returns it.
//
// The restore path calls it once a guest resumes from a snapshot: a snapshot
// carries the agent's memory, so every value minted before the snapshot
// survives the restore, and only an explicit rotation can tell the host that
// its pre-restore sessions describe a different guest instance. Callers must
// expect the agent to keep serving the connection that triggered the rotation;
// the point is that the *next* hello reports the new identity.
func RotateBootID() string {
	id := newBootID()
	bootMu.Lock()
	bootID = id
	bootMu.Unlock()
	return id
}

// newBootID mints an opaque boot identity: 16 random bytes in hex, falling back
// to a digest of the process and host identity when the kernel entropy source
// is unavailable.
func newBootID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	host, _ := os.Hostname()
	sum := sha256.Sum256(fmt.Appendf(nil, "%s/%d/%d/%d", host, os.Getpid(), os.Getppid(), time.Now().UnixNano()))
	return hex.EncodeToString(sum[:16])
}
