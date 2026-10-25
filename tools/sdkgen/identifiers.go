package main

import "regexp"

// Identifier strategy: verbatim or error, never rewrite.
//
// Every `components.schemas` name and every `properties` key is emitted verbatim
// as a Python TypedDict field, an interface/alias name and a TypeScript interface
// member. Both languages accept an unquoted member only when its name is a plain
// ASCII identifier, Python additionally rejects its keywords as field names, and
// TypeScript additionally rejects its reserved words as declaration names.
//
// The generator therefore *rejects* a name it cannot emit verbatim and fails the
// run with an error naming the offending schema name or property; it never
// renames, quotes or escapes a name. Rewriting was rejected deliberately:
//
//   - A rename changes the public surface of the SDK relative to the normative
//     `api/openapi.yaml`: the wire name and the declared name would differ, and
//     `make sdk-check` would then prove reproducibility of something the spec
//     does not say.
//   - Quoting cannot be one strategy: a quoted key is a syntax error in a Python
//     TypedDict (`"x-y": str`), while TypeScript would accept `"x-y": string` but
//     then callers could not use dot access. One rule cannot serve both.
//   - A documented error is caught by `make sdk-check` in CI on the pull request
//     that introduces the name, with a message that says what to change.
//
// Two positions are checked separately, because the two languages differ:
//
//   - A **property** name must be a portable identifier and not a Python keyword.
//     TypeScript does not need its reserved list here: a reserved word is a legal
//     interface *member* name (`delete: string;` is valid), and `delete` is not a
//     Python keyword, so the emitted pair is valid in both languages.
//   - A **schema** name is a declaration name, so it must also not be a
//     TypeScript reserved word (`export interface delete {}` is a syntax error).

// identifierPattern is the portable intersection: an ASCII letter or underscore
// followed by letters, digits or underscores. That is a valid Python identifier
// (so a TypedDict field, a class name and a keyword-argument name in the client)
// and a valid TypeScript identifier (so an interface member and a type name). A
// schema that satisfies it renders as valid code in both languages.
var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// pythonKeywords is keyword.kwlist for Python 3 (3.11 through 3.15; the list has
// not changed since 3.7). A keyword passes the pattern above but is a syntax
// error as a TypedDict field name and as a class name.
var pythonKeywords = map[string]bool{
	"False": true, "None": true, "True": true,
	"and": true, "as": true, "assert": true, "async": true, "await": true,
	"break": true, "class": true, "continue": true, "def": true, "del": true,
	"elif": true, "else": true, "except": true, "finally": true, "for": true,
	"from": true, "global": true, "if": true, "import": true, "in": true,
	"is": true, "lambda": true, "nonlocal": true, "not": true, "or": true,
	"pass": true, "raise": true, "return": true, "try": true, "while": true,
	"with": true, "yield": true,
}

// typescriptReservedWords are the ECMAScript/TypeScript words that cannot name an
// interface, class or type alias. Contextual words (`type`, `of`, `get`, `any`,
// `string`, ...) are deliberately absent: they are legal type names, and the
// normative spec already uses two of them as property names.
var typescriptReservedWords = map[string]bool{
	"break": true, "case": true, "catch": true, "class": true, "const": true,
	"continue": true, "debugger": true, "default": true, "delete": true,
	"do": true, "else": true, "enum": true, "export": true, "extends": true,
	"false": true, "finally": true, "for": true, "function": true, "if": true,
	"import": true, "in": true, "instanceof": true, "new": true, "null": true,
	"return": true, "super": true, "switch": true, "this": true, "throw": true,
	"true": true, "try": true, "typeof": true, "var": true, "void": true,
	"while": true, "with": true,
	// Reserved in strict mode, which every ES module is.
	"implements": true, "interface": true, "let": true, "package": true,
	"private": true, "protected": true, "public": true, "static": true,
	"yield": true, "await": true,
}

// identifierProblem explains why name cannot be emitted verbatim, or returns ""
// when it can. It keeps isPortableIdentifier and the two error messages on one
// rule, so a name can never pass one check and fail another.
func identifierProblem(name string) string {
	if !identifierPattern.MatchString(name) {
		return "is not a portable identifier"
	}
	if pythonKeywords[name] {
		return "is a Python keyword"
	}
	return ""
}

// isPortableIdentifier reports whether name can be emitted verbatim as a
// property name in both generated languages.
func isPortableIdentifier(name string) bool {
	return identifierProblem(name) == ""
}

// checkPropertyName rejects a `properties` key that cannot be a Python TypedDict
// field and a TypeScript interface member at the same time.
func (p *parser) checkPropertyName(name, jsonPath string) error {
	if problem := identifierProblem(name); problem != "" {
		return p.errf(jsonPath, "property %q %s: tools/sdkgen emits names "+
			"verbatim, and a property must match ^[A-Za-z_][A-Za-z0-9_]*$ and "+
			"must not be a Python keyword to be valid in both Python and "+
			"TypeScript; rename it in the spec", name, problem)
	}
	return nil
}

// checkSchemaName rejects a component name that cannot be a Python TypedDict
// class, a Python type alias and a TypeScript interface or type alias.
func (p *parser) checkSchemaName(name, jsonPath string) error {
	if problem := identifierProblem(name); problem != "" {
		return p.errf(jsonPath, "schema name %q %s: tools/sdkgen emits names "+
			"verbatim, and a schema name must match ^[A-Za-z_][A-Za-z0-9_]*$ and "+
			"must not be a Python keyword to be valid in both Python and "+
			"TypeScript; rename it in the spec", name, problem)
	}
	if typescriptReservedWords[name] {
		return p.errf(jsonPath, "schema name %q is a TypeScript reserved word: "+
			"it cannot name an interface or type alias, and tools/sdkgen emits "+
			"names verbatim; rename it in the spec", name)
	}
	return nil
}
