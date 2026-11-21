package terminal

import (
	"image"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/raywcm/taskdock/internal/protocol"
	"github.com/raywcm/taskdock/internal/task"
	"github.com/raywcm/taskdock/internal/vtscreen"
)

func TestStatusLineBoundsAndSanitizesMetadata(t *testing.T) {
	for _, width := range []int{1, 8, 20, 80, 160} {
		s := statusScreen{name: strings.Repeat("项目", 35) + "\x1b\r\n", memory: "1023 MiB", clock: "15:45 21-Nov-26"}
		line := s.statusLine(width)
		if ansi.StringWidth(line) != width || strings.ContainsAny(line, "\x1b\r\n") {
			t.Fatalf("invalid row at width %d: %q", width, line)
		}
		if width >= 24 && !strings.Contains(line, "RSS 1023 MiB") {
			t.Fatal("name hid memory", line)
		}
	}
	for width := 1; width < 160; width++ {
		s := statusScreen{name: strings.Repeat("会话", 128), windowName: "codex", title: strings.Repeat("赛", 300), mode: "Read only", memory: "1023 MiB", clock: "15:45 21-Nov-26"}
		line := s.statusLine(width)
		if ansi.StringWidth(line) != width || (width >= 65 && !strings.Contains(line, "] 0:codex*")) {
			t.Fatalf("width %d: %q", width, line)
		}
	}
	s := statusScreen{name: "a", command: "sh", memory: "1 MiB", rowMarker: "┃ "}
	for width := 1; width <= 80; width++ {
		if line := s.statusLine(width); ansi.StringWidth(line) != width {
			t.Fatalf("footer does not fill %d columns: %q", width, line)
		}
	}
	if got := sessionSize(task.Size{Cols: 80, Rows: 1}, true); got.Rows != 1 {
		t.Fatal(got)
	}
	if got := sessionSize(task.Size{Cols: 80, Rows: 24}, true); got.Rows != 23 {
		t.Fatal(got)
	}
}

func TestStatusScreenCursorWrapAndSavedCursorAreIndependentOfBar(t *testing.T) {
	s := newStatusScreen(task.Size{Cols: 8, Rows: 5}, task.Spec{Name: "test"}, nil)
	defer s.close()
	if err := s.feed([]byte("12345678")); err != nil {
		t.Fatal(err)
	}
	// Resource updates leave the application's phantom wrap and saved cursor alone.
	s.update("renamed", &task.Resources{MemoryBytes: 123 << 20})
	if err := s.feed([]byte("X\x1b7\x1b[4;1HZ\x1b8Y")); err != nil {
		t.Fatal(err)
	}
	rows := vtscreen.Rows(s.e)
	if !strings.HasPrefix(ansi.Strip(rows[0]), "12345678") || !strings.HasPrefix(ansi.Strip(rows[1]), "XY") {
		t.Fatal(rows)
	}
	if s.name != "renamed" || s.memory != "123 MiB" {
		t.Fatal(s.name, s.memory)
	}
}

func TestStatusScreenHistoricalQueriesNeverReachProcess(t *testing.T) {
	replies := make(chan string, 4)
	s := newStatusScreen(task.Size{Cols: 80, Rows: 24}, task.Spec{Name: "test"}, func(frame protocol.Frame) error { replies <- string(frame.Payload); return nil })
	defer s.close()
	if err := s.feed([]byte("\x1b[6n\x1b[c\x1b]52;c;historical\a\x1b]11;?\a")); err != nil {
		t.Fatal(err)
	}
	s.start()
	if s.controls.Len() != 0 {
		t.Fatal("replayed historical terminal side effects")
	}
	if err := s.feed([]byte("\x1b[3;7H\x1b[6n")); err != nil {
		t.Fatal(err)
	}
	if got := <-replies; got != "\x1b[3;7R" {
		t.Fatalf("wrong viewport cursor report %q", got)
	}
	select {
	case extra := <-replies:
		t.Fatalf("historical reply escaped %q", extra)
	default:
	}
	if err := s.feed([]byte("\x1b]11;?\a")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.controls.String(), "\x1b]11;?\x1b\\") {
		t.Fatal("lost live palette query")
	}
}

func TestStatusScreenUnicodeTitleDoesNotPrint(t *testing.T) {
	s := newStatusScreen(task.Size{Cols: 80, Rows: 24}, task.Spec{Name: "test"}, nil)
	defer s.close()
	if err := s.feed([]byte("prompt> \x1b]0;查看5090当前训练状态 | codex\a")); err != nil {
		t.Fatal(err)
	}
	rows := vtscreen.Rows(s.e)
	if got := strings.TrimSpace(ansi.Strip(rows[0])); got != "prompt>" {
		t.Fatalf("window title leaked onto the application screen: %q", got)
	}
}

