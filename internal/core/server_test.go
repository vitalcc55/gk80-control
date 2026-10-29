package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"go.yaml.in/yaml/v3"
)

func TestHandshakeErrorsMatchContract(t *testing.T) {
	data, err := os.ReadFile("../../api/core.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	document["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("https://gk80.invalid/core.json", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("https://gk80.invalid/core.json#/components/schemas/ApiError")
	if err != nil {
		t.Fatal(err)
	}
	handler := handshakeHandler("known-secret", uuid.New())
	for _, authorization := range []string{"", "Bearer wrong", "wrong", "Bearer known-secret-extra"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/handshake", nil)
		req.Header.Set("Authorization", authorization)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != http.StatusUnauthorized || response.Header().Get("Content-Type") != "application/json" || response.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Fatalf("unauthorized response: %d %v", response.Code, response.Header())
		}
		var body contract.ApiError
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Code != "unauthorized" || body.Message != "Authentication required" {
			t.Fatalf("invalid response: %s: %v", response.Body.String(), err)
		}
		var actual any
		if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(actual); err != nil {
			t.Fatalf("response does not match ApiError: %v", err)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/handshake", nil)
	req.Header.Set("Authorization", "Bearer known-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET" {
		t.Fatalf("method response: %d %v", response.Code, response.Header())
	}
}
