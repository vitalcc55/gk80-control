package contract

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestBrowserResponseExamplesMatchOpenAPI(t *testing.T) {
	body, err := os.ReadFile("../../tests/fixtures/browser-responses.json")
	if err != nil {
		t.Fatal(err)
	}
	var examples []struct {
		Name   string `json:"name"`
		Schema string `json:"schema"`
		Kind   string `json:"kind"`
		JSON   string `json:"json"`
		Valid  bool   `json:"valid"`
	}
	if err := json.Unmarshal(body, &examples); err != nil {
		t.Fatal(err)
	}
	schemas := make(map[string]*jsonschema.Schema)
	for _, example := range examples {
		schema := schemas[example.Schema]
		if schema == nil {
			schema = schemaFor(t, example.Schema)
			schemas[example.Schema] = schema
		}
		t.Run(example.Schema+"/"+example.Name, func(t *testing.T) {
			raw := []byte(example.JSON)
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.UseNumber()
			var value any
			var parseErr error
			if example.Kind == "json" {
				value, parseErr = inputValue(decoder, 0)
			} else {
				// Preserve decimal lexemes without the production parser's float64 limits.
				// These cases compare the schema itself, not Go DTO decoding.
				parseErr = decoder.Decode(&value)
			}
			_, endErr := decoder.Token()
			valid := validUnicode(raw) && parseErr == nil && endErr == io.EOF && schema.Validate(value) == nil
			if valid != example.Valid {
				t.Fatalf("OpenAPI acceptance=%t, expected=%t: %s", valid, example.Valid, example.JSON)
			}
		})
	}
}
