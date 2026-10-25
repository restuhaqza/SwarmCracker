package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// brokenWriteFile replaces the write step of fileOps with an unconditional
// failure. A read-only directory would do the same only for an unprivileged
// user: root ignores the mode bits, and some CI sandboxes run as root.
func brokenWriteFile(err error) fileOps {
	ops := osFileOps
	ops.writeFile = func(string, []byte, os.FileMode) error { return err }
	return ops
}

// brokenRename replaces the rename step, the last step before the target file
// changes, with an unconditional failure.
func brokenRename(err error) fileOps {
	ops := osFileOps
	ops.rename = func(string, string) error { return err }
	return ops
}

// assertFile asserts the exact bytes and that no temporary sibling is left.
func assertFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Errorf("%s = %q, want %q", path, got, want)
	}
	if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s.tmp survived a write of %s", path, path)
	}
}

func TestWriteTargetsKeepsPreviousFileWhenTheWriteFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sdk", "python", "swarmcracker", "types.py")
	writeFile(t, path, "previous\n")
	wantErr := errors.New("no space left on device")

	err := writeTargetsWith(brokenWriteFile(wantErr), []target{{path: path, data: []byte("new\n")}})
	if !errors.Is(err, wantErr) {
		t.Fatalf("writeTargetsWith() error = %v, want %v", err, wantErr)
	}
	assertFile(t, path, "previous\n")
}

func TestWriteTargetsKeepsPreviousFileWhenTheRenameFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "types.d.ts")
	writeFile(t, path, "previous\n")
	wantErr := errors.New("rename refused")

	err := writeTargetsWith(brokenRename(wantErr), []target{{path: path, data: []byte("new\n")}})
	if !errors.Is(err, wantErr) {
		t.Fatalf("writeTargetsWith() error = %v, want %v", err, wantErr)
	}
	assertFile(t, path, "previous\n")
}

func TestWriteTargetsCreatesMissingDirectories(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "c.py")

	if err := writeTargetsWith(brokenWriteFile(errors.New("boom")), []target{{path: path, data: []byte("new\n")}}); err == nil {
		t.Fatal("writeTargetsWith() error = nil, want the injected failure")
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Errorf("parent directory was not created: %v", err)
	}
}

// TestWriteTargetsReplacesContentAndLeavesNoTempFile is the positive case the
// two failure tests rely on: the target ends up holding the new bytes and the
// temporary file is gone, so a stale <path>.tmp can never be committed.
func TestWriteTargetsReplacesContentAndLeavesNoTempFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "types.py")
	writeFile(t, path, "previous\n")

	if err := writeTargetsWith(osFileOps, []target{{path: path, data: []byte("new\n")}}); err != nil {
		t.Fatalf("writeTargetsWith() error = %v", err)
	}
	assertFile(t, path, "new\n")
}

// TestRunLeavesNoTempFilesAfterGenerating guards the CLI path end to end: both
// targets are written through the temporary-and-rename route.
func TestRunLeavesNoTempFilesAfterGenerating(t *testing.T) {
	in := newGenerateIn(t)
	stderr := &strings.Builder{}
	if code := run(in.args(), io.Discard, stderr); code != 0 {
		t.Fatalf("run() = %d, stderr = %s", code, stderr.String())
	}
	for _, path := range []string{in.pythonPath, in.tsPath} {
		if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s.tmp was left behind", path)
		}
	}
}
