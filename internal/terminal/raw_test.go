package terminal

import (
	"bytes"
	"strings"
	"testing"
)

func TestPasteEndingAfterLiteralEscDoesNotCaptureNextKey(t *testing.T) {
	paste := "\x1b[200~literal\x1b\x1b[201~"
	for split := 0; split <= len(paste); split++ {
		commands := 0
		d := EscapeDecoder{Command: func(raw []byte, _ uint16) bool {
			if string(raw) == "\x1b" {
				commands++
				return true
			}
			return false
		}}
		first, handled := d.Feed([]byte(paste[:split]))
		last, done := d.Feed([]byte(paste[split:]))
		if handled || done || string(append(first, last...)) != paste || d.pasting || commands != 0 {
			t.Fatalf("split %d: paste changed or did not end", split)
		}
		d.Feed([]byte{0x1b})
		if output := d.FlushEscape(); len(output) != 0 || commands != 1 {
			t.Fatal("Esc after paste was forwarded")
		}
	}
}

func TestEmbeddedCommandPreservesSwitchTypeaheadAcrossEverySplit(t *testing.T) {
	input := []byte("before\x1b[Bafter中文")
	for split := 0; split <= len(input); split++ {
		commands := 0
		d := EscapeDecoder{Command: func(raw []byte, _ uint16) bool {
			if string(raw) != "\x1b[B" {
				return false
			}
			commands++
			return true
		}}
		var output, remaining []byte
		for _, part := range [][]byte{input[:split], input[split:]} {
			if commands > 0 {
				remaining = append(remaining, part...)
				continue
			}
			data, handled := d.Feed(part)
			output = append(output, data...)
			if handled {
				remaining = append(remaining, d.Remaining...)
			}
		}
		if commands != 1 || string(output) != "before" || string(remaining) != "after中文" {
			t.Fatalf("split %d: commands=%d old=%q new=%q", split, commands, output, remaining)
		}
	}
}

func TestEmbeddedCommandsIgnorePasteAndRemovedShortcuts(t *testing.T) {
	d := EscapeDecoder{Command: func(raw []byte, _ uint16) bool {
		return string(raw) == "\x1b[B"
	}}
	input := []byte("\x02x\x1b[200~\x02\x1b[B\x1b[24~\x02d\x1b[201~")
	var output []byte
	for _, b := range input {
		data, handled := d.Feed([]byte{b})
		if handled {
			t.Fatal("paste or unknown command intercepted")
		}
		output = append(output, data...)
	}
	if !bytes.Equal(output, input) {
		t.Fatalf("literal input changed: %q", output)
	}
}

func TestCtrlEnterOnlyInterceptedByEmbeddedControl(t *testing.T) {
	for _, encoding := range []string{"\x1b[13;5u", "\x1b[13;5:1u", "\x1b[27;5;13~", "\x1b[13;69u"} {
		for split := 0; split <= len(encoding); split++ {
			d := EscapeDecoder{Command: func(_ []byte, key uint16) bool { return key == KeyCtrlEnter }}
			first, handled := d.Feed([]byte(encoding[:split]))
			if !handled {
				last, done := d.Feed([]byte(encoding[split:]))
				first, handled = append(first, last...), done
			}
			if !handled || len(first) != 0 {
				t.Fatalf("encoding %q split %d was not handled", encoding, split)
			}
		}
		d := EscapeDecoder{}
		output, handled := d.Feed([]byte(encoding))
		if handled || string(output) != encoding {
			t.Fatal("fullscreen attachment intercepted Ctrl+Enter")
		}
	}
	for _, literal := range []string{"\r", "\n", "\x1b[13;2u", "\x1b[13;5:3u", "\x1b[200~\x1b[13;5u\x1b[201~"} {
		d := EscapeDecoder{Command: func(_ []byte, key uint16) bool { return key == KeyCtrlEnter }}
		output, handled := d.Feed([]byte(literal))
		if handled || string(output) != literal {
			t.Fatalf("application input intercepted: %q", literal)
		}
	}
}

func TestEscapeAcrossReadBoundaries(t *testing.T) {
	d := EscapeDecoder{}
	first, detached := d.Feed([]byte("a\x1b[113;"))
	if detached || string(first) != "a" {
		t.Fatal("incorrect partial escape")
	}
	second, detached := d.Feed([]byte("5ux"))
	if !detached || len(second) != 0 || string(d.Remaining) != "x" {
		t.Fatal("detach or remaining input failed")
	}
}

func TestVimEscapeAndDetachCoalescedDuringStartup(t *testing.T) {
	for _, shortcut := range []string{"\x11", "\x1b[113;5u", "\x1b[24~"} {
		input := []byte("text\x1b" + shortcut + "suffix")
		for split := 0; split <= len(input); split++ {
			d := EscapeDecoder{}
			first, detached := d.Feed(input[:split])
			if !detached {
				last, done := d.Feed(input[split:])
				first, detached = append(first, last...), done
			}
			if !detached || string(first) != "text\x1b" {
				t.Fatalf("shortcut %q split %d: output=%q detached=%v", shortcut, split, first, detached)
			}
		}
	}
}

