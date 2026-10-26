package main

import "regexp"

// Identifier strategy: verbatim or error, never rewrite.
//
// Every `components.schemas` name and every `properties` key is emitted verbatim
// as a Python TypedDict field, an interface/alias name and a TypeScript interface
// member. Both languages accept an unquoted member only when its name is a plain
// ASCII identifier, Python additionally rejects its keywords as field names, and
// a *declaration* name is narrower than a member name in TypeScript: `delete` is
// a legal member but not a legal interface name.
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
// Three positions are checked separately, because the languages differ:
//
//   - A **property** name must be a portable identifier and not a Python keyword.
//     TypeScript does not need any blocklist here: a reserved word is a legal
//     interface *member* name (`delete: string;` type-checks), and `delete` is not
//     a Python keyword, so the emitted pair is valid in both languages.
//   - A **schema** name is a declaration name, so it must also not be a word tsc
//     refuses there (see typescriptKeywordBlocklist and
//     typescriptTypeNameBlocklist) nor the lib type the emitted type expressions
//     rely on (Record).
//   - A **schema** name is also a *module-level* binding, so it must not shadow a
//     name the generated Python module imports or a builtin its annotations use:
//     `class Any(TypedDict)` makes every later `dict[str, Any]` mean the new
//     class, and the module stays importable while being silently wrong.

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

// typescriptKeywordBlocklist are the ECMAScript/TypeScript keywords that cannot
// appear as a declaration name at all: tsc 7.0.2 stops at the parse and reports
// TS1128("Declaration or statement expected") and friends. Verified against the
// pinned compiler by TestPinnedTSCRefusesEveryBlocklistedDeclarationName.
//
// Words that are only reserved in strict-mode *binding* positions are
// deliberately absent: tsc accepts `export interface implements {}` and
// `export type let = string`, and the emitters only ever produce `interface` and
// `type` declarations, never `class`, `let` or `const`. `implements`, `let`,
// `package`, `private`, `protected`, `public`, `static` and `interface` are
// therefore legal schema names, and testdata/accepted.yaml proves it.
var typescriptKeywordBlocklist = []string{
	"break", "case", "catch", "class", "const", "continue", "debugger",
	"default", "delete", "do", "else", "enum", "export", "extends", "false",
	"finally", "for", "function", "if", "import", "in", "instanceof", "new",
	"null", "return", "super", "switch", "this", "throw", "true", "try",
	"typeof", "var", "void", "while", "with",
}

// typescriptTypeNameBlocklist are the words tsc parses but refuses *by name*:
// TS2427 `Interface name cannot be 'string'` and the matching TS2457 for a type
// alias. They pass the identifier pattern, so nothing else would catch them.
var typescriptTypeNameBlocklist = []string{
	"any", "bigint", "boolean", "never", "number", "object", "string",
	"symbol", "undefined", "unknown",
}

// typescriptAliasOnlyBlocklist are words that are legal as an interface name but
// not as a type alias name: `export interface as {}` parses, while
// `export type as = string;` is TS1005. A schema can be emitted in either form,
// so they are refused. (`as` is also a Python keyword, so the identifier rule
// already refuses it today; the list keeps the TypeScript rule complete on its
// own instead of depending on that coincidence.)
var typescriptAliasOnlyBlocklist = []string{"as"}

// typescriptModuleVocabulary is the TypeScript lib type the emitted declarations
// use. A schema named Record shadows it: the module then reports TS2315
// "Type 'Record' is not generic" on every map type, so the generated file does
// not compile.
var typescriptModuleVocabulary = map[string]bool{"Record": true}

// nameSet flattens name lists into the lookup maps the checks use.
func nameSet(groups ...[]string) map[string]bool {
	set := make(map[string]bool)
	for _, group := range groups {
		for _, name := range group {
			set[name] = true
		}
	}
	return set
}

// typescriptDeclarationBlocklist is every word scanned above that cannot name an
// interface or a type alias.
var typescriptDeclarationBlocklist = nameSet(
	typescriptKeywordBlocklist,
	typescriptTypeNameBlocklist,
	typescriptAliasOnlyBlocklist,
)

// The generated Python module binds these names at module level, so a schema
// with one of them would shadow them. The lists themselves live next to the
// emitter (emit_python.go) so the import line and the check cannot drift apart.
var (
	pythonTypingNames  = nameSet(pyTypingImportNames)
	pythonFutureNames  = nameSet(pyFutureImportNames)
	pythonBuiltinNames = nameSet(pyBuiltinTypeNames)
)

// identifierProblem explains why name cannot be emitted verbatim as a property
// name, or returns "" when it can. It keeps isPortableIdentifier and the error
// message on one rule, so a name can never pass one check and fail another.
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

// schemaNameProblem explains why name cannot declare a schema in both generated
// modules, or returns "" when it can. It is identifierProblem plus the
// declaration-position and module-namespace rules that only apply to a schema.
func schemaNameProblem(name string) string {
	if problem := identifierProblem(name); problem != "" {
		return problem
	}
	switch {
	case typescriptDeclarationBlocklist[name]:
		return "is a TypeScript keyword or type name, which tsc refuses as an " +
			"interface name and as a type alias name"
	case typescriptModuleVocabulary[name]:
		return "is the TypeScript lib type Record, which the generated module uses " +
			"for map types, so the declaration would shadow it and tsc would " +
			"reject the module (TS2315)"
	case pythonTypingNames[name]:
		return "shadows a name the generated Python module imports from typing"
	case pythonFutureNames[name]:
		return "shadows a name the generated Python module imports from __future__"
	case pythonBuiltinNames[name]:
		return "shadows a Python builtin the generated module uses in its annotations"
	}
	return ""
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
// class, a Python type alias and a TypeScript interface or type alias in the
// generated modules.
func (p *parser) checkSchemaName(name, jsonPath string) error {
	if problem := schemaNameProblem(name); problem != "" {
		return p.errf(jsonPath, "schema name %q %s: tools/sdkgen emits names "+
			"verbatim, so this name cannot be generated; rename it in "+
			"components.schemas", name, problem)
	}
	return nil
}
