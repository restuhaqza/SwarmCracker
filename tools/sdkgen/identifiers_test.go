package main

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestIsPortableIdentifier pins the documented *property* rule the emitters rely
// on: an ASCII letter or underscore followed by letters, digits or underscores,
// and not a Python keyword.
func TestIsPortableIdentifier(t *testing.T) {
	valid := []string{"id", "updatedAt", "_private", "x1", "A", "schema_version", "bx2"}
	for _, name := range valid {
		if !isPortableIdentifier(name) {
			t.Errorf("isPortableIdentifier(%q) = false, want true", name)
		}
	}
	invalid := []string{
		"",           // empty
		"x-y",        // hyphen: invalid in both languages unquoted
		"1abc",       // starts with a digit
		"with space", // whitespace
		"a.b",        // dot
		"naïve",      // non-ASCII
		"class",      // Python keyword
		"from",       // Python keyword
		"None",       // Python keyword (a constant)
		"x;y",        // statement separator
		"x\ny",       // newline injection
		"x\"y",       // quote
	}
	for _, name := range invalid {
		if isPortableIdentifier(name) {
			t.Errorf("isPortableIdentifier(%q) = true, want false", name)
		}
	}
}

// TestParseRejectsNonIdentifierPropertyName is the item-2 guard: the generator
// fails with an error naming the property instead of emitting invalid code.
func TestParseRejectsNonIdentifierPropertyName(t *testing.T) {
	cases := []struct {
		name     string
		property string
		reason   string
	}{
		{name: "hyphen", property: "x-y", reason: "is not a portable identifier"},
		{name: "leading digit", property: "1abc", reason: "is not a portable identifier"},
		{name: "space", property: "with space", reason: "is not a portable identifier"},
		{name: "python keyword", property: "class", reason: "is a Python keyword"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := specWith("    Thing:\n      type: object\n      properties:\n        " +
				quoteYAMLKey(tc.property) + ": {type: string}\n")
			_, err := Parse([]byte(src), "test.yaml")
			if err == nil {
				t.Fatalf("Parse() accepted property %q", tc.property)
			}
			message := err.Error()
			if !strings.Contains(message, `"`+tc.property+`"`) {
				t.Errorf("error does not quote the property %q: %s", tc.property, message)
			}
			if !strings.Contains(message, "#/components/schemas/Thing/properties/") {
				t.Errorf("error does not carry the JSON path: %s", message)
			}
			if !strings.Contains(message, tc.reason) {
				t.Errorf("error does not explain the rule (%q): %s", tc.reason, message)
			}
		})
	}
}

// TestParseRejectsHostileSchemaName covers the declaration-position half of the
// rule: a component name becomes a class, interface or alias name in both
// languages, so it is held to the property rule plus the names that are legal
// only as *members* (`delete`), the names tsc refuses as a declaration
// (`string`), the lib type the emitter relies on (`Record`) and every name the
// generated Python module binds at module level (`Any`, `dict`, `annotations`).
func TestParseRejectsHostileSchemaName(t *testing.T) {
	cases := []struct {
		name   string
		schema string
		reason string
	}{
		{name: "hyphen", schema: "Bad-Name", reason: "is not a portable identifier"},
		{name: "python keyword", schema: "class", reason: "is a Python keyword"},
		{name: "typescript keyword", schema: "delete", reason: "TypeScript keyword or type name"},
		{name: "typescript keyword new", schema: "new", reason: "TypeScript keyword or type name"},
		{name: "typescript type name", schema: "string", reason: "TypeScript keyword or type name"},
		{name: "typescript type name undefined", schema: "undefined", reason: "TypeScript keyword or type name"},
		{name: "typescript type name object", schema: "object", reason: "TypeScript keyword or type name"},
		{name: "typescript alias-only word", schema: "as", reason: "is a Python keyword"},
		{name: "typescript lib type", schema: "Record", reason: "TypeScript lib type Record"},
		{name: "python typing import", schema: "Any", reason: "imports from typing"},
		{name: "python typing TypedDict", schema: "TypedDict", reason: "imports from typing"},
		{name: "python future import", schema: "annotations", reason: "imports from __future__"},
		{name: "python builtin", schema: "dict", reason: "Python builtin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := specWith(schemaSpec(tc.schema))
			_, err := Parse([]byte(src), "test.yaml")
			if err == nil {
				t.Fatalf("Parse() accepted schema name %q", tc.schema)
			}
			if !strings.Contains(err.Error(), `"`+tc.schema+`"`) {
				t.Errorf("error does not quote the schema name: %s", err)
			}
			if !strings.Contains(err.Error(), "schema name") {
				t.Errorf("error does not say it is a schema name: %s", err)
			}
			if !strings.Contains(err.Error(), tc.reason) {
				t.Errorf("error does not explain the rule (%q): %s", tc.reason, err)
			}
		})
	}
}

