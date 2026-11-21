package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/raywcm/taskdock/internal/vtscreen"
)

// textViewport scrolls captured text in terminal cells, including wide characters.
type textViewport struct {
	lines          []string
	x, y, maxWidth int
}

const horizontalScrollStep = 8

func newTextViewport(text string) textViewport {
	var view textViewport
	if text != "" {
		view.lines = strings.Split(strings.TrimSuffix(text, "\n"), "\n")
		for _, line := range view.lines {
			view.maxWidth = max(view.maxWidth, ansi.StringWidth(line))
		}
	}
	return view
}

func (v *textViewport) bottom(height int) int { return max(0, len(v.lines)-max(1, height)) }

func (v *textViewport) clamp(width, height int) {
	v.y = max(0, min(v.y, v.bottom(height)))
	v.x = max(0, min(v.x, v.maxWidth-max(1, width)))
}

func (v *textViewport) resize(width, oldHeight, height int) {
	if v.y == v.bottom(oldHeight) {
		v.y = v.bottom(height)
	}
	v.clamp(width, height)
}

// Only capture mouse events for a visible read-only pane with hidden content.
func (m *model) scrollableText() (*textViewport, uv.Rectangle) {
	var view *textViewport
	var height int
	switch {
	case m.mode == "history" && m.history.ready && m.history.err == nil:
		view, height = &m.history.textViewport, m.historyHeight()
	case m.mode == "tmux-view" && m.tmux.loaded && m.tmux.err == nil:
		view, height = &m.tmux.textViewport, m.height-tmuxHeaderRows
	}
	width := m.width - 4
	if view != nil && !m.tooSmall() && (view.maxWidth > width || len(view.lines) > height) {
		return view, uv.Rect(2, 3, width, height)
	}
	return nil, uv.Rectangle{}
}

func (v *textViewport) wheel(msg tea.MouseWheelMsg, area uv.Rectangle) {
	if !uv.Pos(msg.X, msg.Y).In(area) {
		return
	}
	switch msg.Button {
	case tea.MouseWheelLeft:
		v.x -= horizontalScrollStep
	case tea.MouseWheelRight:
		v.x += horizontalScrollStep
	default:
		v.y -= vtscreen.WheelLines(msg.Button)
	}
	v.clamp(area.Dx(), area.Dy())
}

func scrollRow(key string, row, pageSize, last int) int {
	switch key {
	case "up", "k":
		row--
	case "down", "j":
		row++
	case "pgup":
		row -= pageSize
	case "pgdown":
		row += pageSize
	case "home":
		row = 0
	case "end":
		row = last
	}
	return max(0, min(row, last))
}

func (v *textViewport) scroll(key string, width, height int) {
	v.y = scrollRow(key, v.y, height, v.bottom(height))
	switch key {
	case "home":
		v.x = 0
	case "left", "h":
		v.x -= horizontalScrollStep
	case "right", "l":
		v.x += horizontalScrollStep
	}
	v.x = max(0, min(v.x, v.maxWidth-max(1, width)))
}
