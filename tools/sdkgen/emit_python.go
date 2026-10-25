package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// GeneratePython renders the Python model file.
func GeneratePython(spec *Spec) ([]byte, error) {
	classes := make([]*Schema, 0, len(spec.Schemas))
	aliases := make([]*Schema, 0, len(spec.Schemas))
	for _, schema := range spec.Schemas {
		if isClass(schema.Type) {
			classes = append(classes, schema)
			continue
		}
		aliases = append(aliases, schema)
	}
	ordered, err := orderAliases(aliases)
	if err != nil {
		return nil, err
	}

	classDecls := make([]string, 0, len(classes))
	for _, schema := range classes {
		classDecls = append(classDecls, pyClass(schema))
	}
	aliasDecls := make([]string, 0, len(ordered))
	for _, schema := range ordered {
		aliasDecls = append(aliasDecls, pyAlias(schema))
	}

	var body strings.Builder
	body.WriteString("# " + generatedHeader + "\n")
	body.WriteString("from __future__ import annotations\n\n")
	body.WriteString("from typing import Any, Literal, NotRequired, TypedDict, Union\n")
	if text := joinDeclarations(classDecls, aliasDecls); text != "" {
		body.WriteString("\n")
		body.WriteString(text)
		body.WriteString("\n")
	}
	return []byte(body.String()), nil
}

// joinDeclarations renders the multi-line declarations (classes, interfaces)
// separated by one blank line, then the one-line declarations (aliases)
// separated by one newline, with one blank line between the two groups.
func joinDeclarations(blocks, lines []string) string {
	parts := make([]string, 0, 2)
	if len(blocks) > 0 {
		parts = append(parts, strings.Join(blocks, "\n\n"))
	}
	if len(lines) > 0 {
		parts = append(parts, strings.Join(lines, "\n"))
	}
	return strings.Join(parts, "\n\n")
}

// isClass reports whether a schema becomes a TypedDict. An object without named
// properties is a map and becomes a type alias instead.
func isClass(t *Type) bool {
	return t.Kind == KindObject && len(t.Properties) > 0
}

// pyClass renders one TypedDict.
func pyClass(schema *Schema) string {
	resolved := schema.Type
	var out strings.Builder
	if resolved.HasExtra {
		out.WriteString("# additionalProperties is not modelled here: " +
			"TypedDict cannot express extra keys.\n")
	}
	if len(resolved.RequiresOneOf) > 0 {
		out.WriteString("# oneOf: " + oneOfNote(resolved) + "\n")
	}
	out.WriteString("class " + schema.Name + "(TypedDict):\n")
	if resolved.Description != "" {
		out.WriteString(`    """` + pyDoc(resolved.Description) + `"""` + "\n")
	}
	for _, property := range resolved.Properties {
		annotation := pyType(property.Type)
		if !resolved.IsRequired(property.Name) {
			annotation = "NotRequired[" + annotation + "]"
		}
		out.WriteString("    " + property.Name + ": " + annotation)
		if property.Type.Format == "byte" {
			out.WriteString("  # base64")
		}
		out.WriteString("\n")
	}
	return strings.TrimSuffix(out.String(), "\n")
}

// pyAlias renders one module-level type alias.
func pyAlias(schema *Schema) string {
	var out strings.Builder
	if schema.Type.Description != "" {
		out.WriteString("# " + pyDoc(schema.Type.Description) + "\n")
	}
	out.WriteString(schema.Name + " = " + pyType(schema.Type))
	if schema.Type.Format == "byte" {
		out.WriteString("  # base64")
	}
	return strings.TrimSuffix(out.String(), "\n")
}

// pyType renders a type expression.
func pyType(t *Type) string {
	expression := pyBaseType(t)
	if t.Nullable {
		return "Union[" + expression + ", None]"
	}
	return expression
}

