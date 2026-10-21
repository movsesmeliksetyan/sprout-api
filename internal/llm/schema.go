package llm

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// unsupported are the JSON Schema keywords the provider's structured output
// does not take. They are left out of the schema sent with a call and
// checked on the answer instead.
var unsupported = map[string]bool{
	"minimum":          true,
	"maximum":          true,
	"exclusiveMinimum": true,
	"exclusiveMaximum": true,
	"multipleOf":       true,
	"minLength":        true,
	"maxLength":        true,
	"pattern":          true,
	"minItems":         true,
	"maxItems":         true,
	"uniqueItems":      true,
}

// Schema is the JSON Schema an answer must match. Every object in it must
// set "additionalProperties": false.
type Schema struct {
	// Name identifies the schema in logs and in the fake's keys.
	Name string
	// constrained is the schema as sent to the provider.
	constrained map[string]any
	validator   *jsonschema.Schema
}

// NewSchema compiles a JSON Schema document. It is meant for package-level
// variables, so a schema that does not compile is a programming error.
func NewSchema(name string, document []byte) (*Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(document))
	if err != nil {
		return nil, fmt.Errorf("llm: schema %s: %w", name, err)
	}
	compiler := jsonschema.NewCompiler()
	location := "schema://" + name
	if err := compiler.AddResource(location, doc); err != nil {
		return nil, fmt.Errorf("llm: schema %s: %w", name, err)
	}
	validator, err := compiler.Compile(location)
	if err != nil {
		return nil, fmt.Errorf("llm: schema %s: %w", name, err)
	}

	var raw map[string]any
	if err := json.Unmarshal(document, &raw); err != nil {
		return nil, fmt.Errorf("llm: schema %s: %w", name, err)
	}
	constrained, _ := strip(raw).(map[string]any)
	return &Schema{Name: name, constrained: constrained, validator: validator}, nil
}

// MustSchema is NewSchema for schemas fixed at compile time.
func MustSchema(name string, document []byte) *Schema {
	schema, err := NewSchema(name, document)
	if err != nil {
		panic(err)
	}
	return schema
}

// validate reports whether answer is JSON that matches the whole schema,
// the keywords the provider did not see included.
func (s *Schema) validate(answer []byte) error {
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(answer))
	if err != nil {
		return fmt.Errorf("%w: not JSON: %w", ErrInvalidOutput, err)
	}
	if err := s.validator.Validate(value); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidOutput, err)
	}
	return nil
}

// strip returns a copy of a schema node without the unsupported keywords.
// The keys of "properties" are names, not keywords, and are kept.
func strip(node any) any {
	switch value := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, child := range value {
			switch {
			case unsupported[key]:
			case key == "properties":
				properties, _ := child.(map[string]any)
				kept := make(map[string]any, len(properties))
				for name, property := range properties {
					kept[name] = strip(property)
				}
				out[key] = kept
			default:
				out[key] = strip(child)
			}
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, child := range value {
			out[i] = strip(child)
		}
		return out
	}
	return node
}
