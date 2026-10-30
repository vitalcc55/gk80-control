package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/vitalcc55/gk80-control/api"
	"go.yaml.in/yaml/v3"
)

var ErrInvalidResponse = errors.New("invalid core response")

// ResponseValidator validates the received JSON before Go assigns zero values.
type ResponseValidator struct{ schemas map[string]*jsonschema.Schema }

func NewResponseValidator() (*ResponseValidator, error) {
	var document map[string]any
	if err := yaml.Unmarshal([]byte(api.CoreSchema()), &document); err != nil {
		return nil, err
	}
	document["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	const source = "https://gk80.invalid/core-response.json"
	if err := compiler.AddResource(source, document); err != nil {
		return nil, err
	}
	result := &ResponseValidator{schemas: make(map[string]*jsonschema.Schema)}
	for _, name := range []string{"UiLease", "Snapshot", "TestList", "TestRecord", "Readiness", "MeasurementPage", "AuditPage", "CommandStatus", "CommandAccepted", "ApiError"} {
		schema, err := compiler.Compile(source + "#/components/schemas/" + name)
		if err != nil {
			return nil, fmt.Errorf("compile response schema %s: %w", name, err)
		}
		result.schemas[name] = schema
	}
	return result, nil
}

func (v *ResponseValidator) Decode(name string, body []byte, target any) error {
	schema := v.schemas[name]
	if schema == nil || !validUnicode(body) {
		return ErrInvalidResponse
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	value, err := inputValue(decoder, 0)
	if err != nil {
		return ErrInvalidResponse
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrInvalidResponse
	}
	if err := schema.Validate(value); err != nil {
		return ErrInvalidResponse
	}
	decoded := json.NewDecoder(bytes.NewReader(body))
	decoded.DisallowUnknownFields()
	if err := decoded.Decode(target); err != nil {
		return ErrInvalidResponse
	}
	return nil
}
