package terminal

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/raywcm/taskdock/internal/i18n"
	"github.com/raywcm/taskdock/internal/task"
)

func TestSessionIdentityShowsNameShortIDAndLocalizedHint(t *testing.T) {
	spec := task.Spec{Name: "代码会话", ID: "ff709424d117a8b1564b3eb13e7b6580"}
	got := newIdentity(spec, i18n.New("zh-CN"), task.Size{Cols: 100, Rows: 24}, false)
	if got.title != "TaskDock · 代码会话 [ff709424]" || strings.Contains(got.banner(), string(spec.ID)) {
		t.Fatalf("incorrect identity: %+v", got)
	}
	if !strings.Contains(got.banner(), "代码会话 [ff709424]") || !strings.Contains(got.banner(), "Ctrl+Q") || !strings.Contains(got.banner(), "断开") {
		t.Fatal(got.banner())
	}
	if !strings.HasPrefix(got.start(), saveWindowTitle+"\x1b]2;") {
		t.Fatal("title must be saved before setting it")
	}
	noEscape := newIdentity(spec, i18n.New("en"), task.Size{Cols: 100, Rows: 24}, true)
	if strings.Contains(noEscape.banner(), "F12") || strings.Contains(noEscape.banner(), "Ctrl+Q") || strings.Contains(noEscape.banner(), "Ctrl+]") || !strings.Contains(noEscape.banner(), "Close terminal") {
		t.Fatal("advertised unavailable detach shortcut", noEscape.banner())
	}
}

func TestSessionIdentityBoundsCJKNoticeAndStripsControls(t *testing.T) {
	spec := task.Spec{Name: strings.Repeat("项目", 30) + "\x1b]52;c;injected\x07\r\n", ID: "ff709424d117a8b1564b3eb13e7b6580"}
	for _, width := range []uint16{1, 16, 32, 80} {
		got := newIdentity(spec, i18n.New("en"), task.Size{Cols: width, Rows: 24}, false)
		if strings.ContainsAny(got.title, "\x1b\x07\r\n") {
			t.Fatal("control sequence in title", got.title)
		}
		if ansi.StringWidth(got.line) > max(1, int(width)-1) {
			t.Fatalf("banner wraps %d columns: %q", width, got.line)
		}
		if width >= 32 && !strings.Contains(got.line, "ff709424") {
			t.Fatal("long name hid short ID", got.line)
		}
	}
}