// TestSchemaNameProblemRefusesEveryBlocklistedName keeps the checks and the
// lists from drifting apart: every name in every blocklist must be refused, and
// a list emptied by accident must not leave the test vacuous.
func TestSchemaNameProblemRefusesEveryBlocklistedName(t *testing.T) {
	groups := map[string]map[string]bool{
		"typescript declaration": typescriptDeclarationBlocklist,
		"typescript module":      typescriptModuleVocabulary,
		"python typing":          pythonTypingNames,
		"python future":          pythonFutureNames,
		"python builtin":         pythonBuiltinNames,
	}
	order := []string{"typescript declaration", "typescript module", "python typing", "python future", "python builtin"}
	for _, group := range order {
		names := groups[group]
		if len(names) == 0 {
			t.Fatalf("%s blocklist is empty", group)
		}
		for name := range names {
			if problem := schemaNameProblem(name); problem == "" {
				t.Errorf("schemaNameProblem(%q) = \"\", want a refusal (%s blocklist)", name, group)
			}
		}
	}
	if len(typescriptKeywordBlocklist) < 30 || len(typescriptTypeNameBlocklist) < 10 {
		t.Errorf("blocklists shrank unexpectedly: %d keywords, %d type names",
			len(typescriptKeywordBlocklist), len(typescriptTypeNameBlocklist))
	}
}

// TestSchemaNameProblemAcceptsBorderlineNames guards the other direction: words
// that look reserved but are legal declaration names in both languages must
// still be accepted, or the rule becomes a gratuitous restriction. The same set
// is generated into testdata/accepted.yaml and compiled by the pinned tsc in
// TestAcceptedFixtureTypeChecksWithPinnedTSC.
func TestSchemaNameProblemAcceptsBorderlineNames(t *testing.T) {
	accepted := []string{
		// Reserved only in strict-mode binding positions, which the emitters
		// never produce: tsc accepts every one of these as an interface and as a
		// type alias name.
		"interface", "implements", "let", "package", "private", "protected",
		"public", "static",
		// Contextual TypeScript words.
		"get", "set", "type", "of", "readonly", "keyof", "infer", "declare",
		"module", "namespace", "unique", "intrinsic", "out", "constructor",
		// Lib and global constructor names the generated module never uses.
		"Object", "String", "Number", "Promise", "Date", "JSON", "Error", "Function",
		// Ordinary forms.
		"schema_version", "_private", "x1", "camelCase",
	}
	for _, name := range accepted {
		if problem := schemaNameProblem(name); problem != "" {
			t.Errorf("schemaNameProblem(%q) = %q, want accepted", name, problem)
		}
	}
}

// TestParseAcceptsTypeScriptReservedWordsAsProperties documents the deliberate
// asymmetry: a reserved word is a legal interface member, so it is only refused
// when the name is in declaration position.
func TestParseAcceptsTypeScriptReservedWordsAsProperties(t *testing.T) {
	src := specWith("    Thing:\n      type: object\n      properties:\n" +
		"        delete: {type: string}\n        default: {type: string}\n" +
		"        function: {type: string}\n        new: {type: string}\n")
	spec := mustParse(t, src)

	typescript, err := GenerateTypeScript(spec)
	if err != nil {
		t.Fatalf("GenerateTypeScript() error = %v", err)
	}
	for _, member := range []string{
		"  delete?: string;",
		"  default?: string;",
		"  function?: string;",
		"  new?: string;",
	} {
		if !strings.Contains(string(typescript), member) {
			t.Errorf("TypeScript output is missing %q\n%s", member, typescript)
		}
	}
}

