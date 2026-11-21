package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
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

func TestTmuxPreviewKeepsScrollPositionAndFollowsLiveName(t *testing.T) {
	m := readyModel(t)
	m.mode, m.tmux.selected = "tmux-view", "$7"
	m.width, m.height = 40, 14
	pane := "%4"
	text := "顶部" + strings.Repeat("界", 24) + " RIGHT\n" + strings.Repeat("middle\n", 30) + "BOTTOM"
	refresh := func(name, content string) {
		t.Helper()
		preview := panePreview{paneID: pane, paneName: name, textViewport: newTextViewport(content)}
		next, _ := m.updateTmux(tmuxResult{generation: m.tmux.generation, panePreview: preview})
		m = next.(model)
	}
	view := func() string { return strings.Join(m.tmuxLines(m.width-4), "\n") }
	refresh("旧名称", text)
	if !strings.Contains(view(), "BOTTOM") || strings.Contains(view(), "顶部") {
		t.Fatal("preview did not start at the bottom")
	}
	wheel := func(x, y int, button tea.MouseButton) {
		next, _ := m.Update(tea.MouseWheelMsg{X: x, Y: y, Button: button})
		m = next.(model)
	}
	bottom := m.tmux.y
	wheel(5, 1, tea.MouseWheelUp)
	if m.tmux.y != bottom || m.View().MouseMode == tea.MouseModeNone {
		t.Fatal("scrollable preview did not confine the wheel to its text region")
	}
	wheel(5, 5, tea.MouseWheelUp)
	wheel(5, 5, tea.MouseWheelRight)
	if m.tmux.y != bottom-3 || m.tmux.x != 8 {
		t.Fatal("wheel did not scroll the captured pane")
	}
	m, _ = press(m, tea.KeyHome)
	if !strings.Contains(view(), "顶部") {
		t.Fatal("Home did not reveal the first row")
	}
	for range 4 {
		m, _ = press(m, tea.KeyRight)
	}
	refresh("新名称", text+"\nnew output")
	if !strings.Contains(view(), "RIGHT") || !strings.Contains(view(), "新名称") || strings.Contains(view(), "旧名称") {
		t.Fatalf("refresh lost the viewport or name: %s", view())
	}
	m, _ = press(m, tea.KeyPgDown)
	if m.tmux.y != m.height-tmuxHeaderRows {
		t.Fatalf("Page Down moved to %d", m.tmux.y)
	}
	m, _ = press(m, tea.KeyHome)
	m, _ = press(m, tea.KeyEnd)
	refresh("新名称", text+"\nTAIL")
	if !strings.Contains(view(), "TAIL") {
		t.Fatal("End did not resume following new output")
	}
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 50})
	m = next.(model)
	if m.tmux.x != 0 || m.tmux.y != 0 || !strings.Contains(view(), "顶部") || !strings.Contains(view(), "TAIL") {
		t.Fatal("resize did not reveal the full pane")
	}
	if m.View().MouseMode != tea.MouseModeNone {
		t.Fatal("preview kept capturing the mouse after the content fit")
	}
	m.width, m.height = 40, 14
	m, _ = press(m, tea.KeyHome)
	m, _ = press(m, tea.KeyRight)
	pane = "%5"
	refresh("新名称", text+"\nNEW PANE")
	if m.tmux.x != 0 || !strings.Contains(view(), "NEW PANE") {
		t.Fatal("a different active pane inherited the previous pane's scroll position")
	}
}
