package vtscreen

import (
	"slices"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
)

// Viewport freezes the retained lines while the user reads earlier output.
// The emulator keeps parsing live output so returning to the bottom is instant.
type Viewport struct {
	history []uv.Line
	live    []uv.Line
	offset  int
}

func (v *Viewport) Reset()         { *v = Viewport{} }
func (v *Viewport) Scrolled() bool { return v != nil && v.offset > 0 }

func cloneEmulatorLines(e *vt.Emulator) []uv.Line {
	w, h := e.Width(), e.Height()
	lines := make([]uv.Line, h)
	for y := 0; y < h; y++ {
		row := make(uv.Line, w)
		for x := 0; x < w; x++ {
			if c := e.CellAt(x, y); c != nil {
				row[x] = *c
			}
		}
		lines[y] = row
	}
	return lines
}

func (v *Viewport) Scroll(e *vt.Emulator, lines int) {
	if lines == 0 {
		return
	}
	if !v.Scrolled() && lines > 0 && e.ScrollbackLen() > 0 {
		// The emulator clones the cells it adds to scrollback. Copy the line
		// headers before the emulator rotates its buffer.
		v.history = slices.Clone(e.Scrollback().Lines())
		v.live = cloneEmulatorLines(e)
	}
	v.offset = min(len(v.history), max(0, v.offset+lines))
	if !v.Scrolled() {
		v.Reset()
	}
}

func (v *Viewport) CellAt(e *vt.Emulator, x, y int) *uv.Cell {
	if v == nil || !v.Scrolled() {
		return e.CellAt(x, y)
	}
	idx := len(v.history) - v.offset + y
	var line uv.Line
	if idx >= 0 && idx < len(v.history) {
		line = v.history[idx]
	} else if liveIdx := idx - len(v.history); liveIdx >= 0 && liveIdx < len(v.live) {
		line = v.live[liveIdx]
	}
	if x < 0 || x >= len(line) {
		return nil
	}
	return &line[x]
}

func (v *Viewport) Grid(e *vt.Emulator) []uv.Line {
	if v == nil || !v.Scrolled() {
		return cloneEmulatorLines(e)
	}
	w := e.Width()
	grid := make([]uv.Line, len(v.live))
	for row := range grid {
		y := len(v.history) - v.offset + row
		var src uv.Line
		if y >= 0 && y < len(v.history) {
			src = v.history[y]
		} else if liveIdx := y - len(v.history); liveIdx >= 0 && liveIdx < len(v.live) {
			src = v.live[liveIdx]
		}
		n := min(w, len(src))
		dst := make(uv.Line, w)
		copy(dst, src[:n])
		grid[row] = dst
	}
	return grid
}

func (v *Viewport) Rows(e *vt.Emulator) []string {
	return v.RowsWithSelection(e, nil)
}

func (v *Viewport) RowsWithSelection(e *vt.Emulator, sel *Selection) []string {
	w, h := e.Width(), e.Height()
	if v != nil && v.Scrolled() {
		h = len(v.live)
	}
	rows := make([]string, h)
	for row := 0; row < h; row++ {
		var selected func(int) bool
		if sel != nil && (sel.Active || sel.Dragging) {
			rowY := row
			selected = func(x int) bool { return sel.Contains(x, rowY) }
		}
		rowY := row
		rows[row] = renderRowSelected(w, func(x int) *uv.Cell { return v.CellAt(e, x, rowY) }, selected)
	}
	return rows
}