func TestStatusPaneTitleAndWindowName(t *testing.T) {
	const name = "project"
	s := newStatusScreen(task.Size{Cols: 100, Rows: 24}, task.Spec{Name: name, Command: []string{"/bin/bash"}}, nil)
	defer s.close()
	for _, part := range []string{"\x1b]2;全国", "总决赛;A\u202e", "\x1b", "\\"} {
		if err := s.feed([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	s.update(name, &task.Resources{ForegroundCommand: "codex"})
	if got := s.statusLine(100); !strings.HasPrefix(got, "[project] 0:codex*") || !strings.Contains(got, "全国总决赛;A") || strings.ContainsRune(got, '\u202e') {
		t.Fatal(got)
	}
	s.windowName = "review"
	s.update(name, &task.Resources{ForegroundCommand: "bash"})
	_ = s.feed([]byte("\x1b]0;\a"))
	if got := s.statusLine(100); !strings.HasPrefix(got, "[project] 0:review*") || strings.Contains(got, "总决赛") {
		t.Fatal(got)
	}
	// Only live notifications from the tmux-compatible application reach the host.
	wrapped := []byte("\x1bPtmux;\x1b\x1b]9;done\x07\x1b\\")
	_ = s.feed(wrapped)
	if s.controls.Len() != 0 {
		t.Fatal("replayed notification")
	}
	s.start()
	_ = s.feed(wrapped)
	_ = s.feed([]byte("\x1b]9;7;ignored\a\x1b]777;other;ignored\a"))
	if s.controls.String() != "\x1b]9;done\x1b\\" {
		t.Fatalf("notification: %q", s.controls.String())
	}
}

func TestApplicationMouseModeTakesOverFromScrollback(t *testing.T) {
	s := newStatusScreen(task.Size{Cols: 20, Rows: 4}, task.Spec{Name: "inline"}, nil)
	defer s.close()
	if err := s.feed([]byte("one\r\ntwo\r\nthree\r\nfour")); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	s.updateMouse(&out)
	wheel := []byte("\x1b[<64;4;2M")
	if got := s.mouseInput(wheel); got != nil || !s.viewport.Scrolled() {
		t.Fatal("wheel did not scroll the inline viewport")
	}
	if err := s.feed([]byte("\x1b[?1006h\x1b[?1002h")); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	s.updateMouse(&out)
	if !strings.Contains(out.String(), ansi.ResetModeMouseNormal) ||
		!strings.Contains(out.String(), ansi.SetModeMouseButtonEvent) ||
		strings.Contains(out.String(), ansi.ResetModeMouseExtSgr) {
		t.Fatalf("wrong transition to application mouse tracking: %q", out.String())
	}
	if got := s.mouseInput(wheel); string(got) != string(wheel) || s.viewport.Scrolled() {
		t.Fatal("application wheel event was intercepted")
	}
}

func TestStatusMouseLegacyEncodingAndRawInput(t *testing.T) {
	s := newStatusScreen(task.Size{Cols: 80, Rows: 24}, task.Spec{}, nil)
	defer s.close()
	if err := s.feed([]byte("\x1b[?1000h")); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	s.updateMouse(&out)
	if s.hostMouse.Encoding != ansi.ModeMouseExtSgr {
		t.Fatal("status screen did not request unambiguous mouse coordinates")
	}
	if got := string(s.mouseInput([]byte("\x1b[<0;79;23M"))); got != "\x1b[M o7" {
		t.Fatalf("legacy application click: %q", got)
	}
	if got := string(s.mouseInput([]byte("\x1b[<0;79;24m"))); got != "\x1b[M#o7" {
		t.Fatalf("legacy release outside the application: %q", got)
	}
	s.rawInput = true
	s.updateMouse(&out)
	if s.hostMouse != s.appMouse {
		t.Fatal("--no-escape changed the application's physical mouse protocol")
	}
}

func TestPixelMouseUsesCurrentWindowAndSourceDimensions(t *testing.T) {
	size := task.Size{Cols: 80, Rows: 24}
	s := newStatusScreen(size, task.Spec{}, nil)
	defer s.close()
	s.resize(size, image.Pt(800, 480), true)
	if err := s.feed([]byte("\x1b[?1002h\x1b[?1016h")); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	s.updateMouse(&out)
	if s.hostMouse.Encoding != ansi.ModeMouseExtSgrPixel {
		t.Fatal("pixel reports were replaced with cell reports")
	}
	check := func(input, expected string) {
		t.Helper()
		if got := string(s.mouseInput([]byte(input))); got != expected {
			t.Fatalf("%q: got %q, want %q", input, got, expected)
		}
	}
	check("\x1b[<65;800;460M", "\x1b[<65;800;460M")
	check("\x1b[<65;1;461M", "")
	s.source(task.Size{Cols: 40, Rows: 12})
	check("\x1b[<0;400;240M", "\x1b[<0;400;240M")
	check("\x1b[<0;799;479m", "\x1b[<0;400;240m")
	s.resize(size, image.Pt(1600, 960), false)
	check("\x1b[<65;800;480M", "\x1b[<65;800;480M")
	check("\x1b[<65;801;480M", "")
	s.resize(size, image.Point{}, false)
	check("\x1b[<65;801;480M", "\x1b[<65;801;480M")
}

func TestFollowLiveDoesNotBlockInputOnAStalledDraw(t *testing.T) {
	s := newStatusScreen(task.Size{Cols: 20, Rows: 4}, task.Spec{Name: "inline"}, nil)
	defer s.close()
	if err := s.feed([]byte("one\r\ntwo\r\nthree\r\nfour")); err != nil {
		t.Fatal(err)
	}
	s.viewport.Scroll(s.e, 1)
	if !s.viewport.Scrolled() {
		t.Fatal("fixture did not enter scrollback")
	}
	s.mu.Lock()
	s.dirty = false
	requested := make(chan struct{})
	go func() { s.followLive(); close(requested) }()
	select {
	case <-requested:
	case <-time.After(time.Second):
		s.mu.Unlock()
		t.Fatal("input waited for the screen lock")
	}
	s.applyFollow()
	s.mu.Unlock()
	if s.viewport.Scrolled() || !s.dirty {
		t.Fatal("the next draw did not resume live output")
	}
}

func TestColorModeFallback(t *testing.T) {
	for _, tc := range []struct{ hint, want string }{
		{"15;0", "dark"}, {"0;default;15", "light"}, {"0;255", "light"}, {"", "auto"}, {"0;999", "auto"},
	} {
		t.Setenv("COLORFGBG", tc.hint)
		if got := ResolveColorMode("auto", nil); got != tc.want {
			t.Fatalf("COLORFGBG=%q: got %q, want %q", tc.hint, got, tc.want)
		}
	}
}
