package vtscreen

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

func TestUnicodeTitlesStayOffScreenAcrossWrites(t *testing.T) {
	for _, title := range []string{"查看5090当前训练状态 | codex", "✳ Claude Code", "末尾 “quoted” 🚀"} {
		for _, ending := range []string{"\a", "\x1b\\", "\x9c"} {
			sequence := "\x1b]0;" + title + ending
			for split := 0; split <= len(sequence); split++ {
				e := New(80, 5)
				gotTitle := ""
				e.SetCallbacks(vt.Callbacks{Title: func(value string) { gotTitle = value }})
				e.WriteString("prompt> ")
				e.Write([]byte(sequence[:split]))
				e.Write([]byte(sequence[split:]))
				e.WriteString("typed")
				row := strings.TrimSpace(ansi.Strip(Rows(e)[0]))
				e.Close()
				if gotTitle != title || row != "prompt> typed" {
					t.Fatalf("title %q ending %q split %d: title=%q row=%q", title, ending, split, gotTitle, row)
				}
			}
		}
	}
}

func TestUnicodeControlStringsDoNotPrint(t *testing.T) {
	for _, sequence := range []string{
		"\x1bPq✳ hidden\x1b\\",
		"\x1b_G日本✳ hidden\x1b\\",
		"\x1bX日本✳ hidden\x1b\\",
		"\x1b^日本✳ hidden\x9c",
		"\x1b]2;\xe2\x9c\x1b\\",
	} {
		e := New(80, 5)
		for _, b := range []byte("before" + sequence + "after") {
			e.Write([]byte{b})
		}
		row := strings.TrimSpace(ansi.Strip(Rows(e)[0]))
		e.Close()
		if row != "beforeafter" {
			t.Fatalf("control string %q leaked into screen: %q", sequence, row)
		}
	}
}
