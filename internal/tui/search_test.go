package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/raywcm/taskdock/internal/task"
)

func TestSearchLargeListFiltersAllTabsAndScrolls(t *testing.T) {
	m := readyModel(t)
	m.tasks = nil
	for i := range 2000 {
		state := task.Running
		if i%2 != 0 {
			state = task.Exited
		}
		m.tasks = append(m.tasks, record(t, fmt.Sprintf("Worker-%04d 中文", i), state))
	}
	m.openSearch()
	m.input.SetValue("worker-1998 中文")
	rows := m.searchRows()
	if len(rows) != 1 || rows[0].Spec.ID != m.tasks[1998].Spec.ID {
		t.Fatal("case-insensitive terms did not match running records")
	}
	m.input.SetValue(string(m.tasks[1728].Spec.ID)[8:])
	if rows := m.searchRows(); len(rows) != 1 || rows[0].Spec.ID != m.tasks[1728].Spec.ID {
		t.Fatal("ID search failed")
	}
	m.input.SetValue("")
	next, _ := m.updateSearch(tea.KeyPressMsg{Code: tea.KeyEnd, Mod: tea.ModCtrl})
	m = next.(model)
	if m.searchCursor != 999 || !strings.Contains(ansi.Strip(m.View().Content), "Worker-1998") {
		t.Fatal("large result list did not scroll to its last row")
	}
	for _, size := range [][2]int{{80, 24}, {120, 30}, {40, 12}} {
		m.width, m.height = size[0], size[1]
		view := m.View().Content
		if len(strings.Split(view, "\n")) > m.height {
			t.Fatal("search exceeded viewport height")
		}
		for _, row := range strings.Split(view, "\n") {
			if ansi.StringWidth(row) > m.width {
				t.Fatal("search exceeded viewport width")
			}
		}
	}
}

func TestSearchSelectionAndCancel(t *testing.T) {
	m := readyModel(t)
	m.openSearch()
	m.input.SetValue("not-found")
	next, _ := m.updateSearch(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	if m.mode != "search" || m.monitoring || m.selected.Kind != "" {
		t.Fatal("empty search changed the session")
	}
	m.input.SetValue("other")
	m.monitoring = true
	next, cmd := m.updateSearch(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	if !m.monitoring || m.monitorID != m.tasks[1].Spec.ID || cmd == nil || m.mode != "" {
		t.Fatal("Enter did not control the matching session")
	}
	defer m.closeMonitor()
	m.openSearch()
	next, _ = m.updateSearch(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = next.(model)
	if m.mode != "" || !m.monitoring || m.monitorID != m.tasks[1].Spec.ID {
		t.Fatal("cancel changed the monitored session")
	}
}

func TestEmptyMonitorCanReturnToList(t *testing.T) {
	m := readyModel(t)
	m.tasks = nil
	m.monitorInput = &monitorInput{}
	m, _ = press(m, 'v')
	m, _ = press(m, tea.KeyF12)
	if m.monitoring || m.monitorInput.enabled {
		t.Fatal("empty monitor captured the manager keyboard")
	}
}

func TestSearchFromListUsesEnterToAttachAndIgnoresCtrlEnter(t *testing.T) {
	m := readyModel(t)
	m.openSearch()
	m.input.SetValue("other")
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModCtrl})
	m = next.(model)
	if m.mode != "search" || m.selected.Kind != "" || m.input.Value() != "other" {
		t.Fatal("Ctrl+Enter changed search or entered a session")
	}
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	if m.selected.Kind != "attach" || m.selected.ID != m.tasks[1].Spec.ID || m.monitoring {
		t.Fatal("search Enter did not follow list attach behavior")
	}
}

func TestRefreshPreservesSearchAndEndsMissingMonitor(t *testing.T) {
	m := readyModel(t)
	target := m.tasks[1].Spec.ID
	m.openSearch()
	m, _ = press(m, tea.KeyDown)
	n, _ := m.Update(loaded{tasks: []task.Record{m.tasks[1], m.tasks[0]}, generation: m.generation})
	m = n.(model)
	m, _ = press(m, tea.KeyEnter)
	if m.selected.ID != target {
		t.Errorf("search attached to %s after reorder, want %s", m.selected.ID, target)
	}
	m = readyModel(t)
	m, _ = press(m, 'v')
	n, _ = m.Update(loaded{tasks: m.tasks[1:], generation: m.generation})
	m = n.(model)
	if m.monitoring || m.monitorID != "" {
		t.Fatalf("monitor followed another session %s", m.monitorID)
	}
}

func TestTinyWindowBlocksHiddenActionsAndFitsViewport(t *testing.T) {
	m := readyModel(t)
	m, _ = press(m, 's')
	n, _ := m.Update(tea.WindowSizeMsg{Width: 1, Height: 2})
	m = n.(model)
	m, cmd := press(m, 'y')
	if m.busy || m.mode != "stop" {
		t.Fatal("acted on an invisible confirmation")
	}
	noAction(t, cmd)
	for _, line := range strings.Split(m.View().Content, "\n") {
		if ansi.StringWidth(line) > 1 {
			t.Fatalf("one-column window overflow: %q", line)
		}
	}
	m, _ = press(m, 'q')
	if !m.quitting {
		t.Fatal("advertised quit key did not exit")
	}
}
