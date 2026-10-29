package contract

import (
	"strings"
	"testing"
)

func TestDecodeInputChecksOriginalJSONBeforeDTO(t *testing.T) {
	validator, err := NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	valid := `{"commandId":"93c5921e-e171-4cd0-a5d1-668d337405c8","expectedRevision":0,"parameters":{"valve":"V","areaM2":0.1,"pmaxPa":1000,"measurementSeconds":1,"direction":"pressure","actuator":false}}`
	cases := []struct {
		name, body string
		valid      bool
	}{
		{"valid zero and false", valid, true},
		{"integer decimal", strings.Replace(valid, `"expectedRevision":0`, `"expectedRevision":1.0`, 1), true},
		{"integer exponent", strings.Replace(valid, `"expectedRevision":0`, `"expectedRevision":1e0`, 1), true},
		{"integer precision", strings.Replace(valid, `"expectedRevision":0`, `"expectedRevision":9007199254740993.0`, 1), true},
		{"huge exponent", strings.Replace(valid, `"pmaxPa":1000`, `"pmaxPa":1e1000001`, 1), false},
		{"tiny exponent", strings.Replace(valid, `"pmaxPa":1000`, `"pmaxPa":1e-1000001`, 1), false},
		{"zero huge exponent", strings.Replace(valid, `"expectedRevision":0`, `"expectedRevision":0e1000001`, 1), true},
		{"invalid utf8", strings.Replace(valid, `"valve":"V"`, "\"valve\":\"\xff\"", 1), false},
		{"lone high surrogate", strings.Replace(valid, `"valve":"V"`, `"valve":"\ud800"`, 1), false},
		{"lone low surrogate", strings.Replace(valid, `"valve":"V"`, `"valve":"\udc00"`, 1), false},
		{"surrogate pair", strings.Replace(valid, `"valve":"V"`, `"valve":"\ud83d\ude00"`, 1), true},
		{"missing revision", strings.Replace(valid, `"expectedRevision":0,`, "", 1), false},
		{"null revision", strings.Replace(valid, `"expectedRevision":0`, `"expectedRevision":null`, 1), false},
		{"missing actuator", strings.Replace(valid, `,"actuator":false`, "", 1), false},
		{"null actuator", strings.Replace(valid, `"actuator":false`, `"actuator":null`, 1), false},
		{"duplicate nested property", strings.Replace(valid, `"actuator":false`, `"actuator":false,"actuator":false`, 1), false},
		{"duplicate top property", strings.Replace(valid, `"expectedRevision":0`, `"expectedRevision":0,"expectedRevision":0`, 1), false},
		{"unknown property", strings.Replace(valid, `"valve":"V"`, `"valve":"V","extra":1`, 1), false},
		{"wrong uuid", strings.Replace(valid, `93c5921e-e171-4cd0-a5d1-668d337405c8`, `not-uuid`, 1), false},
		{"integer overflow", strings.Replace(valid, `"expectedRevision":0`, `"expectedRevision":9223372036854775808`, 1), false},
		{"second object", valid + ` {}`, false},
		{"oversized", valid + strings.Repeat(" ", 8193), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var result CreateTestCommand
			err := validator.Decode("CreateTestCommand", []byte(tc.body), &result)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
		})
	}
	var precise CreateTestCommand
	err = validator.Decode("CreateTestCommand", []byte(strings.Replace(valid, `"expectedRevision":0`, `"expectedRevision":9007199254740993.0`, 1)), &precise)
	if err != nil || precise.ExpectedRevision != 9007199254740993 {
		t.Fatalf("revision precision: %+v %v", precise, err)
	}
}

