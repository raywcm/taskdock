package tui

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/raywcm/taskdock/internal/task"
)

type historyState struct {
	id             task.ID
	name           string
	lines          []string
	x, y, maxWidth int
	ready          bool
	err            error
	generation     uint64
	cancel         context.CancelFunc
}
type historyLoaded struct {
	generation uint64
	lines      []string
	maxWidth   int
	err        error
}

func (m model) historyHeight() int { return max(1, m.height-5) }
func (m *model) closeHistory() {
	if m.history.cancel != nil {
		m.history.cancel()
	}
	m.history = historyState{generation: m.history.generation + 1}
}
func (m *model) openHistory(record task.Record) tea.Cmd {
	m.closeHistory()
	m.mode = "history"
	ctx, cancel := context.WithCancel(m.ctx)
	m.history.cancel = cancel
	m.history.id = record.Spec.ID
	m.history.name = record.Spec.Name
	generation := m.history.generation
	catalog := m.catalog
	id := record.Spec.ID
	return func() tea.Msg {
		data, err := catalog.Logs(ctx, string(id), false, 0)
		text := strings.ReplaceAll(strings.ReplaceAll(string(data), "\r\n", "\n"), "\r", "\n")
		text = strings.ReplaceAll(text, "\t", "    ")
		text = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) && r != '\n' {
				return -1
			}
			return r
		}, text)
		var lines []string
		width := 0
		if text != "" {
			lines = strings.Split(strings.TrimSuffix(text, "\n"), "\n")
			for _, line := range lines {
				width = max(width, ansi.StringWidth(line))
			}
		}
		return historyLoaded{generation: generation, lines: lines, maxWidth: width, err: err}
	}
}
func (m model) historyKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "q", "enter":
		m.mode = ""
		m.closeHistory()
		return m, tea.ClearScreen
	case "up", "k":
		m.history.y--
	case "down", "j":
		m.history.y++
	case "pgup":
		m.history.y -= m.historyHeight()
	case "pgdown":
		m.history.y += m.historyHeight()
	case "home":
		m.history.y = 0
		m.history.x = 0
	case "end":
		m.history.y = max(0, len(m.history.lines)-m.historyHeight())
	case "left", "h":
		m.history.x -= 8
	case "right", "l":
		m.history.x += 8
	}
	m.history.y = max(0, min(m.history.y, max(0, len(m.history.lines)-m.historyHeight())))
	m.history.x = max(0, min(m.history.x, max(0, m.history.maxWidth-max(1, m.width-4))))
	return m, nil
}
func (m model) historyView(width int) []string {
	t, p := m.locale().Text, m.palette()
	title := t("output_history") + " · " + safe(m.history.name) + " [" + m.shortID(m.history.id) + "]"
	lines := []string{m.header(width), p.accent.Bold(true).Render(ansi.Truncate(title, width, "…")), p.muted.Render(strings.Repeat("─", width))}
	height := m.historyHeight()
	y := max(0, min(m.history.y, max(0, len(m.history.lines)-height)))
	for row := 0; row < height; row++ {
		text := ""
		switch {
		case m.history.err != nil:
			if row == 0 {
				text = t("error") + ": " + safe(m.history.err.Error())
			}
		case !m.history.ready:
			if row == 0 {
				text = t("loading")
			}
		case len(m.history.lines) == 0:
			if row == 0 {
				text = t("history_empty")
			}
		case y+row < len(m.history.lines):
			text = ansi.Cut(m.history.lines[y+row], m.history.x, m.history.x+width)
		}
		lines = append(lines, fit(ansi.Truncate(text, width, ""), width))
	}
	position := "0 / 0"
	if len(m.history.lines) > 0 {
		position = fmt.Sprintf("%d–%d / %d", y+1, min(y+height, len(m.history.lines)), len(m.history.lines))
	}
	return append(lines, p.muted.Render(position), "")
}
