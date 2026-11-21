package tui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/raywcm/taskdock/internal/tmux"
)

type tmuxBrowser struct {
	cancel     context.CancelFunc
	sessions   []tmux.Session
	selected   string
	text       string
	err        error
	loaded     bool
	generation uint64
}
type tmuxResult struct {
	generation uint64
	sessions   []tmux.Session
	text       string
	err        error
}
type tmuxRefresh uint64

func (m *model) loadTmux() tea.Cmd {
	m.tmux.generation++
	generation := m.tmux.generation
	if m.tmux.cancel != nil {
		m.tmux.cancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.tmux.cancel = cancel
	selected := m.tmux.selected
	preview := m.mode == "tmux-view"
	return func() tea.Msg {
		server := tmux.Server{}
		result := tmuxResult{generation: generation}
		if preview {
			result.text, result.err = server.Capture(ctx, tmux.Session{ID: selected})
		} else {
			result.sessions, result.err = server.List(ctx)
		}
		return result
	}
}

func (m model) updateTmux(result tmuxResult) (tea.Model, tea.Cmd) {
	if result.generation != m.tmux.generation || (m.mode != "tmux" && m.mode != "tmux-view") {
		return m, nil
	}
	m.tmux.loaded = true
	m.tmux.err = result.err
	m.tmux.text = result.text
	if m.mode == "tmux" {
		m.tmux.sessions = result.sessions
		found := false
		for _, s := range result.sessions {
			if s.ID == m.tmux.selected {
				found = true
				break
			}
		}
		if !found {
			m.tmux.selected = ""
			if len(result.sessions) > 0 {
				m.tmux.selected = result.sessions[0].ID
			}
		}
	}
	return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return tmuxRefresh(result.generation) })
}

func (m model) tmuxKey(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "ctrl+c":
		return m.quit()
	case "q", "esc":
		if m.tmux.cancel != nil {
			m.tmux.cancel()
		}
		m.tmux.generation++
		if m.mode == "tmux-view" {
			m.mode = "tmux"
			return m, m.loadTmux()
		}
		m.mode = ""
		return m, nil
	case "f5", "r":
		return m, m.loadTmux()
	case "enter":
		if m.mode == "tmux" && m.tmux.selected != "" {
			m.mode = "tmux-view"
			m.tmux.text = ""
			m.tmux.loaded = false
			m.tmux.err = nil
			return m, m.loadTmux()
		}
	case "up", "k", "down", "j":
		if m.mode == "tmux" {
			for i, s := range m.tmux.sessions {
				if s.ID != m.tmux.selected {
					continue
				}
				if k == "up" || k == "k" {
					i = max(0, i-1)
				} else {
					i = min(len(m.tmux.sessions)-1, i+1)
				}
				m.tmux.selected = m.tmux.sessions[i].ID
				break
			}
		}
	}
	return m, nil
}

func (m model) tmuxLines(width int) []string {
	t := m.locale().Text
	lines := []string{"tmux  ·  " + t("mode_readonly"), "↑↓  Enter  F5  Esc", ""}
	if m.mode == "tmux-view" {
		lines[1] = "F5  Esc"
		for _, session := range m.tmux.sessions {
			if session.ID == m.tmux.selected {
				lines[0] += "  ·  " + safe(session.Name)
				break
			}
		}
	}
	if m.tmux.err != nil {
		return append(lines, safe(m.tmux.err.Error()))
	}
	if !m.tmux.loaded {
		return append(lines, t("loading"))
	}
	if m.mode == "tmux-view" {
		rows := strings.Split(strings.TrimRight(m.tmux.text, "\n"), "\n")
		available := max(1, m.height-len(lines))
		return append(lines, rows[max(0, len(rows)-available):]...)
	}
	if len(m.tmux.sessions) == 0 {
		return append(lines, t("empty_running"))
	}
	start := 0
	for i, s := range m.tmux.sessions {
		if s.ID == m.tmux.selected {
			start = max(0, i-max(1, m.height-len(lines))+1)
			break
		}
	}
	for _, s := range m.tmux.sessions[start:] {
		marker := "  "
		if s.ID == m.tmux.selected {
			marker = "› "
		}
		lines = append(lines, ansi.Truncate(marker+safe(s.Name)+"  "+s.ID, width, "…"))
	}
	return lines
}