func TestDecodeInputChecksAllCommandSchemas(t *testing.T) {
	validator, err := NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body string
		target     any
		valid      bool
	}{
		{"LeaseRequest", `{"clientId":"93c5921e-e171-4cd0-a5d1-668d337405c8"}`, &LeaseRequest{}, true},
		{"StartTestCommand", `{"commandId":"93c5921e-e171-4cd0-a5d1-668d337405c8","expectedRevision":0,"valveClosed":true,"connectionConfirmed":true}`, &StartTestCommand{}, true},
		{"StartTestCommand", `{"commandId":"93c5921e-e171-4cd0-a5d1-668d337405c8","valveClosed":true,"connectionConfirmed":true}`, &StartTestCommand{}, false},
		{"ConfirmActionCommand", `{"commandId":"93c5921e-e171-4cd0-a5d1-668d337405c8"}`, &ConfirmActionCommand{}, false},
		{"StopTestCommand", `{"commandId":"93c5921e-e171-4cd0-a5d1-668d337405c8"}`, &StopTestCommand{}, true},
		{"Handshake", `{"apiVersion":"v1"}`, &Handshake{}, false},
	} {
		err := validator.Decode(tc.name, []byte(tc.body), tc.target)
		if (err == nil) != tc.valid {
			t.Fatalf("%s %s: %v", tc.name, tc.body, err)
		}
	}
}

func TestInspectInputPreservesExactIdentityAndLegacyRejection(t *testing.T) {
	validator, err := NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	base := `{"commandId":"93c5921e-e171-4cd0-a5d1-668d337405c8","expectedRevision":0,"parameters":{"valve":"V","areaM2":1,"pmaxPa":1000,"measurementSeconds":1,"direction":"pressure","actuator":false}}`
	var first, different, equivalent CreateTestCommand
	a, err := validator.Inspect("CreateTestCommand", []byte(base), &first)
	if err != nil || !a.Valid {
		t.Fatalf("base: %+v %v", a, err)
	}
	b, err := validator.Inspect("CreateTestCommand", []byte(strings.Replace(base, `"areaM2":1`, `"areaM2":1.0000000000000001`, 1)), &different)
	if err != nil || !b.Valid || first.Parameters.AreaM2 != different.Parameters.AreaM2 || string(a.Identity) == string(b.Identity) {
		t.Fatalf("distinct input: %+v %v", b, err)
	}
	c, err := validator.Inspect("CreateTestCommand", []byte(strings.Replace(base, `"areaM2":1`, `"areaM2":1.00`, 1)), &equivalent)
	if err != nil || string(a.Identity) != string(c.Identity) {
		t.Fatalf("equivalent input: %+v %v", c, err)
	}
	decimalA, err := CanonicalJSON([]byte(`{"number":0.100,"commandId":"93C5921E-E171-4CD0-A5D1-668D337405C8"}`))
	if err != nil {
		t.Fatal(err)
	}
	decimalB, err := CanonicalJSON([]byte(`{"commandId":"93c5921e-e171-4cd0-a5d1-668d337405c8","number":1e-1}`))
	if err != nil || string(decimalA) != string(decimalB) {
		t.Fatalf("canonical decimal: %s %s %v", decimalA, decimalB, err)
	}
	var start StartTestCommand
	rejected := `{"commandId":"93c5921e-e171-4cd0-a5d1-668d337405c8","expectedRevision":0,"valveClosed":false,"connectionConfirmed":true}`
	parsed, err := validator.Inspect("StartTestCommand", []byte(rejected), &start)
	if err != nil || parsed.Valid || start.ValveClosed {
		t.Fatalf("legacy rejected: %+v %v", parsed, err)
	}
	_, err = validator.Inspect("StartTestCommand", []byte(strings.Replace(rejected, `"expectedRevision":0,`, "", 1)), &start)
	if err == nil {
		t.Fatal("missing revision accepted on replay path")
	}
}

func TestLongInputNumberRemainsValidAfterCanonicalization(t *testing.T) {
	validator, err := NewInputValidator()
	if err != nil {
		t.Fatal(err)
	}
	body := `{"commandId":"93c5921e-e171-4cd0-a5d1-668d337405c8","expectedRevision":0,"parameters":{"valve":"V","areaM2":1.` + strings.Repeat("1", 7900) + `e-322,"pmaxPa":1000,"measurementSeconds":1,"direction":"pressure","actuator":false}}`
	if len(body) >= 8192 {
		t.Fatalf("input size: %d", len(body))
	}
	var command CreateTestCommand
	input, err := validator.Inspect("CreateTestCommand", []byte(body), &command)
	if err != nil || !input.Valid || command.Parameters.AreaM2 <= 0 {
		t.Fatalf("input: %+v %v", input.Valid, err)
	}
}
