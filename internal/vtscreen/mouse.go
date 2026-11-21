package vtscreen

import (
	"bytes"
	"fmt"
	"image"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

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

// CellTracking returns the tracking available in an embedded, cell-based pane.
func (m MouseModes) CellTracking() ansi.DECMode {
	if m.Encoding == ansi.ModeMouseExtSgrPixel || m.Tracking == ansi.ModeMouseHighlight {
		return 0
	}
	return m.Tracking
}

func DecodeMouse(raw []byte) uv.MouseEvent {
	if !bytes.HasPrefix(raw, []byte("\x1b[<")) {
		return nil
	}
	var decoder uv.EventDecoder
	n, event := decoder.Decode(raw)
	mouse, _ := event.(uv.MouseEvent)
	if n != len(raw) {
		return nil
	}
	return mouse
}

func WheelLines(button uv.MouseButton) int {
	switch button {
	case uv.MouseWheelUp:
		return 3
	case uv.MouseWheelDown:
		return -3
	}
	return 0
}

// MouseCapture binds a drag to the pane that received the button press.
type MouseCapture struct{ pressed uint16 }

// Classic reports reserve the first 32 byte values and use one-based coordinates.
const legacyMouseCells = 255 - 32

func (c *MouseCapture) Reset() { c.pressed = 0 }

func (c *MouseCapture) Input(modes MouseModes, event uv.MouseEvent, area image.Rectangle) []byte {
	if modes.Encoding == 0 {
		area.Max.X = min(area.Max.X, area.Min.X+legacyMouseCells)
		area.Max.Y = min(area.Max.Y, area.Min.Y+legacyMouseCells)
	}
	if area.Empty() {
		c.Reset()
		return nil
	}
	mouse := event.Mouse()
	point := image.Pt(mouse.X, mouse.Y)
	button := uint16(1) << mouse.Button
	held := c.pressed&button != 0
	_, click := event.(uv.MouseClickEvent)
	switch event.(type) {
	case uv.MouseClickEvent:
		c.pressed &^= button
		held = false
	case uv.MouseReleaseEvent:
		c.pressed &^= button
		if !held {
			return nil
		}
	case uv.MouseMotionEvent:
		if mouse.Button != uv.MouseNone && !held {
			return nil
		}
	}
	if !point.In(area) {
		if !held {
			return nil
		}
		point.X = min(area.Max.X-1, max(area.Min.X, point.X))
		point.Y = min(area.Max.Y-1, max(area.Min.Y, point.Y))
	}
	data := modes.EncodeCell(event, point.X-area.Min.X, point.Y-area.Min.Y)
	if click && data != nil && modes.Tracking != ansi.ModeMouseX10 {
		c.pressed |= button
	}
	return data
}

// EncodeCell translates the host event into the protocol requested by the application.
func (m MouseModes) EncodeCell(event uv.MouseEvent, x, y int) []byte {
	mouse := event.Mouse()
	_, motion := event.(uv.MouseMotionEvent)
	_, release := event.(uv.MouseReleaseEvent)
	switch m.CellTracking() {
	case 0:
		return nil
	case ansi.ModeMouseX10:
		if _, click := event.(uv.MouseClickEvent); !click {
			return nil
		}
		mouse.Mod = 0
	case ansi.ModeMouseNormal:
		if motion {
			return nil
		}
	case ansi.ModeMouseButtonEvent:
		if motion && mouse.Button == uv.MouseNone {
			return nil
		}
	}

	if release && m.Encoding != ansi.ModeMouseExtSgr {
		mouse.Button = uv.MouseNone
	}
	b := ansi.EncodeMouseButton(mouse.Button, motion, mouse.Mod.Contains(uv.ModShift), mouse.Mod.Contains(uv.ModAlt), mouse.Mod.Contains(uv.ModCtrl))
	switch m.Encoding {
	case ansi.ModeMouseExtSgr:
		return []byte(ansi.MouseSgr(b, x, y, release))
	case ansi.ModeMouseExtUrxvt:
		return fmt.Appendf(nil, "\x1b[%d;%d;%dM", int(b)+32, x+1, y+1)
	case ansi.ModeMouseExtUtf8:
		return []byte("\x1b[M" + string(rune(b)+32) + string(rune(x)+33) + string(rune(y)+33))
	default:
		if x >= legacyMouseCells || y >= legacyMouseCells {
			return nil
		}
		return []byte{0x1b, '[', 'M', b + 32, byte(x) + 33, byte(y) + 33}
	}
}