func TestOrdinaryInputUnchanged(t *testing.T) {
	input := []byte("\x03\x1b[<0;10;12M\x1b[200~中文\x1b[201~\x00\xff")
	d := EscapeDecoder{}
	var output []byte
	for _, b := range input {
		part, detach := d.Feed([]byte{b})
		if detach {
			t.Fatal("unexpected detach")
		}
		output = append(output, part...)
	}
	if !bytes.Equal(output, input) {
		t.Fatalf("input changed: %q", output)
	}
}

func TestRemovedDetachShortcutsRemainLiteral(t *testing.T) {
	for _, input := range []string{
		"\x02d", "\x02\x04", "\x1dd", "\x02\x02", "\x1d\x1d", "\x02", "\x1d",
		"\x1b[98;5ud", "\x1b[98;5u\x1b[100;5u", "\x1b[27;5;98~\x1b[27;5;100~",
	} {
		for split := 0; split <= len(input); split++ {
			d := EscapeDecoder{}
			first, detached := d.Feed([]byte(input[:split]))
			last, done := d.Feed([]byte(input[split:]))
			output := append(append(first, last...), d.Flush()...)
			if detached || done || string(output) != input {
				t.Fatalf("removed shortcut %q split %d changed to %q", input, split, output)
			}
		}
	}
}

func TestDetachKeysAtEveryReadBoundary(t *testing.T) {
	for _, key := range []string{
		"\x1b[24~", "\x1b[24;1~", "\x1b[24;1:1~", "\x1b[24;1:2~", "\x1b[24;65~",
		"\x11", "\x1b[113;5u", "\x1b[113;5:1u", "\x1b[113;5:2u", "\x1b[113;69u", "\x1b[27;5;113~",
	} {
		for split := 0; split <= len(key); split++ {
			d := EscapeDecoder{}
			out, detached := d.Feed([]byte(key[:split]))
			if !detached {
				part, done := d.Feed([]byte(key[split:]))
				out = append(out, part...)
				detached = done
			}
			if !detached || len(out) != 0 {
				t.Fatalf("key %q split %d: detached=%v output=%q", key, split, detached, out)
			}
		}
	}
}

func TestDetachDecoderPreservesOtherInput(t *testing.T) {
	for _, input := range []string{
		"\x04", "\x1b[100;5u", "\x1b[27;5;100~", // Ctrl+D without a prefix.
		"\x1b[24;2~\x1b[24;5~\x1b[24;1:3~",       // Modified/released F12.
		"\x1b[113;6u\x1b[113;5:3u\x1b[27;6;113~", // Modified/released Ctrl+Q.
		"\x1b[A\x1b[1;5D\x1b[<0;10;12M\x1b[1;2R", // Other terminal input.
		"中文🙂\x00\xff\xf0\xf1\xf2\xf3", "\x1bx", "\x1b\x1b", "\x1b",
		"\x1b[200~pasted:\x11\x1b[113;5u\x1b[24~\x02d\x02\x04\x1dd\x04\x1b[201~",
		"\x1b[" + strings.Repeat("1", 1000) + "~",
	} {
		for width := 1; width <= len(input); width++ {
			d := EscapeDecoder{}
			var output []byte
			for start := 0; start < len(input); start += width {
				part, detached := d.Feed([]byte(input[start:min(start+width, len(input))]))
				if detached {
					t.Fatalf("unexpected detach for %q", input)
				}
				output = append(output, part...)
			}
			output = append(output, d.Flush()...)
			if string(output) != input {
				t.Fatalf("changed %q to %q", input, output)
			}
		}
	}
}

func TestEscapeTimeoutAndPasteExit(t *testing.T) {
	d := EscapeDecoder{}
	if out, done := d.Feed([]byte("\x1b")); len(out) != 0 || done {
		t.Fatal("incomplete escape")
	}
	if string(d.flushEscape()) != "\x1b" || d.Flush() != nil {
		t.Fatal("standalone Esc lost")
	}
	out, done := d.Feed([]byte("[24~\x04"))
	if done || string(out) != "[24~\x04" {
		t.Fatal("expired prefix became a shortcut")
	}
	d = EscapeDecoder{}
	input := "\x1b[200~\x11\x1b[113;5u\x1b[24~\x02d\x1b[201~"
	out, done = d.Feed([]byte(input))
	if done || string(out) != input {
		t.Fatal("paste intercepted")
	}
	if _, done = d.Feed([]byte("\x1b[24~")); !done {
		t.Fatal("paste mode did not end")
	}
}

func TestNoEscapePassesAllDetachKeys(t *testing.T) {
	d := EscapeDecoder{Disable: true}
	input := "\x11\x1b[113;5u\x1b[27;5;113~\x1b[24~\x02d\x02\x04\x1dd\x04"
	out, done := d.Feed([]byte(input))
	if done || string(out) != input || d.Flush() != nil {
		t.Fatal("no-escape changed input")
	}
}

func TestEscapeBufferIsBounded(t *testing.T) {
	d := EscapeDecoder{}
	input := "\x1b[98;5u" + strings.Repeat("\x1b[98;5:3u", 1000)
	out, done := d.Feed([]byte(input))
	out = append(out, d.Flush()...)
	if done || string(out) != input {
		t.Fatal("release flood lost bytes")
	}
	if len(d.escape) > inputEscapeLimit {
		t.Fatal("unbounded input state")
	}
}
