package vtscreen

import (
	"bytes"
	"slices"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

// Viewport freezes the retained lines while the user reads earlier output.
// The emulator keeps parsing live output so returning to the bottom is instant.
type Viewport struct {
	history []uv.Line
	live    []string
	offset  int
}

func (v *Viewport) Reset()         { *v = Viewport{} }
func (v *Viewport) Scrolled() bool { return v != nil && v.offset > 0 }

func (v *Viewport) Scroll(e *vt.Emulator, lines int) {
	if lines == 0 {
		return
	}
	if !v.Scrolled() && lines > 0 && e.ScrollbackLen() > 0 {
		// The emulator clones the cells it adds to scrollback. Copy the line
		// headers before the emulator rotates its buffer.
		v.history = slices.Clone(e.Scrollback().Lines())
		v.live = Rows(e)
	}
	v.offset = min(len(v.history), max(0, v.offset+lines))
	if !v.Scrolled() {
		v.Reset()
	}
}

func (v *Viewport) Rows(e *vt.Emulator) []string {
	if !v.Scrolled() {
		return Rows(e)
	}
	rows := make([]string, len(v.live))
	for row := range rows {
		y := len(v.history) - v.offset + row
		if y >= len(v.history) {
			rows[row] = v.live[y-len(v.history)]
			continue
		}
		line := v.history[y]
		rows[row] = renderRow(min(e.Width(), len(line)), func(x int) *uv.Cell { return &line[x] })
	}
	return rows
}

// MouseModes tracks the application's requested reporting and encoding.
// Each group has mutually exclusive modes on the host terminal.
type MouseModes struct {
	Tracking ansi.DECMode
	Encoding ansi.DECMode
}

func (m *MouseModes) Set(mode ansi.Mode, enabled bool) bool {
	var target *ansi.DECMode
	switch mode {
	case ansi.ModeMouseX10, ansi.ModeMouseNormal, ansi.ModeMouseHighlight, ansi.ModeMouseButtonEvent, ansi.ModeMouseAnyEvent:
		target = &m.Tracking
	case ansi.ModeMouseExtUtf8, ansi.ModeMouseExtSgr, ansi.ModeMouseExtUrxvt, ansi.ModeMouseExtSgrPixel:
		target = &m.Encoding
	default:
		return false
	}
	if enabled {
		*target = mode.(ansi.DECMode)
	} else if *target == mode {
		*target = 0
	}
	return true
}

// ScrollMouse handles complete SGR mouse reports. Other inputs bypass it.
func ScrollMouse(raw []byte) (x, y, lines int, ok bool) {
	if !bytes.HasPrefix(raw, []byte("\x1b[<")) {
		return
	}
	var decoder uv.EventDecoder
	n, event := decoder.Decode(raw)
	mouse, ok := event.(uv.MouseEvent)
	if !ok || n != len(raw) {
		return 0, 0, 0, false
	}
	m := mouse.Mouse()
	if _, wheel := event.(uv.MouseWheelEvent); wheel {
		switch m.Button {
		case uv.MouseWheelUp:
			lines = 3
		case uv.MouseWheelDown:
			lines = -3
		}
	}
	return m.X, m.Y, lines, true
}
