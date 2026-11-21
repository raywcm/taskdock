package terminal

import (
	"bytes"
	"image/color"
	"os"
	"strconv"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// ResolveColorMode prefers a terminal reply over COLORFGBG in automatic mode.
func ResolveColorMode(mode string, background color.Color) string {
	if mode == "light" || mode == "dark" {
		return mode
	}
	if background == nil {
		hint := os.Getenv("COLORFGBG")
		index, err := strconv.ParseUint(hint[strings.LastIndexByte(hint, ';')+1:], 10, 8)
		if err != nil {
			return "auto"
		}
		background = ansi.IndexedColor(index)
	}
	if (uv.BackgroundColorEvent{Color: background}).IsDark() {
		return "dark"
	}
	return "light"
}

func TerminalAccent(mode string) ansi.BasicColor {
	switch mode {
	case "light":
		return ansi.Blue
	case "dark":
		return ansi.BrightCyan
	default:
		return ansi.Cyan
	}
}

func BackgroundReply(raw []byte) color.Color {
	if !bytes.HasPrefix(raw, []byte("\x1b]11;")) {
		return nil
	}
	var decoder uv.EventDecoder
	n, event := decoder.Decode(raw)
	if reply, ok := event.(uv.BackgroundColorEvent); ok && n == len(raw) {
		return reply.Color
	}
	return nil
}
