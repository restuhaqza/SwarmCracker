package main

import (
	"os"
	"strings"
	"testing"
)

// realSpecPath is the normative schema the generated SDK types are built from.
const realSpecPath = "../../api/openapi.yaml"

// specWith wraps schema declarations in a minimal OpenAPI document.
func specWith(schemas string) string {
	return "openapi: 3.1.0\ncomponents:\n  schemas:\n" + schemas
}

func mustParse(t *testing.T, src string) *Spec {
	t.Helper()
	spec, err := Parse([]byte(src), "test.yaml")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	return spec
}

func mustSchema(t *testing.T, spec *Spec, name string) *Type {
	t.Helper()
	schema := spec.Lookup(name)
	if schema == nil {
		t.Fatalf("schema %q not found", name)
	}
	return schema.Type
}

func propertyNames(t *Type) []string {
	names := make([]string, 0, len(t.Properties))
	for _, p := range t.Properties {
		names = append(names, p.Name)
	}
	return names
}

func TestParseRealSpec(t *testing.T) {
	src, err := os.ReadFile(realSpecPath)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	spec, err := Parse(src, "api/openapi.yaml")
	if err != nil {
		t.Fatalf("Parse(%s) error = %v", realSpecPath, err)
	}
	if len(spec.Schemas) < 50 {
		t.Fatalf("parsed %d schemas, want the full component set", len(spec.Schemas))
	}

	// Schemas are sorted by name, which is what makes the output stable.
	for i := 1; i < len(spec.Schemas); i++ {
		if spec.Schemas[i-1].Name >= spec.Schemas[i].Name {
			t.Fatalf("schemas not sorted: %q before %q", spec.Schemas[i-1].Name, spec.Schemas[i].Name)
		}
	}

	t.Run("object properties keep spec order", func(t *testing.T) {
		service := mustSchema(t, spec, "Service")
		if service.Kind != KindObject {
			t.Fatalf("Service kind = %v, want object", service.Kind)
		}
		want := []string{"id", "name", "labels", "revision", "createdAt", "updatedAt", "spec", "status"}
		if got := propertyNames(service); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("Service properties = %v, want %v", got, want)
		}
		for _, name := range []string{"id", "name", "revision", "spec", "status"} {
			if !service.IsRequired(name) {
				t.Errorf("Service.%s should be required", name)
			}
		}
		for _, name := range []string{"labels", "createdAt", "updatedAt"} {
			if service.IsRequired(name) {
				t.Errorf("Service.%s should be optional", name)
			}
		}
		if got := service.Property("labels"); got == nil || got.Kind != KindObject || !got.HasExtra || got.Extra == nil || got.Extra.Kind != KindString {
			t.Errorf("Service.labels = %+v, want map<string,string>", got)
		}
	})

	t.Run("allOf merges the referenced schema", func(t *testing.T) {
		doc := mustSchema(t, spec, "ServiceDocument")
		if doc.Kind != KindObject {
			t.Fatalf("ServiceDocument kind = %v, want object", doc.Kind)
		}
		for _, name := range []string{"schemaVersion", "kind", "dryRun", "warnings"} {
			if doc.Property(name) == nil {
				t.Errorf("ServiceDocument is missing merged property %q", name)
			}
		}
		if doc.Property("spec") == nil {
			t.Error("ServiceDocument lost the inherited spec property")
		}
		if !doc.IsRequired("schemaVersion") || !doc.IsRequired("kind") || doc.IsRequired("dryRun") {
			t.Error("ServiceDocument required set is wrong")
		}
		// A ref inside a merged schema stays a ref.
		if got := doc.Property("spec"); got.Kind != KindRef || got.Ref != "ServiceSpec" {
			t.Errorf("ServiceDocument.spec = %+v, want $ref ServiceSpec", got)
		}
	})

	t.Run("nullable type list", func(t *testing.T) {
		next := mustSchema(t, spec, "ServiceList").Property("next")
		if next == nil || next.Kind != KindString || !next.Nullable {
			t.Fatalf("ServiceList.next = %+v, want nullable string", next)
		}
		if !mustSchema(t, spec, "ServiceList").IsRequired("next") {
			t.Error("ServiceList.next should be required and nullable")
		}
	})

	t.Run("inline allOf property becomes a named type", func(t *testing.T) {
		partial := mustSchema(t, spec, "Partial")
		field := partial.Property("error")
		if field == nil || field.Kind != KindRef || field.Ref != "PartialError" {
			t.Fatalf("Partial.error = %+v, want $ref PartialError", field)
		}
		hoisted := mustSchema(t, spec, "PartialError")
		if hoisted.Kind != KindObject {
			t.Fatalf("PartialError kind = %v, want object", hoisted.Kind)
		}
		for _, name := range []string{"code", "message", "requestId", "retryable"} {
			if hoisted.Property(name) == nil {
				t.Errorf("PartialError is missing the inherited property %q", name)
			}
			if !hoisted.IsRequired(name) {
				t.Errorf("PartialError.%s should be required", name)
			}
		}
		if !strings.Contains(hoisted.Description, "requestId is required here") {
			t.Errorf("PartialError description = %q, want the allOf node's description", hoisted.Description)
		}
		// The synthesized type is a component schema like any other.
		if spec.Lookup("PartialError") == nil {
			t.Error("PartialError is not registered in the spec")
		}
	})

	t.Run("anyOf with a null member is nullable", func(t *testing.T) {
		update := mustSchema(t, spec, "ServiceSpecPatch").Property("update")
		if update == nil || update.Kind != KindRef || update.Ref != "UpdateConfig" || !update.Nullable {
			t.Fatalf("ServiceSpecPatch.update = %+v, want nullable $ref UpdateConfig", update)
		}
	})

	t.Run("wire names from snapshot and task documents", func(t *testing.T) {
		snapshot := mustSchema(t, spec, "Snapshot")
		for _, name := range []string{"memoryMB", "vcpuCount", "sizeBytes", "rootfsPath"} {
			if snapshot.Property(name) == nil {
				t.Errorf("Snapshot is missing %q", name)
			}
		}
		task := mustSchema(t, spec, "Task")
		if task.Property("serviceId") == nil {
			t.Error("Task is missing serviceId")
		}
		if exit := task.Property("exitCode"); exit == nil || exit.Kind != KindInteger {
			t.Errorf("Task.exitCode = %+v, want integer", exit)
		}
	})

	t.Run("enum properties carry their literals", func(t *testing.T) {
		code := mustSchema(t, spec, "Error").Property("code")
		if code == nil || code.Kind != KindEnum || len(code.Enum) != 15 {
			t.Fatalf("Error.code = %+v, want a 15-value enum", code)
		}
		if code.Enum[0].Raw != "invalid" || !code.Enum[0].IsString {
			t.Errorf("Error.code first literal = %+v", code.Enum[0])
		}
	})

	t.Run("byte and date-time formats", func(t *testing.T) {
		data := mustSchema(t, spec, "ExecFrame").Property("data")
		if data == nil || data.Kind != KindString || data.Format != "byte" {
			t.Errorf("ExecFrame.data = %+v, want string with format byte", data)
		}
		at := mustSchema(t, spec, "LogFrame").Property("time")
		if at == nil || at.Format != "date-time" {
			t.Errorf("LogFrame.time = %+v, want format date-time", at)
		}
	})

	t.Run("oneOf requirement fragments are recorded, not unioned", func(t *testing.T) {
		write := mustSchema(t, spec, "SecretWrite")
		if write.Kind != KindObject {
			t.Fatalf("SecretWrite kind = %v, want object", write.Kind)
		}
		if len(write.RequiresOneOf) != 2 ||
			strings.Join(write.RequiresOneOf[0], ",") != "data" ||
			strings.Join(write.RequiresOneOf[1], ",") != "driver" {
			t.Errorf("SecretWrite.RequiresOneOf = %v, want [[data] [driver]]", write.RequiresOneOf)
		}
		if write.IsRequired("data") || write.IsRequired("driver") {
			t.Error("SecretWrite must not require both oneOf alternatives")
		}
	})

	t.Run("additionalProperties false is a closed object", func(t *testing.T) {
		patch := mustSchema(t, spec, "ConfigPatch")
		if patch.HasExtra {
			t.Error("ConfigPatch must not carry a free-form map")
		}
	})
}

