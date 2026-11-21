package service

import (
	"bytes"
	"testing"
)

func TestLogFilterAcrossReadBoundaries(t *testing.T) {
	input := []byte("hello\x1b[31m中文\x1b[0m\x1b]52;c;SECRET\x1b\\\x1bPPRIVATE\x1b\\\n")
	var f logFilter
	var output []byte
	for _, b := range input {
		output = append(output, f.clean([]byte{b})...)
	}
	if !bytes.Equal(output, []byte("hello中文\n")) {
		t.Fatalf("unsafe or broken log: %q", output)
	}
}

func TestPlainLogControlSequencesAtEverySplit(t *testing.T) {
	for _, input := range []string{
		"前\x9b31m红\x9b0m后",
		"前\x1b]2;界\u041c标题\x9c红后",
		"前\x1b[32\x18红后",
		"前\x1b[31m红\x7f后",
	} {
		for cut := 0; cut <= len(input); cut++ {
			var f logFilter
			got := append([]byte(nil), f.clean([]byte(input[:cut]))...)
			got = append(got, f.clean([]byte(input[cut:]))...)
			if string(got) != "前红后" {
				t.Fatalf("split %d on %q: got %q", cut, input, got)
			}
		}
	}
}
