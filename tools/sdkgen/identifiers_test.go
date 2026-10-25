package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// TestIsPortableIdentifier pins the documented rule the emitters rely on:
// an ASCII letter or underscore followed by letters, digits or underscores, and
// not a Python keyword.
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

// TestParseRejectsNonIdentifierSchemaName covers the other half of the rule: a
// component name becomes a class, interface or alias name, so it is held to the
// same standard, plus the TypeScript reserved words that cannot be a declaration
// name at all.
func TestParseRejectsNonIdentifierSchemaName(t *testing.T) {
	cases := []struct {
		name   string
		schema string
		reason string
	}{
		{name: "hyphen", schema: "Bad-Name", reason: "is not a portable identifier"},
		{name: "python keyword", schema: "class", reason: "is a Python keyword"},
		{name: "typescript reserved", schema: "interface", reason: "TypeScript reserved word"},
		{name: "typescript reserved delete", schema: "delete", reason: "TypeScript reserved word"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := specWith("    " + tc.schema + ":\n      type: object\n      properties:\n        id: {type: string}\n")
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

// TestParseAcceptsTypeScriptReservedWordsAsProperties documents the deliberate
// asymmetry: a reserved word is a legal interface member, so it is only refused
// when the name is in declaration position.
func TestParseAcceptsTypeScriptReservedWordsAsProperties(t *testing.T) {
	src := specWith("    Thing:\n      type: object\n      properties:\n" +
		"        delete: {type: string}\n        default: {type: string}\n")
	spec := mustParse(t, src)

	typescript, err := GenerateTypeScript(spec)
	if err != nil {
		t.Fatalf("GenerateTypeScript() error = %v", err)
	}
	for _, member := range []string{"  delete?: string;", "  default?: string;"} {
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

// TestCommittedSpecIsIdentifierClean is the reproduction of the whole item: the
// normative spec must never introduce a name the generator would refuse, or
// `make sdk-check` breaks on a spec-only pull request.
func TestCommittedSpecIsIdentifierClean(t *testing.T) {
	src, err := os.ReadFile(realSpecPath)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	spec, err := Parse(src, "api/openapi.yaml")
	if err != nil {
		t.Fatalf("Parse(%s) error = %v", realSpecPath, err)
	}
	for _, schema := range spec.Schemas {
		if !isPortableIdentifier(schema.Name) {
			t.Errorf("schema %q is not a portable identifier", schema.Name)
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
