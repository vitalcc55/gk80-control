package transport

import (
	"bufio"
	"strings"
	"testing"
)

func TestBootstrapFramesAreBoundedAndStrict(t *testing.T) {
	for _, input := range []string{
		`{"secret":"x","unknown":"y"}` + "\n",
		`{"secret":"x"} {}` + "\n",
		strings.Repeat("a", 8193) + "\n",
		`{"secret":"x"}`,
	} {
		var result Bootstrap
		if err := ReadFrame(bufio.NewReaderSize(strings.NewReader(input), 8193), &result); err == nil {
			t.Fatalf("invalid frame accepted: length=%d", len(input))
		}
	}
}
