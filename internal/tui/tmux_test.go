package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/raywcm/taskdock/internal/tmux"
)

func TestTmuxListDoesNotOpenStaleRowsOrRenderInvisibleRows(t *testing.T) {
	m := readyModel(t)
	m.mode = "tmux"
	for i := range 1000 {
		m.tmux.sessions = append(m.tmux.sessions, tmux.Session{ID: fmt.Sprintf("$%d", i), Name: fmt.Sprintf("session-%d", i)})
	}
	m.tmux.selected = "$700"
	n, _ := m.tmuxKey("enter")
	if n.(model).mode != "tmux" {
		t.Fatal("opened a stale selection while loading")
	}
	m.tmux.loaded = true
	rows := m.tmuxLines(60)
	if len(rows) > m.height || !strings.Contains(strings.Join(rows, "\n"), "› session-700") {
		t.Fatalf("list did not fit selection: %d rows", len(rows))
	}
	m.tmux.sessions = nil
	if strings.Contains(strings.Join(m.tmuxLines(60), "\n"), "Press n") {
		t.Fatal("empty browser advertised an unavailable command")
	}
}
