package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/raywcm/taskdock/internal/ipc"
	"github.com/raywcm/taskdock/internal/monitor"
	"github.com/raywcm/taskdock/internal/task"
)

type monitorTick struct{ generation uint64 }
type monitorOpened struct {
	generation uint64
	session    *monitor.Session
	err        error
}

func monitorTickCmd(generation uint64) tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return monitorTick{generation: generation} })
}
func (m model) monitorGeometry() (left, right, height int) {
	width := max(1, m.width-4)
	left = min(28, max(16, width/4))
	right = max(1, width-left-3)
	height = max(1, m.height-7)
	return
}
func (m model) monitorSize() task.Size {
	_, width, height := m.monitorGeometry()
	return task.Size{Rows: uint16(min(256, max(4, height))), Cols: uint16(min(512, max(20, width)))}
}
func (m model) appArea() uv.Rectangle {
	if m.tab == tabRunning && m.monitoring && m.width >= 50 && m.height >= 12 {
		left, right, height := m.monitorGeometry()
		return uv.Rect(left+5, 5, right, height)
	}
	return uv.Rectangle{}
}
func (m *model) closeMonitor() {
	if m.monitorInput != nil {
		m.monitorInput.Disable()
	}
	m.releaseMonitor()
}
func (m *model) releaseMonitor() {
	if m.monitorCancel != nil {
		m.monitorCancel()
		m.monitorCancel = nil
	}
	if m.monitorSession != nil {
		m.monitorSession.Close()
		m.monitorSession = nil
	}
	m.monitorID = ""
	m.monitorGeneration++
	m.monitorFrame = monitor.Frame{}
}
func (m *model) syncMonitor() tea.Cmd {
	if !m.monitoring || m.tab != tabRunning || m.mode == "search" {
		return nil
	}
	r, ok := m.current()
	if !ok || m.width < 50 || m.height < 12 {
		m.closeMonitor()
		return nil
	}
	if r.Spec.ID == m.monitorID {
		return nil
	}
	if m.monitorInput != nil {
		m.monitorInput.Begin()
	}
	m.releaseMonitor()
	m.monitorID = r.Spec.ID
	ctx, cancel := context.WithCancel(m.ctx)
	m.monitorCancel = cancel
	generation := m.monitorGeneration
	size := m.monitorSize()
	catalog := m.catalog
	return func() tea.Msg {
		session, err := catalog.Monitor(ctx, r.Spec.ID, size)
		if ctx.Err() != nil && session != nil {
			session.Close()
			session = nil
		}
		return monitorOpened{generation: generation, session: session, err: err}
	}
}
func (m model) monitorView(width int) []string {
	t, p := m.locale().Text, m.palette()
	if m.width < 50 || m.height < 12 {
		return []string{m.header(width), t("monitor"), t("small_window")}
	}
	left, right, height := m.monitorGeometry()
	rows := m.rows(tabRunning)
	title := t("monitor_select")
	if r, ok := m.current(); ok {
		title = safe(r.Spec.Name) + " [" + m.shortID(r.Spec.ID) + "]"
		if m.monitorFrame.Err == nil {
			title += " · " + t("monitor_control")
		}
	}
	position := 0
	if len(rows) > 0 {
		position = m.cursors[0] + 1
	}
	label := fmt.Sprintf("%s %d / %d", t("session"), position, len(rows))
	lines := []string{m.header(width), "", m.tabs(width), p.muted.Render(strings.Repeat("─", width)), fit(label, left) + " │ " + ansi.Truncate(p.accent.Bold(true).Render(title), right, "…")}
	start := max(0, m.cursors[0]-height+1)
	preview := m.monitorFrame
	for y := 0; y < height; y++ {
		label := ""
		if i := start + y; i < len(rows) {
			label = "● " + safe(rows[i].Spec.Name)
			if !p.terminal {
				label = fit(label, left)
			}
			label = p.choice(label, i == m.cursors[0])
		}
		content := ""
		switch {
		case preview.Err != nil:
			if y == height/2 {
				content = t("monitor_unavailable")
			}
			if y == height/2+1 {
				var remote *ipc.RemoteError
				if errors.Is(preview.Err, monitor.ErrEmulation) {
					content = t("monitor_parser_error")
				} else if errors.As(preview.Err, &remote) && remote.Code == "busy" {
					content = t("monitor_busy")
				} else {
					content = safe(preview.Err.Error())
				}
			}
		case !preview.Ready:
			if y == height/2 {
				if len(rows) == 0 {
					content = t("empty_running")
				} else {
					content = t("loading")
				}
			}
		case y < len(preview.Rows):
			content = preview.Rows[y]
		}
		lines = append(lines, fit(label, left)+" │ "+fit(ansi.Truncate(content, right, ""), right))
	}
	lines = append(lines, m.status(width), "")
	return lines
}
