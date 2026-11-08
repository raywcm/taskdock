package terminal

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/raywcm/taskdock/internal/protocol"
	"github.com/raywcm/taskdock/internal/task"
	"github.com/raywcm/taskdock/internal/vtscreen"
)

func TestStatusLineBoundsAndSanitizesMetadata(t *testing.T) {
	for _, width := range []int{1, 8, 20, 80, 160} {
		line := statusLine(strings.Repeat("项目", 35)+"\x1b\r\n", "1023 MiB", width)
		if ansi.StringWidth(line) > width || strings.ContainsAny(line, "\x1b\r\n") {
			t.Fatalf("invalid row at width %d: %q", width, line)
		}
		if width >= 20 && !strings.Contains(line, "RSS 1023 MiB") {
			t.Fatal("name hid memory", line)
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
