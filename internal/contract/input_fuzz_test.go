package contract

import (
	"bytes"
	"fmt"
	"math"
	"math/big"
	"testing"
)

func FuzzCanonicalJSONNumber(f *testing.F) {
	f.Add(int64(0), int8(0))
	f.Add(int64(1), int8(-1))
	f.Add(int64(9007199254740993), int8(0)) // Beyond exact float64 integer precision.
	f.Add(int64(math.MinInt64), int8(9))
	f.Add(int64(math.MaxInt64), int8(-9))
	f.Fuzz(func(t *testing.T, coefficient int64, exponent int8) {
		exponent %= 10
		scientific := fmt.Appendf(nil, "%de%d", coefficient, exponent)
		fractional := fmt.Appendf(nil, "%d.0e%d", coefficient, exponent)
		a, err := CanonicalJSON(scientific)
		if err != nil {
			t.Fatal(err)
		}
		b, err := CanonicalJSON(fractional)
		if err != nil || !bytes.Equal(a, b) {
			t.Fatalf("equivalent numbers must have the same identity: %q vs %q (%v)", a, b, err)
		}
		want, ok := new(big.Rat).SetString(string(scientific))
		if !ok {
			t.Fatalf("invalid numeric probe: %s", scientific)
		}
		got, ok := new(big.Rat).SetString(string(a))
		if !ok || got.Cmp(want) != 0 {
			t.Fatalf("identity changed the numeric value of %s to %s", scientific, a)
		}
	})
}

func FuzzInspectStartCommand(f *testing.F) {
	validator, err := NewInputValidator()
	if err != nil {
		f.Fatal(err)
	}
	const id = "6fa459ea-ee8a-4ca4-894e-db77e160355e"
	f.Add([]byte(`{"commandId":"` + id + `","expectedRevision":0,"valveClosed":true,"connectionConfirmed":true}`))
	f.Add([]byte(`{"commandId":"` + id + `","expectedRevision":7,"valveClosed":false,"connectionConfirmed":true}`))
	f.Add([]byte(`{"commandId":"` + id + `","expectedRevision":1,"expectedRevision":2,"valveClosed":true,"connectionConfirmed":true}`))
	f.Add([]byte(`{"commandId":"` + id + `","expectedRevision":9223372036854775808,"valveClosed":true,"connectionConfirmed":true}`))
	f.Add([]byte(`{"commandId":"\uD800","expectedRevision":null} {}`))

	f.Fuzz(func(t *testing.T, body []byte) {
		var command StartTestCommand
		initial, err := validator.Inspect("StartTestCommand", body, &command)
		if err != nil {
			return
		}
		if initial.Valid && (!command.ValveClosed || !command.ConnectionConfirmed || command.ExpectedRevision < 0) {
			t.Fatal("inspection accepted a start without required confirmations or a nonnegative revision")
		}
		var restored StartTestCommand
		replay, err := validator.InspectCanonical("StartTestCommand", initial.Identity, &restored)
		if err != nil || replay.Valid != initial.Valid || restored != command || !bytes.Equal(replay.Identity, initial.Identity) {
			t.Fatalf("canonical replay changed command identity, payload or validity: %v", err)
		}
	})
}
