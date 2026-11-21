package vtscreen

import (
	"image"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

func TestEmbeddedMouseEncodings(t *testing.T) {
	for _, tc := range []struct {
		tracking, encoding ansi.DECMode
		raw, want          string
		x                  int
	}{
		{1000, 1006, "\x1b[<80;40;10M", "\x1b[<80;5;3M", 4},
		{1000, 0, "\x1b[<0;40;10m", "\x1b[M#%#", 4},
		{1000, 0, "\x1b[<0;40;10M", "\x1b[M \xff#", 222},
		{1000, 0, "\x1b[<0;40;10M", "", 223},
		{1000, 1005, "\x1b[<0;40;10M", "\x1b[M Ā#", 223},
		{1000, 1015, "\x1b[<65;40;10M", "\x1b[97;224;3M", 223},
		{1002, 1006, "\x1b[<32;40;10M", "\x1b[<32;5;3M", 4},
		{1002, 1006, "\x1b[<35;40;10M", "", 4},
		{1003, 1006, "\x1b[<35;40;10M", "\x1b[<35;5;3M", 4},
		{1000, 1006, "\x1b[<32;40;10M", "", 4},
		{9, 0, "\x1b[<16;40;10M", "\x1b[M %#", 4},
		{9, 0, "\x1b[<0;40;10m", "", 4},
		{0, 1006, "\x1b[<64;40;10M", "", 4},
		{1000, 1016, "\x1b[<64;40;10M", "", 4},
	} {
		modes := MouseModes{Tracking: tc.tracking, Encoding: tc.encoding}
		event := DecodeMouse([]byte(tc.raw))
		if got := string(modes.EncodeCell(event, tc.x, 2)); got != tc.want {
			t.Errorf("%+v %q: got %q, want %q", modes, tc.raw, got, tc.want)
		}
	}
}

func TestLegacyMouseDragReleasesAtLastEncodableCell(t *testing.T) {
	var drag MouseCapture
	pane := image.Rect(10, 5, 350, 245)
	modes := MouseModes{Tracking: ansi.ModeMouseButtonEvent}
	for _, tc := range []struct{ raw, want string }{
		{"\x1b[<0;234;230M", ""}, // Outside the legacy range: no new press.
		{"\x1b[<0;11;6M", "\x1b[M !!"},
		{"\x1b[<32;340;240M", "\x1b[M@\xff\xff"},
		{"\x1b[<0;340;240m", "\x1b[M#\xff\xff"},
		{"\x1b[<32;12;6M", ""}, // The release ended capture.
	} {
		if got := string(drag.Input(modes, DecodeMouse([]byte(tc.raw)), pane)); got != tc.want {
			t.Fatalf("%q: got %q, want %q", tc.raw, got, tc.want)
		}
	}
}

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

func TestViewportKeepsEarlierRowsWhileOutputContinues(t *testing.T) {
	e := New(20, 3)
	defer e.Close()
	e.WriteString("\x1b[32molder\x1b[0m\r\nsecond\r\nthird\r\nfourth")
	var viewport Viewport
	viewport.Scroll(e, 3)
	rows := viewport.Rows(e)
	if !viewport.Scrolled() || !strings.Contains(rows[0], "\x1b[32molder") {
		t.Fatalf("lost scrollback style or content: %q", rows)
	}
	e.WriteString("\r\nlive")
	if got := viewport.Rows(e)[0]; got != rows[0] {
		t.Fatalf("live output moved scrollback: %q", got)
	}
	viewport.Scroll(e, -3)
	if viewport.Scrolled() || !strings.Contains(viewport.Rows(e)[2], "live") {
		t.Fatal("scrolling down did not return to the live screen")
	}
}
