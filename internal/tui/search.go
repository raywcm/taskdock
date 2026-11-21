package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/raywcm/taskdock/internal/task"
)

func (m *model) openSearch() tea.Cmd {
	if m.monitorInput != nil {
		m.monitorInput.Disable()
	}
	m.mode = "search"
	m.searchCursor = 0
	m.input.SetValue("")
	m.input.SetWidth(max(4, min(68, m.width-12)))
	return m.input.Focus()
}

func (m model) searchRows() []task.Record {
	query := strings.Fields(strings.ToLower(m.input.Value()))
	rows := make([]task.Record, 0, len(m.tasks))
	for _, r := range m.tasks {
		text := strings.ToLower(r.Spec.Name + " " + string(r.Spec.ID) + " " + r.Spec.Dir + " " + strings.Join(r.Spec.Command, " "))
		matches := true
		for _, word := range query {
			if !strings.Contains(text, word) {
				matches = false
				break
			}
		}
		if matches {
			rows = append(rows, r)
		}
	}
	return rows
}

func (m *model) resumeMonitor() tea.Cmd {
	if m.monitoring && m.monitorInput != nil && m.monitorSession != nil && m.monitorFrame.Err == nil {
		m.monitorInput.Begin()
		m.monitorInput.SetTarget(m.monitorSession)
	}
	return m.syncMonitor()
}

func (m model) updateSearch(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		rows := m.searchRows()
		last := max(0, len(rows)-1)
		m.searchCursor = min(m.searchCursor, last)
		switch key.String() {
		case "ctrl+c":
			return m.quit()
		case "esc", "ctrl+f":
			m.mode = ""
			m.input.Blur()
			return m, m.resumeMonitor()
		case "up", "down", "pgup", "pgdown", "ctrl+home", "ctrl+end":
			m.searchCursor = scrollRow(strings.TrimPrefix(key.String(), "ctrl+"), m.searchCursor, max(1, m.height-8), last)
			return m, nil
		case "ctrl+enter":
			return m, nil
		case "enter":
			if len(rows) == 0 {
				return m, nil
			}
			r := rows[m.searchCursor]
			m.mode = ""
			m.input.Blur()
			m.tab = tabStop
			if active(r) {
				m.tab = tabRunning
			}
			m.lastTaskTab = m.tab
			for i, row := range m.rows(m.tab) {
				if row.Spec.ID == r.Spec.ID {
					m.cursors[m.tab] = i
					break
				}
			}
			if !active(r) {
				m.monitoring = false
				m.closeMonitor()
				return m, m.openHistory(r)
			}
			if !m.monitoring {
				m.selected = Selection{Kind: "attach", ID: r.Spec.ID, State: m.state()}
				return m.quit()
			}
			if r.Spec.ID == m.monitorID {
				return m, m.resumeMonitor()
			}
			return m, m.syncMonitor()
		}
	}
	before := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if m.input.Value() != before {
		m.searchCursor = 0
	}
	return m, cmd
}

// Only activation typeahead has already left the raw reader. Normal search
// input uses Bubble Tea's existing decoder and text editor.
func (m model) searchTypeahead(raw []byte, initial tea.Cmd) (tea.Model, tea.Cmd) {
	commands := []tea.Cmd{initial}
	var decoder uv.EventDecoder
	for len(raw) > 0 {
		if m.mode != "search" {
			if m.monitorInput != nil && m.monitoring {
				m.monitorInput.forward(raw)
			}
			break
		}
		n, event := decoder.Decode(raw)
		if n <= 0 {
			break
		}
		raw = raw[n:]
		if key, ok := event.(uv.KeyPressEvent); ok {
			next, cmd := m.updateSearch(tea.KeyPressMsg(key))
			m = next.(model)
			commands = append(commands, cmd)
		}
	}
	return m, tea.Batch(commands...)
}

func (m model) searchView(width int) []string {
	t, p := m.locale().Text, m.palette()
	rows := m.searchRows()
	count := len(rows)
	cursor := min(m.searchCursor, max(0, count-1))
	lines := []string{m.header(width), "", p.accent.Bold(true).Render(t("search_sessions")), m.input.View(), p.muted.Render(strings.Repeat("─", width))}
	height := max(1, m.height-7)
	start := max(0, cursor-height+1)
	for i := start; i < min(count, start+height); i++ {
		r := rows[i]
		state := t("stop")
		if active(r) {
			state = t("running")
		}
		label := fmt.Sprintf("%s [%s] · %s", safe(r.Spec.Name), m.shortID(r.Spec.ID), state)
		label = ansi.Truncate(label, max(1, width-4), "…")
		if i == cursor && !p.terminal {
			label = p.accent.Render(label)
		}
		lines = append(lines, p.listChoice(label, i == cursor))
	}
	if count == 0 {
		lines = append(lines, t("search_empty"))
	}
	for len(lines) < m.height-2 {
		lines = append(lines, "")
	}
	position := 0
	if count > 0 {
		position = cursor + 1
	}
	lines = append(lines, p.muted.Render(fmt.Sprintf("%d / %d · %d %s", position, count, len(m.tasks), t("sessions"))), "")
	return lines
}
