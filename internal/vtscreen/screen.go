// Package vtscreen contains the bounded terminal used by session viewports.
package vtscreen

import (
	"strings"
	"unicode"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

const ScrollbackLimit = 1000

func New(width, height int) *vt.Emulator {
	e := vt.NewEmulator(width, height)
	// Retained output may describe a larger terminal. x/vt v0.1.0 accepts
	// out-of-screen margins, then panics on insert/delete/reverse-index. Ignore
	// those invalid regions before the dependency can store them. Rendering
	// uses this guard for every bounded viewport.
	e.RegisterCsiHandler('r', func(params ansi.Params) bool {
		top, _, _ := params.Param(0, 1)
		bottom, _, _ := params.Param(1, e.Height())
		return top > e.Height() || bottom > e.Height()
	})
	e.RegisterCsiHandler('s', func(params ansi.Params) bool {
		left, _, _ := params.Param(0, 1)
		right, _, _ := params.Param(1, e.Width())
		return left > e.Width() || right > e.Width()
	})
	e.SetScrollbackSize(ScrollbackLimit)
	return e
}
func Rows(e *vt.Emulator) []string {
	rows := make([]string, e.Height())
	for y := 0; y < e.Height(); y++ {
		rows[y] = renderRow(e.Width(), func(x int) *uv.Cell { return e.CellAt(x, y) })
	}
	return rows
}

func renderRow(width int, at func(int) *uv.Cell) string {
	return renderRowSelected(width, at, nil)
}

func renderRowSelected(width int, at func(int) *uv.Cell, selected func(int) bool) string {
	var line strings.Builder
	style := ""
	sel := false
	for x := range width {
		cell := at(x)
		wantSel := selected != nil && selected(x)
		if cell == nil {
			if style != "" || sel != wantSel {
				line.WriteString("\x1b[0m")
				style = ""
				sel = wantSel
				if sel {
					line.WriteString("\x1b[7m")
				}
			}
			line.WriteByte(' ')
			continue
		}
		if cell.Width == 0 {
			continue
		}
		next := cell.Style.String()
		if next != style || wantSel != sel {
			line.WriteString("\x1b[0m")
			line.WriteString(next)
			if wantSel {
				line.WriteString("\x1b[7m")
			}
			style = next
			sel = wantSel
		}
		// Only generated SGR plus printable cell content leave this renderer.
		// OSC title/clipboard/hyperlinks, DCS and terminal queries stay in the emulator.
		content := sanitizeCellContent(cell.Content)
		if content == "" {
			content = " "
		}
		line.WriteString(content)
	}
	line.WriteString("\x1b[0m")
	return line.String()
}

func sanitizeCellContent(raw string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, raw)
}