func TestParseRejectsUnsupportedConstructs(t *testing.T) {
	tests := []struct {
		name    string
		schemas string
		want    []string
	}{
		{
			name:    "unknown type",
			schemas: "    Bad: {type: widget}\n",
			want:    []string{"#/components/schemas/Bad", "unsupported type"},
		},
		{
			name:    "array without items",
			schemas: "    Bad:\n      type: object\n      properties:\n        list: {type: array}\n",
			want:    []string{"#/components/schemas/Bad/properties/list", "items"},
		},
		{
			name:    "unsupported keyword",
			schemas: "    Bad:\n      type: object\n      properties:\n        a:\n          not: {type: string}\n",
			want:    []string{"#/components/schemas/Bad/properties/a", "not"},
		},
		{
			name:    "required names an undeclared property",
			schemas: "    Bad:\n      type: object\n      required: [id, missing]\n      properties:\n        id: {type: string}\n",
			want:    []string{"#/components/schemas/Bad", "missing"},
		},
		{
			name:    "dangling ref",
			schemas: "    Bad: {$ref: '#/components/schemas/Nope'}\n",
			want:    []string{"#/components/schemas/Bad", "Nope"},
		},
		{
			name:    "non-component ref",
			schemas: "    Bad: {$ref: '#/definitions/Nope'}\n",
			want:    []string{"#/components/schemas/Bad", "$ref"},
		},
		{
			name:    "two non-null types",
			schemas: "    Bad: {type: [string, integer]}\n",
			want:    []string{"#/components/schemas/Bad", "one type"},
		},
		{
			name:    "allOf member is not an object",
			schemas: "    Bad:\n      allOf:\n        - {type: string}\n",
			want:    []string{"#/components/schemas/Bad/allOf/0", "object"},
		},
		{
			name: "allOf with a sibling properties",
			schemas: "    Bad:\n      allOf:\n        - {type: object}\n" +
				"      properties:\n        a: {type: string}\n",
			want: []string{"#/components/schemas/Bad", "allOf", "sibling"},
		},
		{
			name: "oneOf with a sibling properties",
			schemas: "    Bad:\n      oneOf:\n        - {type: string}\n        - {type: integer}\n" +
				"      properties:\n        a: {type: string}\n",
			want: []string{"#/components/schemas/Bad", "oneOf", "sibling"},
		},
		{
			name: "anyOf with a sibling required",
			schemas: "    Bad:\n      anyOf:\n        - {type: string}\n        - {type: integer}\n" +
				"      required: [a]\n",
			want: []string{"#/components/schemas/Bad", "anyOf", "sibling"},
		},
		{
			name: "two combinators on one node",
			schemas: "    Bad:\n      allOf:\n        - {type: object}\n" +
				"      oneOf:\n        - {type: string}\n        - {type: integer}\n",
			want: []string{"#/components/schemas/Bad", "allOf", "oneOf"},
		},
		{
			name: "allOf required names a property no member declares",
			schemas: "    Bad:\n      allOf:\n        - type: object\n          properties:\n            a: {type: string}\n" +
				"        - type: object\n          required: [missing]\n",
			want: []string{"#/components/schemas/Bad/allOf", "missing"},
		},
		{
			name: "synthesised inline type name is already taken",
			schemas: "    BadError: {type: object, properties: {a: {type: string}}}\n" +
				"    Bad:\n      type: object\n      properties:\n        error:\n          allOf:\n" +
				"            - type: object\n              properties:\n                b: {type: string}\n",
			want: []string{"#/components/schemas/Bad/properties/error", "already taken"},
		},
		{
			name: "allOf property clash with a different type",
			schemas: "    Bad:\n      allOf:\n        - type: object\n          properties:\n            a: {type: string}\n" +
				"        - type: object\n          properties:\n            a: {type: integer}\n",
			want: []string{"#/components/schemas/Bad", `property "a"`},
		},
		{
			name:    "additionalProperties is not a bool or schema",
			schemas: "    Bad:\n      type: object\n      additionalProperties: maybe\n",
			want:    []string{"#/components/schemas/Bad", "additionalProperties"},
		},
		{
			name:    "inline object with named properties",
			schemas: "    Bad:\n      type: object\n      properties:\n        nested:\n          type: object\n          properties:\n            a: {type: string}\n",
			want:    []string{"#/components/schemas/Bad/properties/nested", "inline object"},
		},
		{
			name:    "oneOf requirement fragments without a parent object",
			schemas: "    Bad:\n      oneOf:\n        - required: [a]\n        - required: [b]\n",
			want:    []string{"#/components/schemas/Bad/oneOf", "oneOf"},
		},
		{
			name:    "enum value is not a scalar",
			schemas: "    Bad:\n      type: string\n      enum:\n        - {a: b}\n",
			want:    []string{"#/components/schemas/Bad", "enum"},
		},
		{
			name:    "missing components",
			schemas: "",
			want:    []string{"components.schemas"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var src string
			if tc.schemas == "" {
				src = "openapi: 3.1.0\ninfo: {title: x, version: '1'}\n"
			} else {
				src = specWith(tc.schemas)
			}
			_, err := Parse([]byte(src), "test.yaml")
			if err == nil {
				t.Fatal("Parse() error = nil, want a loud failure")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
			if !strings.HasPrefix(err.Error(), "test.yaml: ") {
				t.Errorf("error %q does not name the file", err)
			}
		})
	}
}

func TestParseCombinedSchemas(t *testing.T) {
	src := specWith(`    Plain: {type: string}
    Listing:
      type: object
      description: A page.
      required: [items]
      properties:
        items:
          type: array
          items: {$ref: '#/components/schemas/Plain'}
        next:
          anyOf:
            - {type: string}
            - {type: 'null'}
    Legacy:
      type: object
      properties:
        when:
          type: string
          nullable: true
    Kinds:
      type: object
      properties:
        mode: {type: string, enum: [a, b]}
        count: {type: integer, enum: [1, 2]}
    FreeForm:
      type: object
      additionalProperties: true
    Typed:
      type: object
      additionalProperties: {$ref: '#/components/schemas/Plain'}
    Inferred:
      properties:
        id: {type: string}
`)
	spec := mustParse(t, src)

	inferred := mustSchema(t, spec, "Inferred")
	if inferred.Kind != KindObject || inferred.Property("id") == nil {
		t.Errorf("Inferred = %+v, want an object inferred from properties", inferred)
	}

	listing := mustSchema(t, spec, "Listing")
	if listing.Description != "A page." {
		t.Errorf("Listing description = %q", listing.Description)
	}
	items := listing.Property("items")
	if items == nil || items.Kind != KindArray || items.Items.Kind != KindRef || items.Items.Ref != "Plain" {
		t.Errorf("Listing.items = %+v, want array of $ref Plain", items)
	}
	next := listing.Property("next")
	if next == nil || !next.Nullable || next.Kind != KindString {
		t.Errorf("Listing.next = %+v, want nullable string", next)
	}

	legacy := mustSchema(t, spec, "Legacy").Property("when")
	if legacy == nil || !legacy.Nullable {
		t.Errorf("Legacy.when = %+v, want nullable via nullable: true", legacy)
	}

	kinds := mustSchema(t, spec, "Kinds")
	mode := kinds.Property("mode")
	if mode == nil || mode.Kind != KindEnum || len(mode.Enum) != 2 || mode.Enum[1].Raw != "b" {
		t.Errorf("Kinds.mode = %+v", mode)
	}
	count := kinds.Property("count")
	if count == nil || count.Kind != KindEnum || count.Enum[0].Raw != "1" || count.Enum[0].IsString {
		t.Errorf("Kinds.count = %+v", count)
	}

	free := mustSchema(t, spec, "FreeForm")
	if !free.HasExtra || free.Extra != nil || len(free.Properties) != 0 {
		t.Errorf("FreeForm = %+v, want a free-form map", free)
	}
	typed := mustSchema(t, spec, "Typed")
	if !typed.HasExtra || typed.Extra == nil || typed.Extra.Ref != "Plain" {
		t.Errorf("Typed = %+v, want map<string, $ref Plain>", typed)
	}
}

func TestParseMergesAllOfIntoOneObject(t *testing.T) {
	src := specWith(`    Base:
      type: object
      required: [id]
      properties:
        id: {type: string}
        note: {type: string}
    Extended:
      allOf:
        - $ref: '#/components/schemas/Base'
        - type: object
          required: [note]
          properties:
            note:
              type: string
              description: Later members win.
            extra: {type: boolean}
    Nested:
      type: object
      properties:
        inline:
          allOf:
            - $ref: '#/components/schemas/Base'
            - type: object
              required: [note]
`)
	spec := mustParse(t, src)

	extended := mustSchema(t, spec, "Extended")
	want := []string{"id", "note", "extra"}
	if got := propertyNames(extended); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Extended properties = %v, want %v", got, want)
	}
	if !extended.IsRequired("id") || !extended.IsRequired("note") || extended.IsRequired("extra") {
		t.Errorf("Extended required = %v, want the union [id note]", extended.Required)
	}
	if note := extended.Property("note"); note == nil || note.Description != "Later members win." {
		t.Errorf("Extended.note = %+v, want the later member's declaration", note)
	}

	nested := mustSchema(t, spec, "Nested").Property("inline")
	if nested == nil || nested.Kind != KindRef || nested.Ref != "NestedInline" {
		t.Fatalf("Nested.inline = %+v, want $ref NestedInline", nested)
	}
	hoisted := mustSchema(t, spec, "NestedInline")
	if !hoisted.IsRequired("note") || hoisted.Property("id") == nil {
		t.Errorf("NestedInline = %+v, want the merged Base object with note required", hoisted)
	}
}

func TestParseIsDeterministic(t *testing.T) {
	src, err := os.ReadFile(realSpecPath)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	first := mustParse(t, string(src))
	second := mustParse(t, string(src))

	firstPython, err := GeneratePython(first)
	if err != nil {
		t.Fatalf("GeneratePython() error = %v", err)
	}
	secondPython, err := GeneratePython(second)
	if err != nil {
		t.Fatalf("GeneratePython() error = %v", err)
	}
	if string(firstPython) != string(secondPython) {
		t.Error("Python output changed between two parses of the same spec")
	}

	firstTS, err := GenerateTypeScript(first)
	if err != nil {
		t.Fatalf("GenerateTypeScript() error = %v", err)
	}
	secondTS, err := GenerateTypeScript(second)
	if err != nil {
		t.Fatalf("GenerateTypeScript() error = %v", err)
	}
	if string(firstTS) != string(secondTS) {
		t.Error("TypeScript output changed between two parses of the same spec")
	}
}