// TestParseAcceptsEveryIdentifierFormTheEmittersCanRender checks the rule is not
// so strict that ordinary names are refused, and that a name it accepts really
// reaches both generated files verbatim.
func TestParseAcceptsEveryIdentifierFormTheEmittersCanRender(t *testing.T) {
	src := specWith("    Thing:\n      type: object\n      required: [_private]\n" +
		"      properties:\n        _private: {type: string}\n        camelCase: {type: integer}\n" +
		"        snake_case: {type: boolean}\n        x1: {type: string}\n")
	spec := mustParse(t, src)

	python, err := GeneratePython(spec)
	if err != nil {
		t.Fatalf("GeneratePython() error = %v", err)
	}
	typescript, err := GenerateTypeScript(spec)
	if err != nil {
		t.Fatalf("GenerateTypeScript() error = %v", err)
	}
	for _, name := range []string{"_private", "camelCase", "snake_case", "x1"} {
		if !strings.Contains(string(python), "\n    "+name+": ") {
			t.Errorf("Python output is missing field %q\n%s", name, python)
		}
		if !strings.Contains(string(typescript), "\n  "+name+": ") &&
			!strings.Contains(string(typescript), "\n  "+name+"?: ") {
			t.Errorf("TypeScript output is missing member %q\n%s", name, typescript)
		}
	}
}

// TestEmittedPythonImportsCannotBeShadowed derives the rule from the real
// output instead of from the lists: whatever name the generated module imports,
// a schema may not declare it. A hardcoded import added to the emitter therefore
// fails here until the blocklist covers it.
func TestEmittedPythonImportsCannotBeShadowed(t *testing.T) {
	python, err := GeneratePython(mustParse(t, smallSpec))
	if err != nil {
		t.Fatalf("GeneratePython() error = %v", err)
	}
	imported := 0
	for _, line := range strings.Split(string(python), "\n") {
		module, names, ok := strings.Cut(line, " import ")
		if !ok || !strings.HasPrefix(module, "from ") {
			continue
		}
		for _, name := range strings.Split(names, ", ") {
			imported++
			if problem := schemaNameProblem(name); problem == "" {
				t.Errorf("the generated module imports %q, but a schema with that "+
					"name is accepted and would shadow the import", name)
			}
		}
	}
	if imported < 6 {
		t.Fatalf("found only %d imported names in the generated module; the check would be vacuous", imported)
	}
}

// TestRunFailsOnNonIdentifierProperty covers the CLI contract: exit 1, the
// property is named on stderr, and no output file is written, so a stale-by-rule
// spec can never leave an invalid model behind.
func TestRunFailsOnNonIdentifierProperty(t *testing.T) {
	in := newGenerateIn(t)
	writeFile(t, in.spec, specWith("    Thing:\n      type: object\n      properties:\n        x-y: {type: string}\n"))

	var stdout, stderr bytes.Buffer
	if code := run(in.args(), &stdout, &stderr); code != 1 {
		t.Fatalf("run() = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "x-y") {
		t.Errorf("stderr does not name the property: %s", stderr.String())
	}
	for _, path := range []string{in.pythonPath, in.tsPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("run() wrote %s for a spec it refused", path)
		}
	}
}

