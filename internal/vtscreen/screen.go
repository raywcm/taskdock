// Package vtscreen contains the bounded terminal used by session viewports.
package vtscreen

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

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
	e.SetScrollbackSize(0)
	return e
}
func Rows(e *vt.Emulator) []string {
	rows := make([]string, e.Height())
	for y := 0; y < e.Height(); y++ {
		var line strings.Builder
		style := ""
		for x := 0; x < e.Width(); x++ {
			cell := e.CellAt(x, y)
			if cell == nil {
				if style != "" {
					line.WriteString("\x1b[0m")
					style = ""
				}
				line.WriteByte(' ')
				continue
			}
			if cell.Width == 0 {
				continue
			}
			next := cell.Style.String()
			if next != style {
				line.WriteString("\x1b[0m")
				line.WriteString(next)
				style = next
			}
			// Only generated SGR plus printable cell content leave this renderer.
			// OSC title/clipboard/hyperlinks, DCS and terminal queries stay in the emulator.
			content := strings.Map(func(r rune) rune {
				if unicode.IsControl(r) {
					return -1
				}
				return r
			}, cell.Content)
			if content == "" {
				content = " "
			}
			line.WriteString(content)
		}
		line.WriteString("\x1b[0m")
		rows[y] = line.String()
	}
	return rows
}
