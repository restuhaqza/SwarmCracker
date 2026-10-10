package storage

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// captureExec replaces execCommand with a recorder that always succeeds, so the
// debugfs invocations can be asserted without a real filesystem.
func captureExec(t *testing.T) *[][]string {
	t.Helper()
	var mu sync.Mutex
	calls := &[][]string{}
	orig := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		mu.Lock()
		*calls = append(*calls, append([]string{name}, args...))
		mu.Unlock()
		return exec.Command("true")
	}
	t.Cleanup(func() { execCommand = orig })
	return calls
}

func joinedCalls(calls *[][]string) string {
	var b strings.Builder
	for _, c := range *calls {
		b.WriteString(strings.Join(c, " "))
		b.WriteByte('\n')
	}
	return b.String()
}

func TestInjectSecrets_SetsModeUIDGID(t *testing.T) {
	calls := captureExec(t)

	sm := NewSecretManager("", "")
	err := sm.InjectSecrets(context.Background(), "t1", []types.SecretRef{
		{ID: "s1", Name: "db", Target: "/run/secrets/db", Data: []byte("x"), Mode: 0o400, UID: "1000", GID: "2000"},
	}, "/tmp/rootfs.ext4")
	if err != nil {
		t.Fatalf("InjectSecrets: %v", err)
	}

	out := joinedCalls(calls)
	for _, want := range []string{
		"sif /run/secrets/db mode 0100400",
		"sif /run/secrets/db uid 1000",
		"sif /run/secrets/db gid 2000",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in debugfs calls:\n%s", want, out)
		}
	}
}

func TestInjectSecrets_DefaultMode(t *testing.T) {
	calls := captureExec(t)

	sm := NewSecretManager("", "")
	// Mode 0 must default to 0400 for secrets.
	if err := sm.InjectSecrets(context.Background(), "t1", []types.SecretRef{
		{ID: "s1", Name: "db", Target: "/run/secrets/db", Data: []byte("x")},
	}, "/tmp/rootfs.ext4"); err != nil {
		t.Fatalf("InjectSecrets: %v", err)
	}
	if out := joinedCalls(calls); !strings.Contains(out, "sif /run/secrets/db mode 0100400") {
		t.Errorf("expected default secret mode 0400:\n%s", out)
	}
}

func TestInjectConfigs_DefaultMode(t *testing.T) {
	calls := captureExec(t)

	sm := NewSecretManager("", "")
	// Mode 0 must default to 0444 for configs.
	if err := sm.InjectConfigs(context.Background(), "t1", []types.ConfigRef{
		{ID: "c1", Name: "app", Target: "/config/app.yaml", Data: []byte("x")},
	}, "/tmp/rootfs.ext4"); err != nil {
		t.Fatalf("InjectConfigs: %v", err)
	}
	if out := joinedCalls(calls); !strings.Contains(out, "sif /config/app.yaml mode 0100444") {
		t.Errorf("expected default config mode 0444:\n%s", out)
	}
}
