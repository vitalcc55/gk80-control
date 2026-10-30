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
