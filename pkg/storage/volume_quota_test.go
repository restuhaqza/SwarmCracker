package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestQuotaEnforcer_CheckCreate(t *testing.T) {
	q := NewQuotaEnforcer()

	// Valid sizes
	for _, size := range []int{0, 100, 1024, 1048576} {
		if err := q.CheckCreate(size); err != nil {
			t.Errorf("CheckCreate(%d) = %v, want nil", size, err)
		}
	}

	// Negative
	if err := q.CheckCreate(-1); err == nil {
		t.Error("CheckCreate(-1) should error")
	}

	// Too large
	if err := q.CheckCreate(1048577); err == nil {
		t.Error("CheckCreate(1048577) should error")
	}
}

func TestQuotaEnforcer_CheckSync(t *testing.T) {
	q := NewQuotaEnforcer()

	// Unlimited quota — should always pass
	if err := q.CheckSync("vol", t.TempDir(), 0); err != nil {
		t.Errorf("unlimited quota should pass: %v", err)
	}

	// Create a temp dir with some data
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file.dat"), make([]byte, 1024), 0644); err != nil {
		t.Fatal(err)
	}

	// Under limit (1KB data, 100MB limit)
	if err := q.CheckSync("vol", dir, 100); err != nil {
		t.Errorf("under limit should pass: %v", err)
	}

	// Zero limit means unlimited in CheckSync
	if err := q.CheckSync("vol", dir, 0); err != nil {
		t.Errorf("0 limit should be unlimited: %v", err)
	}
}

func TestQuotaEnforcer_CheckCapacity(t *testing.T) {
	q := NewQuotaEnforcer()

	// Unlimited
	if err := q.CheckCapacity("vol", t.TempDir(), 0); err != nil {
		t.Error("unlimited should pass")
	}

	// Create data that exceeds limit
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "big.dat"), make([]byte, 2*1024*1024), 0644); err != nil {
		t.Fatal(err)
	}

	err := q.CheckCapacity("vol", dir, 1) // 1 MB limit, 2 MB data
	if err == nil {
		t.Error("should fail when over capacity")
	}
	if _, ok := err.(*QuotaError); !ok {
		t.Errorf("expected *QuotaError, got %T: %v", err, err)
	}
}

func TestQuotaEnforcer_EnforceDirLimit(t *testing.T) {
	q := NewQuotaEnforcer()

	dir := t.TempDir()
	// Write multiple files (1KB each)
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(filepath.Join(dir, string(rune('A'+i))), make([]byte, 1024), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Zero limit means no enforcement
	removed, err := q.EnforceDirLimit(dir, 0)
	if err != nil {
		t.Fatalf("EnforceDirLimit: %v", err)
	}
	if removed != 0 {
		t.Errorf("removed %d, want 0 for unlimited", removed)
	}
}

func TestQuotaError(t *testing.T) {
	e := &QuotaError{
		Volume:    "test",
		LimitMB:   100,
		CurrentMB: 200,
	}
	msg := e.Error()
	if msg == "" {
		t.Error("error message should not be empty")
	}

	// Custom message
	e2 := &QuotaError{Message: "custom error"}
	if e2.Error() != "custom error" {
		t.Errorf("custom message = %q", e2.Error())
	}
}
