package tui

import (
	"context"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/raywcm/taskdock/internal/tmux"
)

type panePreview struct {
	textViewport
	paneID, paneName string
}

type tmuxBrowser struct {
	panePreview
	cancel     context.CancelFunc
	sessions   []tmux.Session
	selected   string
	err        error
	loaded     bool
	generation uint64
}
type tmuxResult struct {
	panePreview
	generation uint64
	sessions   []tmux.Session
	err        error
}
type tmuxRefresh uint64

const tmuxHeaderRows = 3

func (b tmuxBrowser) selectedIndex() int {
	return slices.IndexFunc(b.sessions, func(s tmux.Session) bool { return s.ID == b.selected })
}

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
			pane, err := server.Capture(ctx, tmux.Session{ID: selected})
			result.err = err
			if err == nil {
				result.panePreview = panePreview{
					paneID: pane.ID, paneName: pane.SessionName,
					textViewport: newTextViewport(strings.TrimRight(pane.Text, "\n")),
				}
			}
			// A failed capture can be transient. Leave the preview only if the
			// server confirms the selected session has actually disappeared.
			if result.err != nil && ctx.Err() == nil {
				sessions, err := server.List(ctx)
				if err == nil && !slices.ContainsFunc(sessions, func(s tmux.Session) bool { return s.ID == selected }) {
					result.sessions = sessions
					result.err = nil
				}
			}
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
	if result.sessions != nil {
		m.mode = "tmux"
	}
	m.tmux.loaded = true
	m.tmux.err = result.err
	if m.mode == "tmux" {
		m.tmux.panePreview = panePreview{}
		m.tmux.sessions = result.sessions
		if m.tmux.selectedIndex() < 0 {
			m.tmux.selected = ""
			if len(result.sessions) > 0 {
				m.tmux.selected = result.sessions[0].ID
			}
		}
	} else if result.err == nil {
		height := max(1, m.height-tmuxHeaderRows)
		result.y = result.bottom(height)
		if m.tmux.paneID == result.paneID {
			result.x = m.tmux.x
			if m.tmux.y != m.tmux.bottom(height) {
				result.y = m.tmux.y
			}
		}
		result.clamp(m.width-4, height)
		m.tmux.panePreview = result.panePreview
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
			m.tmux.loaded = false
			m.tmux.err = nil
			return m, m.loadTmux()
		}
		m.mode = ""
		return m, nil
	case "f5", "r":
		return m, m.loadTmux()
	case "enter":
		if m.mode == "tmux" && m.tmux.loaded && m.tmux.selected != "" {
			m.mode = "tmux-view"
			m.tmux.loaded = false
			m.tmux.err = nil
			return m, m.loadTmux()
		}
	case "up", "down", "k", "j", "pgup", "pgdown", "home", "end", "left", "right", "h", "l":
		height := max(1, m.height-tmuxHeaderRows)
		if m.mode == "tmux-view" {
			m.tmux.scroll(k, m.width-4, height)
		} else if i := m.tmux.selectedIndex(); i >= 0 {
			i = scrollRow(k, i, height, len(m.tmux.sessions)-1)
			m.tmux.selected = m.tmux.sessions[i].ID
		}
	}
	return m, nil
}

func (m model) tmuxLines(width int) []string {
	t := m.locale().Text
	lines := []string{"tmux  ·  " + t("mode_readonly"), "↑↓  PgUp/PgDn  Enter  F5  Esc", ""}
	if m.mode == "tmux-view" {
		lines[1] = "↑↓ ←→  PgUp/PgDn  Home/End  F5  Esc"
		if m.tmux.paneName != "" {
			lines[0] += "  ·  " + safe(m.tmux.paneName)
		}
	}
	if m.tmux.err != nil {
		return append(lines, safe(m.tmux.err.Error()))
	}
	if !m.tmux.loaded {
		return append(lines, t("loading"))
	}
	available := max(1, m.height-len(lines))
	if m.mode == "tmux-view" {
		start := min(m.tmux.y, m.tmux.bottom(available))
		for _, line := range m.tmux.lines[start:min(len(m.tmux.lines), start+available)] {
			lines = append(lines, ansi.Cut(line, m.tmux.x, m.tmux.x+width))
		}
		return lines
	}
	if len(m.tmux.sessions) == 0 {
		return append(lines, t("empty_tmux"))
	}
	start := max(0, m.tmux.selectedIndex()-available+1)
	p := m.palette()
	for _, s := range m.tmux.sessions[start:min(len(m.tmux.sessions), start+available)] {
		label := p.arrow(safe(s.Name)+"  "+s.ID, s.ID == m.tmux.selected)
		lines = append(lines, ansi.Truncate(label, width, "…"))
	}
	return lines
}