// pyBaseType renders a type expression without its nullable wrapper.
func pyBaseType(t *Type) string {
	switch t.Kind {
	case KindString:
		return "str"
	case KindInteger:
		return "int"
	case KindNumber:
		return "float"
	case KindBoolean:
		return "bool"
	case KindNull:
		return "None"
	case KindRef:
		return t.Ref
	case KindEnum:
		return pyLiterals(t.Enum)
	case KindArray:
		return "list[" + pyType(t.Items) + "]"
	case KindUnion:
		variants := make([]string, 0, len(t.Variants))
		for _, variant := range t.Variants {
			variants = append(variants, pyType(variant))
		}
		return "Union[" + strings.Join(variants, ", ") + "]"
	case KindObject:
		if t.HasExtra && t.Extra != nil {
			return "dict[str, " + pyType(t.Extra) + "]"
		}
		return "dict[str, Any]"
	default:
		return "Any"
	}
}

// pyLiterals renders a Literal[...] expression.
func pyLiterals(literals []Literal) string {
	values := make([]string, 0, len(literals))
	for _, literal := range literals {
		values = append(values, pyLiteral(literal))
	}
	return "Literal[" + strings.Join(values, ", ") + "]"
}

// pyLiteral renders one enum value.
func pyLiteral(literal Literal) string {
	switch {
	case literal.IsString:
		return strconv.Quote(literal.Raw)
	case literal.IsBool:
		if literal.Raw == "true" {
			return "True"
		}
		return "False"
	default:
		return literal.Raw
	}
}

// pyDoc folds a description into one line safe to place inside """. A YAML
// block scalar keeps paragraph breaks, which would end the docstring line.
func pyDoc(description string) string {
	folded := oneLine(description)
	folded = strings.ReplaceAll(folded, `\`, `\\`)
	folded = strings.ReplaceAll(folded, `"""`, `\"\"\"`)
	// A trailing quote would run into the closing delimiter.
	if strings.HasSuffix(folded, `"`) && !strings.HasSuffix(folded, `\"`) {
		folded = strings.TrimSuffix(folded, `"`) + `\"`
	}
	return folded
}

// oneLine collapses every run of whitespace into a single space.
func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// oneOfNote renders the unmodelled oneOf constraint as one line.
func oneOfNote(t *Type) string {
	alternatives := make([]string, 0, len(t.RequiresOneOf))
	for _, required := range t.RequiresOneOf {
		alternatives = append(alternatives, "["+strings.Join(required, ", ")+"]")
	}
	return "one of these required sets must hold: " + strings.Join(alternatives, ", ") + "."
}

// orderAliases sorts aliases so a name is defined before the alias that uses it.
// Class annotations are postponed by `from __future__ import annotations`, but an
// alias assignment is evaluated at import time and needs its dependencies first.
func orderAliases(aliases []*Schema) ([]*Schema, error) {
	byName := make(map[string]*Schema, len(aliases))
	for _, alias := range aliases {
		byName[alias.Name] = alias
	}

	visited := make(map[string]bool, len(aliases))
	visiting := make(map[string]bool, len(aliases))
	ordered := make([]*Schema, 0, len(aliases))

	var visit func(schema *Schema) error
	visit = func(schema *Schema) error {
		if visited[schema.Name] {
			return nil
		}
		if visiting[schema.Name] {
			return fmt.Errorf("alias cycle at schema %q", schema.Name)
		}
		visiting[schema.Name] = true
		for _, ref := range referencedSchemas(schema.Type) {
			dependency, ok := byName[ref]
			if !ok {
				continue
			}
			if err := visit(dependency); err != nil {
				return err
			}
		}
		delete(visiting, schema.Name)
		visited[schema.Name] = true
		ordered = append(ordered, schema)
		return nil
	}

	for _, alias := range aliases {
		if err := visit(alias); err != nil {
			return nil, err
		}
	}
	return ordered, nil
}

// referencedSchemas returns the sorted schema names a type points at.
func referencedSchemas(t *Type) []string {
	seen := map[string]bool{}
	var walk func(t *Type)
	walk = func(t *Type) {
		if t == nil {
			return
		}
		if t.Kind == KindRef && t.Ref != "" {
			seen[t.Ref] = true
		}
		for _, property := range t.Properties {
			walk(property.Type)
		}
		walk(t.Items)
		walk(t.Extra)
		for _, variant := range t.Variants {
			walk(variant)
		}
	}
	walk(t)

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
