package contract

import (
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestHistoryQueryRoundTrip(t *testing.T) {
	values := url.Values{"q": {"  клапан 100%_А  "}, "createdFrom": {"2026-11-05T00:00:00+03:00"},
		"createdBefore": {"2026-11-06T00:00:00+03:00"}, "outcome": {"completed"}, "limit": {"1"},
		"after": {"11111111-1111-4111-8111-111111111111"}}
	params, err := ParseTestListQuery(values)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeTestListQuery(params)
	if err != nil {
		t.Fatal(err)
	}
	values.Set("q", "клапан 100%_А")
	if !reflect.DeepEqual(encoded, values) {
		t.Fatalf("query changed: %v, want %v", encoded, values)
	}
	if _, err := ParseTestListQuery(url.Values{}); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryQueryRejectsMalformedFilters(t *testing.T) {
	for _, raw := range []string{
		"q=a&q=b", "q=" + strings.Repeat("я", 201), "q=%FF", "after=00000000-0000-0000-0000-000000000000",
		"after={11111111-1111-4111-8111-111111111111}", "limit=0", "limit=501", "limit=%2B1", "limit=1.0",
		"createdFrom=2026-02-30T00:00:00Z", "createdBefore=", "outcome=all", "unknown=x",
		"createdFrom=2026-11-05T1:00:00Z", "createdBefore=2026-11-05T1:00:00.1Z",
		"createdFrom=2026-11-05T00:00:00.1234567891Z", "createdBefore=2026-11-05T00:00:00.1234567891Z",
		"createdFrom=2026-11-05T00:00:00,1Z", "createdFrom=2026-11-05T00:00:00%2B24:00",
		"createdFrom=2026-11-06T00:00:00Z&createdBefore=2026-11-06T00:00:00Z",
		"createdFrom=2026-11-07T00:00:00Z&createdBefore=2026-11-06T00:00:00Z",
	} {
		t.Run(raw, func(t *testing.T) {
			values, err := url.ParseQuery(raw)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseTestListQuery(values); err == nil {
				t.Fatal("invalid filter accepted")
			}
		})
	}
}

func TestHistoryQueryValidatesRawWailsDateFieldsBeforeTimeConversion(t *testing.T) {
	for _, raw := range []string{"", "2026-11-05T00:00:00.1234567891Z"} {
		if _, err := NormalizeTestListParams(ListTestsParams{CreatedFrom: &raw}); err == nil {
			t.Fatalf("raw date accepted: %q", raw)
		}
		if _, err := NormalizeTestListParams(ListTestsParams{CreatedBefore: &raw}); err == nil {
			t.Fatalf("raw date accepted: %q", raw)
		}
	}
}
