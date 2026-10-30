package contract

import "testing"

func TestResponseValidationRejectsMissingNullAndDuplicateFields(t *testing.T) {
	validator, err := NewResponseValidator()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		body  string
		valid bool
	}{
		{`{"items":[],"nextAfter":null}`, true},
		{`{"items":[]}`, false},
		{`{"items":null,"nextAfter":null}`, false},
		{`{"items":[],"items":[],"nextAfter":null}`, false},
		{`{"items":[],"nextAfter":null,"extra":true}`, false},
	} {
		var result TestList
		if err := validator.Decode("TestList", []byte(tc.body), &result); (err == nil) != tc.valid {
			t.Fatalf("%s: %v", tc.body, err)
		}
	}
}

func TestHandshakeResponseRequiresDatabaseIdentity(t *testing.T) {
	validator, err := NewResponseValidator()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		body  string
		valid bool
	}{
		{`{"apiVersion":"v1","backendRunId":"6c229f38-3426-4608-bd72-dcd0d3b2bcd7","databaseId":"9a0dce72-7219-45fc-8025-568b49e70919","demo":true}`, true},
		{`{"apiVersion":"v1","backendRunId":"6c229f38-3426-4608-bd72-dcd0d3b2bcd7","demo":true}`, false},
		{`{"apiVersion":"v1","backendRunId":"6c229f38-3426-4608-bd72-dcd0d3b2bcd7","databaseId":null,"demo":true}`, false},
		{`{"apiVersion":"v1","backendRunId":"6c229f38-3426-4608-bd72-dcd0d3b2bcd7","databaseId":"00000000-0000-0000-0000-000000000000","demo":true}`, false},
		{`{"apiVersion":"v1","backendRunId":"6c229f38-3426-4608-bd72-dcd0d3b2bcd7","databaseId":"bad","demo":true}`, false},
		{`{"apiVersion":"v1","backendRunId":"6c229f38-3426-4608-bd72-dcd0d3b2bcd7","databaseId":"9a0dce72-7219-45fc-8025-568b49e70919","databaseId":"9a0dce72-7219-45fc-8025-568b49e70919","demo":true}`, false},
	} {
		var result Handshake
		if err := validator.Decode("Handshake", []byte(tc.body), &result); (err == nil) != tc.valid {
			t.Fatalf("%s: %v", tc.body, err)
		}
	}
}