// TestHostileFixtureIsRejectedByTheGenerator is the end-to-end reproduction of
// the finding this file exists for: testdata/hostile.yaml holds names that are
// legal OpenAPI names and legal identifiers, so the earlier rule accepted them
// all, and the generated modules did not compile (or, worse, compiled against a
// shadowed import). The document must now be refused, name by name, before a
// single byte is written.
func TestHostileFixtureIsRejectedByTheGenerator(t *testing.T) {
	names := fixtureSchemaNames(t, "testdata/hostile.yaml")
	if len(names) < 20 {
		t.Fatalf("hostile fixture only declares %d schemas; coverage shrank: %v", len(names), names)
	}
	for _, name := range names {
		if problem := schemaNameProblem(name); problem == "" {
			t.Errorf("schemaNameProblem(%q) = \"\", want a refusal", name)
		}
	}

	in := newGenerateIn(t)
	writeFile(t, in.spec, readFile(t, "testdata/hostile.yaml"))
	var stdout, stderr bytes.Buffer
	if code := run(in.args(), &stdout, &stderr); code != 1 {
		t.Fatalf("run() = %d, want 1 (stderr: %s)", code, stderr.String())
	}
	message := stderr.String()
	if !strings.Contains(message, "#/components/schemas/") {
		t.Errorf("stderr does not carry the JSON path: %s", message)
	}
	if !strings.Contains(message, "schema name") {
		t.Errorf("stderr does not explain what was refused: %s", message)
	}
	for _, path := range []string{in.pythonPath, in.tsPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("run() wrote %s for a spec it refused", path)
		}
	}
}

// TestIdentifierValidationRejectsHostileNamesAndAcceptsTheCommittedSpec replaces
// the earlier check that only asserted "the committed spec is clean" by calling
// the helper it was supposed to be testing. It now proves both directions of the
// rule: every hostile name in testdata/hostile.yaml is refused through the real
// parse path (with its JSON path), and the normative api/openapi.yaml is
// accepted name by name.
func TestIdentifierValidationRejectsHostileNamesAndAcceptsTheCommittedSpec(t *testing.T) {
	hostile := fixtureSchemaNames(t, "testdata/hostile.yaml")
	if len(hostile) == 0 {
		t.Fatal("hostile fixture declares no schemas")
	}
	for _, name := range hostile {
		t.Run("rejects/"+name, func(t *testing.T) {
			src := specWith(schemaSpec(name))
			_, err := Parse([]byte(src), "testdata/hostile.yaml")
			if err == nil {
				t.Fatalf("Parse() accepted schema name %q", name)
			}
			if !strings.Contains(err.Error(), `"`+name+`"`) {
				t.Errorf("error does not quote the schema name: %s", err)
			}
			if !strings.Contains(err.Error(), "#/components/schemas/"+name) {
				t.Errorf("error does not carry the JSON path: %s", err)
			}
		})
	}

	src, err := os.ReadFile(realSpecPath)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	spec, err := Parse(src, realSpecPath)
	if err != nil {
		t.Fatalf("Parse(%s) error = %v; the committed spec must stay clean", realSpecPath, err)
	}
	if len(spec.Schemas) < 10 {
		t.Fatalf("committed spec resolved only %d schemas; the check would be vacuous", len(spec.Schemas))
	}
	for _, schema := range spec.Schemas {
		if problem := schemaNameProblem(schema.Name); problem != "" {
			t.Errorf("committed schema %q is refused: %s", schema.Name, problem)
		}
		for _, property := range schema.Type.Properties {
			if !isPortableIdentifier(property.Name) {
				t.Errorf("%s.%s is not a portable identifier", schema.Name, property.Name)
			}
		}
	}
}

// quoteYAMLKey renders a property key for the test specs, double-quoting it when
// a bare key would change meaning (a space, a leading digit, and so on).
func quoteYAMLKey(name string) string {
	if isPortableIdentifier(name) {
		return name
	}
	return `"` + name + `"`
}

// schemaSpec renders one throwaway schema declaration. Fixture names are plain
// ASCII identifiers, so a bare YAML key is unambiguous.
func schemaSpec(name string) string {
	return "    " + name + ":\n      type: object\n      properties:\n        id: {type: string}\n"
}

// fixtureSchemaNames returns the sorted components.schemas names of a fixture.
func fixtureSchemaNames(t *testing.T, path string) []string {
	t.Helper()
	var doc struct {
		Components struct {
			Schemas map[string]any `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal([]byte(readFile(t, filepath.Clean(path))), &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	names := make([]string, 0, len(doc.Components.Schemas))
	for name := range doc.Components.Schemas {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
