package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// This file holds the two gates that prove the identifier rule against the real
// compiler instead of against a list written from memory:
//
//   - every name the generator refuses as a TypeScript declaration (`string`,
//     `any`, `delete`, ...) is refused by the pinned tsc too, and
//   - a fixture full of aggressive-but-legal names is generated and type-checks.
//
// Both need `npm ci` in sdk/node. Without it they skip with an actionable
// message so `go test ./tools/sdkgen` still works on a bare checkout; CI runs
// `npm ci` before `make sdk-typecheck`, which runs them, so they never skip
// silently there.

// pinnedTSC returns the path of the TypeScript compiler pinned as the only
// devDependency of sdk/node and asserts it is that exact version.
func pinnedTSC(t *testing.T) string {
	t.Helper()
	root := filepath.Join("..", "..", "sdk", "node")
	bin := filepath.Join(root, "node_modules", ".bin", "tsc")
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("pinned TypeScript compiler not found at %s: run `npm ci` in sdk/node "+
			"(CI installs it before `make sdk-typecheck`)", bin)
	}

	want := pinnedTypeScriptVersion(t, root)
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		t.Fatalf("%s --version: %v", bin, err)
	}
	got := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "Version "))
	if got != want {
		t.Fatalf("tsc is version %q, but sdk/node/package.json pins %q; run `npm ci` in sdk/node", got, want)
	}
	return bin
}

// pinnedTypeScriptVersion reads devDependencies.typescript from sdk/node.
func pinnedTypeScriptVersion(t *testing.T, root string) string {
	t.Helper()
	var pkg struct {
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(root, "package.json"))), &pkg); err != nil {
		t.Fatalf("parse sdk/node/package.json: %v", err)
	}
	version := pkg.DevDependencies["typescript"]
	if version == "" {
		t.Fatal("sdk/node/package.json does not pin typescript as a devDependency")
	}
	return version
}

// probeDeclaration is one throwaway .d.ts body used to ask tsc about a name.
type probeDeclaration struct {
	// form is "interface" or "alias", the two declaration shapes the emitters
	// produce.
	form string
	// name is the declaration name under test.
	name string
}

// write asks tsc to write the declarations into dir and returns the paths.
func (d probeDeclaration) write(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, d.form+"_"+d.name+".d.ts")
	body := ""
	switch d.form {
	case "interface":
		body = "export interface " + d.name + " {\n  id?: string;\n}\n"
	case "alias":
		body = "export type " + d.name + " = string;\n"
	default:
		t.Fatalf("unknown declaration form %q", d.form)
	}
	writeFile(t, path, body)
	return path
}

// tscFilesWithErrors runs the pinned compiler over paths and returns the set of
// file *base* names it reported a diagnostic for. tsc prints each path relative
// to its own working directory, so the base name is the only stable key.
func tscFilesWithErrors(t *testing.T, tsc string, paths []string) map[string]bool {
	t.Helper()
	args := append([]string{"--noEmit", "--strict"}, paths...)
	out, err := exec.Command(tsc, args...).CombinedOutput()
	reported := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "error TS") {
			continue
		}
		file, _, found := strings.Cut(line, "(")
		if found {
			reported[filepath.Base(file)] = true
		}
	}
	if err != nil && len(reported) == 0 {
		t.Fatalf("tsc failed without a diagnostic (%v):\n%s", err, out)
	}
	return reported
}

