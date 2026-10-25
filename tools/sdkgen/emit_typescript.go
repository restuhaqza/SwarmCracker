package main

import (
	"strings"
)

// GenerateTypeScript renders the TypeScript model file.
func GenerateTypeScript(spec *Spec) ([]byte, error) {
	interfaces := make([]string, 0, len(spec.Schemas))
	aliases := make([]string, 0, len(spec.Schemas))
	for _, schema := range spec.Schemas {
		if isClass(schema.Type) {
			interfaces = append(interfaces, tsInterface(schema))
			continue
		}
		aliases = append(aliases, tsAlias(schema))
	}

	var body strings.Builder
	body.WriteString("// " + generatedHeader + "\n")
	if text := joinDeclarations(interfaces, aliases); text != "" {
		body.WriteString("\n")
		body.WriteString(text)
		body.WriteString("\n")
	}
	return []byte(body.String()), nil
}

// tsInterface renders one interface.
func tsInterface(schema *Schema) string {
	resolved := schema.Type
	var out strings.Builder
	if resolved.HasExtra {
		out.WriteString("// additionalProperties is not modelled here: " +
			"an interface cannot express a typed index signature.\n")
	}
	if resolved.Description != "" {
		out.WriteString("/** " + tsDoc(resolved.Description) + " */\n")
	}
	if len(resolved.RequiresOneOf) > 0 {
		out.WriteString("// oneOf: " + oneOfNote(resolved) + "\n")
	}
	out.WriteString("export interface " + schema.Name + " {\n")
	for _, property := range resolved.Properties {
		if note := tsPropertyNote(property.Type); note != "" {
			out.WriteString("  " + note + "\n")
		}
		optional := ""
		if !resolved.IsRequired(property.Name) {
			optional = "?"
		}
		out.WriteString("  " + property.Name + optional + ": " + tsType(property.Type) + ";\n")
	}
	out.WriteString("}\n")
	return strings.TrimSuffix(out.String(), "\n")
}

// tsAlias renders one type alias.
func tsAlias(schema *Schema) string {
	var out strings.Builder
	if schema.Type.Description != "" {
		out.WriteString("/** " + tsDoc(schema.Type.Description) + " */\n")
	}
	if schema.Type.Format == "byte" {
		out.WriteString("/** base64 */\n")
	}
	out.WriteString("export type " + schema.Name + " = " + tsType(schema.Type) + ";")
	return strings.TrimSuffix(out.String(), "\n")
}

// tsPropertyNote renders the doc comment of one property, if it has one.
func tsPropertyNote(t *Type) string {
	note := ""
	if t.Format == "byte" {
		note = "base64"
	}
	if t.Description != "" {
		if note != "" {
			note += ": "
		}
		note += tsDoc(t.Description)
	}
	if note == "" {
		return ""
	}
	return "/** " + note + " */"
}

// tsType renders a type expression.
func tsType(t *Type) string {
	expression := tsBaseType(t)
	if t.Nullable {
		return expression + " | null"
	}
	return expression
}

// tsBaseType renders a type expression without its nullable arm.
func tsBaseType(t *Type) string {
	switch t.Kind {
	case KindString:
		return "string"
	case KindInteger, KindNumber:
		return "number"
	case KindBoolean:
		return "boolean"
	case KindNull:
		return "null"
	case KindRef:
		return t.Ref
	case KindEnum:
		return tsLiterals(t.Enum)
	case KindArray:
		element := tsType(t.Items)
		if strings.Contains(element, " | ") {
			element = "(" + element + ")"
		}
		return element + "[]"
	case KindUnion:
		variants := make([]string, 0, len(t.Variants))
		for _, variant := range t.Variants {
			variants = append(variants, tsType(variant))
		}
		return strings.Join(variants, " | ")
	case KindObject:
		if t.HasExtra && t.Extra != nil {
			return "Record<string, " + tsType(t.Extra) + ">"
		}
		return "Record<string, unknown>"
	default:
		return "unknown"
	}
}

// tsLiterals renders the literal union of an enum.
func tsLiterals(literals []Literal) string {
	values := make([]string, 0, len(literals))
	for _, literal := range literals {
		values = append(values, tsLiteral(literal))
	}
	return strings.Join(values, " | ")
}

// tsLiteral renders one enum value.
func tsLiteral(literal Literal) string {
	if literal.IsString {
		return quoteTS(literal.Raw)
	}
	return literal.Raw
}

// quoteTS double-quotes a string literal. The values are schema names and enum
// members, so only a quote or a backslash can need escaping.
func quoteTS(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + replacer.Replace(value) + `"`
}

// tsDoc folds a description into one line safe inside /** */.
func tsDoc(description string) string {
	folded := oneLine(description)
	return strings.ReplaceAll(folded, "*/", `*\/`)
}
