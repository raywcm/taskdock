package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func (m model) hasDialog() bool {
	return m.mode == "stop" || m.mode == "delete" || m.mode == "takeover" || m.mode == "name-conflict" || m.mode == "new" || m.mode == "rename"
}
func (m model) dialogBody(textWidth int, p palette) []string {
	t := m.locale().Text
	if m.mode == "new" || m.mode == "rename" {
		prompt := t("new_prompt")
		if m.mode == "rename" {
			prompt = t("rename_prompt")
		}
		return append(strings.Split(ansi.Wrap(prompt, textWidth, ""), "\n"), "", m.input.View(), "", t("edit_hint"), m.status(textWidth))
	}
	title, prompt, action := t("confirm_stop"), t("stop_prompt"), t("stop_action")
	switch m.mode {
	case "delete":
		title, prompt, action = t("confirm_delete"), t("delete_prompt"), t("delete")
	case "takeover":
		title, prompt, action = t("confirm_takeover"), t("takeover_prompt"), t("mode_takeover")
	case "name-conflict":
		title, prompt, action = t("name_conflict"), t("name_conflict_prompt"), t("use_name")
	}
	var details []string
	if m.mode == "name-conflict" {
		details = []string{t("requested_name") + ": " + safe(m.conflictName), t("suggested_name") + ": " + safe(m.proposedName)}
	} else {
		details = []string{safe(m.targetName) + " [" + m.shortID(m.target) + "]"}
	}
	body := []string{lipgloss.NewStyle().Bold(true).Render("! " + title), ""}
	body = append(body, strings.Split(ansi.Wrap(prompt, textWidth, ""), "\n")...)
	body = append(body, "")
	for _, line := range details {
		body = append(body, strings.Split(ansi.Wrap(line, textWidth, ""), "\n")...)
	}
	// Keep the action buttons visible even on small screens and long CJK names.
	maxBody := max(1, m.height-8)
	if len(body) > maxBody {
		body = body[:maxBody]
		body[len(body)-1] = ansi.Truncate(body[len(body)-1], max(1, textWidth-1), "") + "…"
	}
	buttons := []string{"[ " + t("cancel") + " ]", "[ " + action + " ]"}
	for i := range buttons {
		buttons[i] = p.choice(buttons[i], i == m.confirmChoice)
	}
	body = append(body, "")
	if ansi.StringWidth(strings.Join(buttons, "   ")) > textWidth {
		body = append(body, buttons...)
	} else {
		body = append(body, strings.Join(buttons, "   "))
	}
	return append(body, t("confirm_hint"))
}

func (m model) dialog(base string) string {
	p := m.palette()
	width := max(10, min(68, m.width-6))
	textWidth := max(6, width-6)
	body := m.dialogBody(textWidth, p)
	for i := range body {
		body[i] = ansi.Truncate(body[i], textWidth, "…")
	}
	boxStyle := lipgloss.NewStyle().Width(width).Padding(1, 2).Border(lipgloss.RoundedBorder()).BorderForeground(p.accent.GetForeground())
	dimStyle := p.muted
	if !p.terminal {
		bg, fg, border := "#202C3D", "#EFF5FF", "#F2C078"
		if p.colorMode == "light" {
			bg, fg, border = "#FFFFFF", "#243141", "#986000"
		}
		boxStyle = boxStyle.BorderForeground(lipgloss.Color(border)).Background(lipgloss.Color(bg)).Foreground(lipgloss.Color(fg))
		dimStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#697586"))
	}
	box := boxStyle.Render(strings.Join(body, "\n"))
	x := max(0, (m.width-lipgloss.Width(box))/2)
	y := max(0, (m.height-lipgloss.Height(box))/2)
	// Compose complete opaque rows. Padding must replace every old cell when
	// a dialog changes height while the monitor behind it keeps refreshing.
	backdrop := strings.Split(ansi.Strip(base), "\n")
	boxLines := strings.Split(box, "\n")
	boxWidth := lipgloss.Width(box)
	lines := make([]string, m.height)
	for i := range lines {
		line := ""
		if i < len(backdrop) {
			line = backdrop[i]
		}
		line = dimStyle.Render(fit(line, m.width))
		if row := i - y; row >= 0 && row < len(boxLines) {
			line = fit(ansi.Cut(line, 0, x), x) + "\x1b[0m" + fit(boxLines[row], boxWidth) + "\x1b[0m" + ansi.Cut(line, x+boxWidth, m.width)
		}
		lines[i] = fit(ansi.Truncate(line, m.width, ""), m.width)
	}
	return strings.Join(lines, "\n")
}
