package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/vitalcc55/gk80-control/api"
	"go.yaml.in/yaml/v3"
)

const maxInputBytes = 8192
const maxIdentityBytes = maxInputBytes*6 + 1024

var ErrInvalidInput = errors.New("invalid request body")
var ErrIdentityTooLarge = errors.New("canonical identity too large")

// InputValidator validates the original JSON against the embedded OpenAPI schemas.
type InputValidator struct {
	schemas    map[string]*jsonschema.Schema
	structures map[string]*jsonschema.Schema
}

type InspectedInput struct {
	Identity []byte
	Valid    bool
}

func NewInputValidator() (*InputValidator, error) {
	var document map[string]any
	if err := yaml.Unmarshal([]byte(api.CoreSchema()), &document); err != nil {
		return nil, err
	}
	document["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	const source = "https://gk80.invalid/core.json"
	if err := compiler.AddResource(source, document); err != nil {
		return nil, err
	}
	var structural map[string]any
	if err := yaml.Unmarshal([]byte(api.CoreSchema()), &structural); err != nil {
		return nil, err
	}
	structural["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	removeValueConstraints(structural)
	const shapeSource = "https://gk80.invalid/core-structure.json"
	if err := compiler.AddResource(shapeSource, structural); err != nil {
		return nil, err
	}
	result := &InputValidator{schemas: make(map[string]*jsonschema.Schema), structures: make(map[string]*jsonschema.Schema)}
	for _, name := range []string{"LeaseRequest", "CreateTestCommand", "StartTestCommand", "StopTestCommand", "ConfirmActionCommand"} {
		schema, err := compiler.Compile(source + "#/components/schemas/" + name)
		if err != nil {
			return nil, fmt.Errorf("compile input schema %s: %w", name, err)
		}
		result.schemas[name] = schema
		structure, err := compiler.Compile(shapeSource + "#/components/schemas/" + name)
		if err != nil {
			return nil, fmt.Errorf("compile input structure %s: %w", name, err)
		}
		result.structures[name] = structure
	}
	return result, nil
}

// Decode rejects duplicate keys and missing or null properties before Go assigns zero values.
func (v *InputValidator) Decode(name string, body []byte, target any) error {
	result, err := v.Inspect(name, body, target)
	if err != nil || !result.Valid {
		return ErrInvalidInput
	}
	return nil
}

// Inspect permits a read-only lookup of a structurally valid historical rejection.
// Valid must be true before a new command may execute.
func (v *InputValidator) Inspect(name string, body []byte, target any) (InspectedInput, error) {
	return v.inspect(name, body, target, maxInputBytes)
}

// InspectCanonical checks a previously generated identity, whose escaped form can be larger than the original input.
func (v *InputValidator) InspectCanonical(name string, body []byte, target any) (InspectedInput, error) {
	return v.inspect(name, body, target, maxIdentityBytes)
}

func (v *InputValidator) inspect(name string, body []byte, target any, limit int) (InspectedInput, error) {
	schema := v.schemas[name]
	if schema == nil || len(body) > limit || !validUnicode(body) {
		return InspectedInput{}, ErrInvalidInput
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	value, err := inputValue(decoder, 0)
	if err != nil {
		return InspectedInput{}, ErrInvalidInput
	}
	if _, err := decoder.Token(); err != io.EOF {
		return InspectedInput{}, ErrInvalidInput
	}
	if err := v.structures[name].Validate(value); err != nil {
		return InspectedInput{}, ErrInvalidInput
	}
	valid := schema.Validate(value) == nil
	if object, ok := value.(map[string]any); ok {
		for _, key := range []string{"commandId", "clientId"} {
			if original, ok := object[key].(string); ok {
				parsed, err := uuid.Parse(original)
				if err != nil {
					return InspectedInput{}, ErrInvalidInput
				}
				object[key] = parsed.String()
			}
		}
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return InspectedInput{}, ErrInvalidInput
	}
	decoded := json.NewDecoder(bytes.NewReader(normalized))
	decoded.DisallowUnknownFields()
	if err := decoded.Decode(target); err != nil {
		return InspectedInput{}, ErrInvalidInput
	}
	identity, err := CanonicalJSON(normalized)
	if err != nil {
		return InspectedInput{}, err
	}
	return InspectedInput{Identity: identity, Valid: valid}, nil
}

// CanonicalJSON compares numeric values and object contents without float64 rounding.
func CanonicalJSON(body []byte) ([]byte, error) {
	if len(body) > maxIdentityBytes {
		return nil, ErrIdentityTooLarge
	}
	if !validUnicode(body) {
		return nil, ErrInvalidInput
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	value, err := inputValue(decoder, 0)
	if err != nil {
		return nil, ErrInvalidInput
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, ErrInvalidInput
	}
	return json.Marshal(canonicalValue(value))
}

func canonicalValue(value any) any {
	switch item := value.(type) {
	case json.Number:
		if item == "-0" {
			return json.Number("0")
		}
		return item
	case map[string]any:
		for key, child := range item {
			if key == "commandId" || key == "clientId" || key == "testId" || key == "actionId" {
				if text, ok := child.(string); ok {
					if parsed, err := uuid.Parse(text); err == nil {
						child = parsed.String()
					}
				}
			}
			item[key] = canonicalValue(child)
		}
		return item
	case []any:
		for index, child := range item {
			item[index] = canonicalValue(child)
		}
		return item
	default:
		return value
	}
}

func removeValueConstraints(value any) {
	switch item := value.(type) {
	case map[string]any:
		for _, key := range []string{"const", "enum", "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "minLength", "maxLength", "multipleOf", "not"} {
			delete(item, key)
		}
		for _, child := range item {
			removeValueConstraints(child)
		}
	case []any:
		for _, child := range item {
			removeValueConstraints(child)
		}
	}
}

func inputValue(decoder *json.Decoder, depth int) (any, error) {
	if depth > 32 {
		return nil, ErrInvalidInput
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		if number, ok := token.(json.Number); ok {
			return normalizeNumber(number)
		}
		return token, nil
	}
	switch delim {
	case '{':
		result := make(map[string]any)
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			name, ok := key.(string)
			if !ok {
				return nil, ErrInvalidInput
			}
			if _, exists := result[name]; exists {
				return nil, ErrInvalidInput
			}
			value, err := inputValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			result[name] = value
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return nil, ErrInvalidInput
		}
		return result, nil
	case '[':
		result := make([]any, 0)
		for decoder.More() {
			value, err := inputValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			result = append(result, value)
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return nil, ErrInvalidInput
		}
		return result, nil
	default:
		return nil, ErrInvalidInput
	}
}

func normalizeNumber(number json.Number) (json.Number, error) {
	text := number.String()
	if len(text) > maxIdentityBytes {
		return "", ErrInvalidInput
	}
	mantissa := text
	if index := strings.IndexAny(mantissa, "eE"); index >= 0 {
		mantissa = mantissa[:index]
	}
	zero := true
	for _, ch := range mantissa {
		if ch >= '1' && ch <= '9' {
			zero = false
			break
		}
	}
	if zero {
		if strings.HasPrefix(text, "-") {
			return json.Number("-0"), nil
		}
		return json.Number("0"), nil
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || value == 0 {
		return "", ErrInvalidInput
	}
	rational, ok := new(big.Rat).SetString(text)
	if !ok {
		return "", ErrInvalidInput
	}
	if rational.IsInt() {
		return json.Number(rational.Num().String()), nil
	}
	precision := 0
	if dot := strings.IndexByte(mantissa, '.'); dot >= 0 {
		precision = len(mantissa) - dot - 1
	}
	if index := strings.IndexAny(text, "eE"); index >= 0 {
		exponent := text[index+1:]
		sign := 1
		if strings.HasPrefix(exponent, "-") {
			sign = -1
			exponent = exponent[1:]
		} else if strings.HasPrefix(exponent, "+") {
			exponent = exponent[1:]
		}
		exponent = strings.TrimLeft(exponent, "0")
		if exponent != "" {
			value, err := strconv.Atoi(exponent)
			if err != nil {
				return "", ErrInvalidInput
			}
			precision -= sign * value
		}
	}
	if precision < 0 || precision > maxInputBytes+400 {
		return "", ErrInvalidInput
	}
	decimal := strings.TrimRight(rational.FloatString(precision), "0")
	decimal = strings.TrimSuffix(decimal, ".")
	return json.Number(decimal), nil
}

// encoding/json otherwise silently replaces malformed UTF-8 or surrogate escapes.
func validUnicode(body []byte) bool {
	if !utf8.Valid(body) {
		return false
	}
	inString := false
	for index := 0; index < len(body); index++ {
		if body[index] == '"' {
			inString = !inString
			continue
		}
		if !inString || body[index] != '\\' {
			continue
		}
		index++
		if index >= len(body) {
			return false
		}
		if body[index] != 'u' {
			continue
		}
		if index+4 >= len(body) {
			return false
		}
		code, err := strconv.ParseUint(string(body[index+1:index+5]), 16, 16)
		if err != nil {
			return false
		}
		index += 4
		if code >= 0xdc00 && code <= 0xdfff {
			return false
		}
		if code >= 0xd800 && code <= 0xdbff {
			if index+6 >= len(body) || body[index+1] != '\\' || body[index+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(body[index+3:index+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			index += 6
		}
	}
	return true
}
