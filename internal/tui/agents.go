package tui

import (
	"context"
	"errors"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/raywcm/taskdock/internal/monitor"
	"github.com/raywcm/taskdock/internal/task"
)

type agentsPane struct {
	session    *monitor.Session
	cancel     context.CancelFunc
	frame      monitor.Frame
	generation uint64
	control    bool
}
type agentsOpened struct {
	generation uint64
	session    *monitor.Session
	err        error
}
type agentsTick uint64

func (m model) agentsTop() int {
	if m.height < 12 {
		return 3
	}
	return 4
}
func (m model) agentsSize() task.Size {
	return task.Size{Cols: uint16(min(512, max(1, m.width-4))), Rows: uint16(min(256, max(1, m.height-m.agentsTop()-1)))}
}
func (m *model) closeAgents() {
	if m.monitorInput != nil {
		m.monitorInput.Disable()
	}
	if m.agents.cancel != nil {
		m.agents.cancel()
	}
	if m.agents.session != nil {
		m.agents.session.Close()
	}
	generation := m.agents.generation + 1
	m.agents = agentsPane{generation: generation}
}
func (m *model) openAgents() tea.Cmd {
	m.closeAgents()
	ctx, cancel := context.WithCancel(m.ctx)
	m.agents.cancel = cancel
	generation := m.agents.generation
	size := m.agentsSize()
	catalog := m.catalog
	executable := m.agentctlPath
	return func() tea.Msg {
		launcher, ok := catalog.(interface {
			EnsureAgents(context.Context, string, task.Size) (task.Record, error)
		})
		if !ok {
			return agentsOpened{generation: generation, err: errors.New("Agents backend unavailable")}
		}
		record, err := launcher.EnsureAgents(ctx, executable, size)
		if err != nil {
			return agentsOpened{generation: generation, err: err}
		}
		session, err := catalog.Monitor(ctx, record.Spec.ID, size)
		if ctx.Err() != nil && session != nil {
			session.Close()
			session = nil
		}
		return agentsOpened{generation: generation, session: session, err: err}
	}
}
func agentsTickCmd(generation uint64) tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return agentsTick(generation) })
}
func (m model) agentsView(width int) []string {
	t, p := m.locale().Text, m.palette()
	lines := []string{m.header(width)}
	if m.agentsTop() == 4 {
		lines = append(lines, "")
	}
	lines = append(lines, m.tabs(width), p.muted.Render(strings.Repeat("─", width)))
	height := int(m.agentsSize().Rows)
	for y := 0; y < height; y++ {
		text := ""
		switch {
		case m.agents.frame.Err != nil:
			if y == height/2 {
				text = t("agents_unavailable") + " " + safe(m.agents.frame.Err.Error())
			}
		case !m.agents.frame.Ready:
			if y == height/2 {
				text = t("loading")
			}
		case y < len(m.agents.frame.Rows):
			text = m.agents.frame.Rows[y]
		}
		lines = append(lines, fit(ansi.Truncate(text, width, ""), width))
	}
	hint := t("agents_preview_hint")
	if m.agents.control {
		hint = t("agents_control_hint")
	}
	return append(lines, p.muted.Render(ansi.Truncate(hint, width, "…")))
}
