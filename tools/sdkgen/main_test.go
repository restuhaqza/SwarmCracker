package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const smallSpec = `openapi: 3.1.0
components:
  schemas:
    Service:
      type: object
      required: [id]
      properties:
        id: {type: string}
`

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// generateIn is one run of the generator against a throwaway tree.
type generateIn struct {
	spec       string
	pythonPath string
	tsPath     string
}

func newGenerateIn(t *testing.T) generateIn {
	t.Helper()
	dir := t.TempDir()
	in := generateIn{
		spec:       filepath.Join(dir, "openapi.yaml"),
		pythonPath: filepath.Join(dir, "sdk", "python", "swarmcracker", "types.py"),
		tsPath:     filepath.Join(dir, "sdk", "node", "src", "types.d.ts"),
	}
	writeFile(t, in.spec, smallSpec)
	return in
}

func (in generateIn) args(extra ...string) []string {
	args := []string{
		"-spec", in.spec,
		"-out-python", in.pythonPath,
		"-out-typescript", in.tsPath,
	}
	return append(args, extra...)
}

func TestRunGeneratesBothFiles(t *testing.T) {
	in := newGenerateIn(t)
	var stdout, stderr bytes.Buffer

	if code := run(in.args(), &stdout, &stderr); code != 0 {
		t.Fatalf("run() = %d, stderr = %s", code, stderr.String())
	}

	python := readFile(t, in.pythonPath)
	typescript := readFile(t, in.tsPath)
	if !strings.HasPrefix(python, "# "+generatedHeader+"\n") {
		t.Errorf("Python output does not start with the pinned header:\n%s", python)
	}
	if !strings.HasPrefix(typescript, "// "+generatedHeader+"\n") {
		t.Errorf("TypeScript output does not start with the pinned header:\n%s", typescript)
	}
	if !strings.Contains(python, "class Service(TypedDict):") {
		t.Errorf("Python output is missing Service:\n%s", python)
	}
	if !strings.Contains(typescript, "export interface Service {") {
		t.Errorf("TypeScript output is missing Service:\n%s", typescript)
	}
	if stdout.Len() != 0 {
		t.Errorf("run() wrote to stdout on success: %s", stdout.String())
	}
}

func TestRunCheckPassesOnFreshOutput(t *testing.T) {
	in := newGenerateIn(t)
	var stdout, stderr bytes.Buffer
	if code := run(in.args(), &stdout, &stderr); code != 0 {
		t.Fatalf("generate run() = %d, stderr = %s", code, stderr.String())
	}
	python := readFile(t, in.pythonPath)
	typescript := readFile(t, in.tsPath)

	stdout.Reset()
	stderr.Reset()
	if code := run(in.args("-check"), &stdout, &stderr); code != 0 {
		t.Fatalf("-check run() = %d, stderr = %s", code, stderr.String())
	}
	if readFile(t, in.pythonPath) != python || readFile(t, in.tsPath) != typescript {
		t.Error("-check rewrote a file")
	}
}

func TestRunCheckDetectsDrift(t *testing.T) {
	in := newGenerateIn(t)
	var stdout, stderr bytes.Buffer
	if code := run(in.args(), &stdout, &stderr); code != 0 {
		t.Fatalf("generate run() = %d, stderr = %s", code, stderr.String())
	}

	writeFile(t, in.pythonPath, "# stale\n")

	stdout.Reset()
	stderr.Reset()
	if code := run(in.args("-check"), &stdout, &stderr); code != 1 {
		t.Fatalf("-check run() = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), in.pythonPath) {
		t.Errorf("stderr does not name the stale file: %s", stderr.String())
	}
	if readFile(t, in.pythonPath) != "# stale\n" {
		t.Error("-check wrote to the stale file")
	}
}

func TestRunCheckDetectsMissingOutput(t *testing.T) {
	in := newGenerateIn(t)
	var stdout, stderr bytes.Buffer
	if code := run(in.args(), &stdout, &stderr); code != 0 {
		t.Fatalf("generate run() = %d, stderr = %s", code, stderr.String())
	}
	if err := os.Remove(in.tsPath); err != nil {
		t.Fatalf("remove %s: %v", in.tsPath, err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(in.args("-check"), &stdout, &stderr); code != 1 {
		t.Fatalf("-check run() = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), in.tsPath) {
		t.Errorf("stderr does not name the missing file: %s", stderr.String())
	}
}

func TestRunReportsBadSpec(t *testing.T) {
	in := newGenerateIn(t)
	writeFile(t, in.spec, "openapi: 3.1.0\ncomponents:\n  schemas:\n    Bad: {type: widget}\n")

	var stdout, stderr bytes.Buffer
	if code := run(in.args(), &stdout, &stderr); code != 1 {
		t.Fatalf("run() = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "#/components/schemas/Bad") {
		t.Errorf("stderr does not carry the JSON path: %s", stderr.String())
	}
	if _, err := os.Stat(in.pythonPath); !os.IsNotExist(err) {
		t.Error("run() wrote output for an invalid spec")
	}
}

// TestCommittedOutputIsCurrent is the guard `make sdk-check` relies on: the
// committed generated files must match the committed spec byte for byte.
func TestCommittedOutputIsCurrent(t *testing.T) {
	root := filepath.Join("..", "..")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"-spec", filepath.Join(root, "api", "openapi.yaml"),
		"-out-python", filepath.Join(root, "sdk", "python", "swarmcracker", "types.py"),
		"-out-typescript", filepath.Join(root, "sdk", "node", "src", "types.d.ts"),
		"-check",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("-check on the committed tree = %d, stderr = %s", code, stderr.String())
	}
}