// TestPinnedTSCRefusesEveryBlocklistedDeclarationName keeps the TypeScript rule
// honest: every name tools/sdkgen refuses must really be refused by the pinned
// tsc in at least one of the two declaration shapes the emitters produce, and
// the alias-only list must really be legal as an interface name (that is why it
// is a separate list).
//
// The batches are split by diagnostic phase on purpose: tsc reports *syntactic*
// diagnostics for the whole program but stops semantic checking per file, so a
// keyword batch and a type-name batch cannot share one invocation without
// hiding the TS2427/TS2457 errors.
func TestPinnedTSCRefusesEveryBlocklistedDeclarationName(t *testing.T) {
	tsc := pinnedTSC(t)

	cases := []struct {
		name  string
		decls []probeDeclaration
	}{
		{
			name:  "keywords as interfaces",
			decls: declarationsOf("interface", typescriptKeywordBlocklist),
		},
		{
			name:  "type names as interfaces",
			decls: declarationsOf("interface", typescriptTypeNameBlocklist),
		},
		{
			name:  "type names as aliases",
			decls: declarationsOf("alias", typescriptTypeNameBlocklist),
		},
		{
			name:  "alias-only names as aliases",
			decls: declarationsOf("alias", typescriptAliasOnlyBlocklist),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			paths := make([]string, 0, len(tc.decls))
			for _, decl := range tc.decls {
				paths = append(paths, decl.write(t, dir))
			}
			reported := tscFilesWithErrors(t, tsc, paths)
			for i, decl := range tc.decls {
				if !reported[filepath.Base(paths[i])] {
					t.Errorf("tsc accepted %q as a %s declaration (%s), but "+
						"tools/sdkgen refuses it", decl.name, decl.form, paths[i])
				}
			}
			if len(reported) > len(tc.decls) {
				t.Errorf("tsc reported %d files, want at most %d", len(reported), len(tc.decls))
			}
		})
	}

	// The alias-only list exists because tsc accepts these names in interface
	// position: prove that, so the split never looks like an accident.
	t.Run("alias-only names are legal as interfaces", func(t *testing.T) {
		dir := t.TempDir()
		paths := make([]string, 0, len(typescriptAliasOnlyBlocklist))
		for _, name := range typescriptAliasOnlyBlocklist {
			paths = append(paths, probeDeclaration{form: "interface", name: name}.write(t, dir))
		}
		if reported := tscFilesWithErrors(t, tsc, paths); len(reported) != 0 {
			t.Errorf("tsc refused an alias-only name as an interface: %v", reported)
		}
	})
}

// declarationsOf pairs every name with the declaration form under test.
func declarationsOf(form string, names []string) []probeDeclaration {
	decls := make([]probeDeclaration, 0, len(names))
	for _, name := range names {
		decls = append(decls, probeDeclaration{form: form, name: name})
	}
	return decls
}

// TestAcceptedFixtureTypeChecksWithPinnedTSC is the second half of the rule: a
// document full of aggressive-but-legal names (reserved words that are legal
// declaration names, `delete`/`new` members, lib constructor names, aliases) is
// generated into a temporary directory and compiled by the pinned tsc. If the
// validator ever becomes too strict in a way that matters to the compiler, this
// fails; if the emitter ever produces invalid code for a name it accepted, this
// fails too.
func TestAcceptedFixtureTypeChecksWithPinnedTSC(t *testing.T) {
	tsc := pinnedTSC(t)

	dir := t.TempDir()
	pythonPath := filepath.Join(dir, "types.py")
	tsPath := filepath.Join(dir, "accepted.d.ts")
	var stdout, stderr bytes.Buffer
	if code := run([]string{
		"-spec", "testdata/accepted.yaml",
		"-out-python", pythonPath,
		"-out-typescript", tsPath,
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("generating testdata/accepted.yaml = %d, stderr = %s", code, stderr.String())
	}

	// The fixture must reach both files verbatim, not be silently dropped.
	typescript := readFile(t, tsPath)
	python := readFile(t, pythonPath)
	for _, want := range []string{
		"export interface interface {",
		"export interface implements {",
		"export interface static {",
		"export type module = string;",
		"export type Map = Record<string, string>;",
		"  delete?: string;",
		"  new?: string;",
		"  function?: string;",
	} {
		if !strings.Contains(typescript, want) {
			t.Errorf("generated TypeScript is missing %q", want)
		}
	}
	for _, want := range []string{
		"class interface(TypedDict):",
		"class implements(TypedDict):",
		"module = str",
	} {
		if !strings.Contains(python, want) {
			t.Errorf("generated Python is missing %q", want)
		}
	}

	writeFile(t, filepath.Join(dir, "tsconfig.json"), `{
  "compilerOptions": {
    "target": "ES2022",
    "lib": ["ES2022"],
    "module": "nodenext",
    "moduleResolution": "nodenext",
    "strict": true,
    "noEmit": true,
    "types": []
  },
  "include": ["*.d.ts"]
}
`)
	if out, err := exec.Command(tsc, "--project", dir).CombinedOutput(); err != nil {
		t.Fatalf("tsc refused the generated declarations (%v):\n%s", err, out)
	}
}
