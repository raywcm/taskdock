package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/raywcm/taskdock/internal/config"
	"github.com/raywcm/taskdock/internal/ipc"
	"github.com/raywcm/taskdock/internal/task"
)

type palette struct{ accent, muted lipgloss.Style }

func (m model) resolvedTheme() string {
	theme := m.settings.Theme
	if m.mode == "settings" {
		theme = m.draft.Theme
	}
	if theme != "auto" {
		return theme
	}
	if m.backgroundKnown {
		if m.darkBackground {
			return "dark"
		}
		return "light"
	}
	parts := strings.Split(os.Getenv("COLORFGBG"), ";")
	if n, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
		if n == 7 || n == 15 {
			return "light"
		}
		return "dark"
	}
	return "auto"
}
func (m model) palette() palette {
	dark := m.resolvedTheme() != "light"
	accent, muted := "#70E1C8", "#909BAE"
	if !dark {
		accent, muted = "#006C70", "#536175"
	}
	return palette{lipgloss.NewStyle().Foreground(lipgloss.Color(accent)), lipgloss.NewStyle().Foreground(lipgloss.Color(muted))}
}
func (m model) header(width int) string {
	p := m.palette()
	brand := p.accent.Bold(true).Render("TaskDock")
	brandWidth := ansi.StringWidth(brand) + 2
	if width <= brandWidth {
		return ansi.Truncate(brand, width, "…")
	}
	return brand + "  " + m.helpLine(width-brandWidth)
}
func (m model) tabs(width int) string {
	p := m.palette()
	labels := make([]string, tabCount)
	gap := "   "
	for i, name := range tabNames {
		labels[i] = m.locale().Text(name)
		if i != tabSettings {
			labels[i] = fmt.Sprintf("%s %d", labels[i], len(m.rows(i)))
		}
	}
	needed := func() int {
		cells := (tabCount-1)*len(gap) + tabCount*2
		for _, label := range labels {
			cells += ansi.StringWidth(label)
		}
		return cells
	}
	if needed() > width {
		gap = " "
		for i, name := range tabNames {
			labels[i] = m.locale().Text(name)
		}
	}
	if needed() > width {
		labelWidth := max(1, (width-tabCount*2-(tabCount-1))/tabCount)
		for i, label := range labels {
			labels[i] = ansi.Truncate(label, labelWidth, "…")
		}
	}
	tabs := make([]string, 0, tabCount)
	for i, text := range labels {
		style := lipgloss.NewStyle().Padding(0, 1)
		if m.tab == i {
			style = style.Bold(true).Reverse(true)
		} else {
			style = style.Inherit(p.muted)
		}
		tabs = append(tabs, style.Render(text))
	}
	return ansi.Truncate(strings.Join(tabs, gap), width, "…")
}
func (m model) rowText(r task.Record, c columns) string {
	marker := "○"
	if active(r) {
		marker = "●"
	}
	if r.Health == "unreachable" {
		marker = "!"
	}
	cpu, mem := "—", "—"
	if r.Resources != nil {
		mem = memory(r.Resources.MemoryBytes)
		if r.Resources.CPUAvailable {
			cpu = fmt.Sprintf("%.1f%%", r.Resources.CPUPercent)
		} else {
			cpu = "…"
		}
	}
	return c.render(marker+" "+safe(r.Spec.Name), shortID(r.Spec.ID, m.tasks), duration(elapsed(r, m.now)), cpu, mem)
}
func (m model) columns(width int) columns {
	c := layout(width, m.settings.ShowResources)
	if c.id > 0 {
		longest := c.id
		for _, r := range m.tasks {
			longest = max(longest, len(shortID(r.Spec.ID, m.tasks)))
		}
		c.name = max(1, c.name-(longest-c.id))
		c.id = longest
	}
	return c
}
func (m model) card(r task.Record, width int, selected bool) string {
	c := m.columns(max(1, width-4))
	style := lipgloss.NewStyle().Width(max(1, width)).Padding(0, 2)
	if selected {
		// Keep the previous horizontal layout, but use a one-line side frame.
		style = style.Padding(0, 1).Border(lipgloss.NormalBorder()).BorderTop(false).BorderBottom(false).Reverse(true).Bold(true).BorderForeground(m.palette().accent.GetForeground())
	}
	return style.Render(m.rowText(r, c))
}
func (m model) helpLine(width int) string {
	t, p := m.locale().Text, m.palette()
	hint, compact := "", ""
	switch {
	case m.mode == "help":
		hint, compact = t("help_close_hint"), "Esc / ?"
	case m.mode == "search":
		hint, compact = t("search_hint"), "↑↓ Enter Esc"
	case m.mode == "history":
		hint, compact = t("history_hint"), "↑↓ PgUp/PgDn Esc"
	case m.mode == "connect":
		hint, compact = t("connection_hint"), "↑↓ Enter Esc"
	case m.mode == "new" || m.mode == "rename":
		hint, compact = t("edit_hint"), "Enter Esc"
	case m.hasDialog():
		hint, compact = t("confirm_hint"), "Enter Esc"
	case m.tab == tabSettings:
		hint, compact = t("settings_tab_hint"), "←→ Enter Esc"
		if m.settingsEditing {
			hint, compact = t("settings_hint"), "↑↓ ←→ Enter Esc"
		}
	case m.monitoring:
		hint, compact = t("monitor_hint"), "↑↓ Shift+↑↓ Ctrl+Q/F12 Ctrl+F"
		if m.monitorFrame.Err != nil {
			hint, compact = t("monitor_retry_hint"), "↑↓ F5 Ctrl+Q/F12"
		}
	}
	if hint != "" {
		if ansi.StringWidth(hint) <= width {
			return p.muted.Render(hint)
		}
		return p.muted.Render(ansi.Truncate(compact, width, "…"))
	}
	enterLabel := t("attach")
	if m.tab == tabStop {
		enterLabel = t("view_history")
	}
	bindings := [][2]string{{"←→", t("tabs_short")}, {"↑↓", t("select")}, {"Enter", enterLabel}, {"v", t("monitor")}, {"n", t("new")}, {"Ctrl+F", t("search_sessions")}, {"?", t("help")}, {"q", t("quit")}}
	items := []string{}
	for _, binding := range bindings {
		items = append(items, p.accent.Render(binding[0])+" "+p.muted.Render(binding[1]))
	}
	line := strings.Join(items, " ")
	if ansi.StringWidth(line) <= width {
		return line
	}
	for _, compact := range []string{"←→ ↑↓ Enter v n Ctrl+F ? q", "Enter v Ctrl+F ? q", "v ? q"} {
		if ansi.StringWidth(compact) <= width {
			return p.muted.Render(compact)
		}
	}
	return ansi.Truncate(p.muted.Render("? "+t("help")), width, "…")
}
func (m model) detail(width int) string {
	r, ok := m.current()
	if !ok {
		return ""
	}
	state := "not_started"
	if r.Runtime != nil {
		state = string(r.Runtime.State)
	}
	if r.Health == "unreachable" {
		state = "unreachable"
	}
	command := ""
	if len(r.Spec.Command) > 0 {
		command = safe(filepath.Base(r.Spec.Command[0]))
	}
	line := safe(r.Spec.Name) + " · " + m.locale().Text(state) + " · " + command + " · " + displayPath(r.Spec.Dir)
	if r.Runtime != nil && r.Runtime.ExitCode != nil {
		line += fmt.Sprintf(" · %s %d", m.locale().Text("exit_code"), *r.Runtime.ExitCode)
	}
	position := fmt.Sprintf("%d / %d", m.cursors[m.tab]+1, len(m.rows(m.tab)))
	if width > ansi.StringWidth(position)+8 {
		return fit(line, width-ansi.StringWidth(position)-2) + "  " + position
	}
	return ansi.Truncate(line, width, "…")
}
func (m model) status(width int) string {
	if m.err != nil {
		var remote *ipc.RemoteError
		if errors.As(m.err, &remote) && remote.Code == "unsupported_attach_mode" {
			return ansi.Truncate(m.locale().Text("unsupported_attach_mode"), width, "…")
		}
		return ansi.Truncate(m.locale().Text("error")+": "+safe(m.err.Error()), width, "…")
	}
	if m.busy {
		return m.locale().Text("working")
	}
	return ansi.Truncate(m.message, width, "…")
}
func (m model) footer(width int) []string {
	p := m.palette()
	lines := []string{"", p.muted.Render(m.detail(width)), m.status(width)}
	return lines
}
func (m model) retentionLabel(seconds int64) string {
	t := m.locale().Text
	if seconds == 0 {
		return t("never")
	}
	for _, unit := range []struct {
		seconds          int64
		singular, plural string
	}{{86400, "day", "days"}, {3600, "hour", "hours"}, {60, "minute", "minutes"}, {1, "second", "seconds"}} {
		if seconds%unit.seconds == 0 {
			count := seconds / unit.seconds
			label := unit.plural
			if count == 1 {
				label = unit.singular
			}
			return fmt.Sprintf("%d %s", count, t(label))
		}
	}
	return ""
}
func (m model) settingsView(width int) []string {
	t := m.locale().Text
	p := m.palette()
	language := config.LanguageName(m.draft.Language)
	if m.draft.Language == "auto" {
		language = t("auto")
	}
	on := t("off")
	if m.draft.ShowResources {
		on = t("on")
	}
	options := [][2]string{{t("language"), language}, {t("theme"), t(m.draft.Theme)}, {t("interval"), m.retentionLabel(int64(m.draft.RefreshSeconds))}, {t("resources"), on}, {t("default_tab"), t(m.draft.DefaultTab)}, {t("auto_delete"), m.retentionLabel(m.draft.HistoryRetentionSeconds)}}
	version := t("version") + "  v" + strings.TrimPrefix(m.version, "v")
	lines := []string{m.header(width), "", m.tabs(width), p.muted.Render(strings.Repeat("─", width)), p.muted.Render(version), ""}
	labelWidth := min(22, max(10, width/2))
	valueWidth := max(1, width-labelWidth-5)
	visible := max(1, m.height-len(lines)-2)
	start := 0
	if m.settingsEditing {
		start = max(0, m.settingRow-visible+1)
	}
	for i := start; i < min(len(options), start+visible); i++ {
		option := options[i]
		line := "  " + fit(option[0], labelWidth) + " " + fit("‹ "+option[1]+" ›", valueWidth)
		if m.settingsEditing && i == m.settingRow {
			line = lipgloss.NewStyle().Bold(true).Reverse(true).Width(width).Render(line)
		}
		lines = append(lines, line)
	}
	lines = append(lines, m.status(width))
	return lines
}
func (m model) helpView(width int) []string {
	t := m.locale().Text
	p := m.palette()
	enterAction := t("attach")
	if m.tab == tabStop {
		enterAction = t("view_history")
	}
	tabKeys := "← / → · Tab / Shift+Tab"
	if m.monitoring {
		tabKeys = "Tab / Shift+Tab"
	}
	lines := []string{m.header(width), "", p.accent.Bold(true).Render(t("keyboard_help")), p.muted.Render(strings.Repeat("─", width))}
	bindings := [][2]string{
		{"↑ / ↓ · j / k", t("select")},
		{"Shift+↑ / Shift+↓", t("move_session")},
		{tabKeys, t("tabs")},
		{"Enter", enterAction},
		{"v", t("monitor")},
		{"Ctrl+F", t("search_sessions")},
		{"c", t("connection_modes")},
		{"n", t("new")},
		{"s", t("stop_action")},
		{"F2 · e", t("rename")},
		{"Delete / Backspace · x", t("delete")},
		{"F5 · r", t("refresh")},
		{",", t("settings")},
		{"F1 / ?", t("help")},
		{"Esc / q / Ctrl+C", t("quit")},
	}
	for _, binding := range bindings {
		lines = append(lines, p.accent.Render(binding[0])+"  "+binding[1])
	}
	lines = append(lines, "", t("detach_hint"), t("help_close_hint"))
	return lines
}

func (m model) connectionView(width int) []string {
	t, p := m.locale().Text, m.palette()
	suffix := " [" + shortID(m.target, m.tasks) + "]"
	name := ansi.Truncate(safe(m.targetName), max(1, width-ansi.StringWidth(suffix)), "…") + suffix
	lines := []string{m.header(width), "", p.accent.Bold(true).Render(t("connection_modes")), name, ""}
	if m.mode == "takeover" {
		return append(lines, t("takeover_prompt"), t("takeover_hint"))
	}
	for i, mode := range connectionModes {
		line := "  " + t("mode_"+mode) + " · " + t("desc_"+mode)
		if i == m.connectChoice {
			line = lipgloss.NewStyle().Bold(true).Reverse(true).Render(line)
		}
		lines = append(lines, line)
	}
	return append(lines, "", m.status(width))
}

func (m model) View() tea.View {
	if m.quitting {
		view := tea.NewView("")
		view.AltScreen = true
		return view
	}
	width := max(1, min(120, m.width-4))
	if m.monitoring || m.mode == "history" {
		width = max(1, m.width-4)
	}
	t := m.locale().Text
	p := m.palette()
	var lines []string
	showMonitor := false
	if m.width < 26 || m.height < 9 {
		lines = []string{m.header(width), t("small_window"), "q " + t("quit")}
	} else if m.mode == "help" {
		lines = m.helpView(width)
	} else if m.mode == "search" {
		lines = m.searchView(width)
	} else if m.mode == "history" {
		lines = m.historyView(width)
	} else if m.mode == "connect" {
		lines = m.connectionView(width)
	} else if m.monitoring && m.tab == tabRunning {
		lines = m.monitorView(width)
		showMonitor = m.width >= 50 && m.height >= 12
	} else if m.tab == tabSettings {
		lines = m.settingsView(width)
	} else {
		compact := m.height < 18
		lines = []string{m.header(width), "", m.tabs(width), p.muted.Render(strings.Repeat("─", width))}
		c := m.columns(max(1, width-4))
		if !compact {
			timeLabel := t("uptime")
			if m.tab == 1 {
				timeLabel = t("duration")
			}
			lines = append(lines, "  "+p.muted.Render(c.render(t("session"), t("id"), timeLabel, t("cpu"), t("memory"))))
		}
		rows := m.rows(m.tab)
		footer := m.footer(max(1, m.width-4))
		bodyHeight := max(1, m.height-len(lines)-len(footer))
		visible := bodyHeight
		cursor := m.cursors[m.tab]
		start := max(0, cursor-visible+1)
		drawn := 0
		if !m.ready {
			lines = append(lines, t("loading"))
			drawn = 1
		} else if len(rows) == 0 {
			key := "empty_running"
			if m.tab == 1 {
				key = "empty_stop"
			}
			lines = append(lines, t(key))
			drawn = 1
		} else {
			for i := start; i < min(len(rows), start+visible); i++ {
				card := strings.Split(m.card(rows[i], width, i == cursor), "\n")
				lines = append(lines, card...)
				drawn += len(card)
			}
		}
		for drawn < bodyHeight {
			lines = append(lines, "")
			drawn++
		}
		lines = append(lines, footer...)
	}
	// Cell-aware clipping bounds CJK names, ANSI styles and tiny terminal sizes.
	if len(lines) > m.height {
		lines = lines[:m.height]
	}
	for i, line := range lines {
		line = "  " + ansi.Truncate(line, max(1, m.width-4), "…")
		theme := m.resolvedTheme()
		if theme == "light" || theme == "dark" {
			base := "\x1b[0m\x1b[38;2;229;237;247m\x1b[48;2;20;26;38m"
			if theme == "light" {
				base = "\x1b[0m\x1b[38;2;31;42;59m\x1b[48;2;246;248;252m"
			}
			paint := func(text string) string {
				return base + strings.NewReplacer("\x1b[m", base, "\x1b[0m", base).Replace(text) + "\x1b[m"
			}
			left, right, height := m.monitorGeometry()
			if showMonitor && m.monitorFrame.Ready && m.monitorFrame.Err == nil && i >= 5 && i < 5+height {
				// The manager theme belongs to the sidebar and gutters. The
				// preview's default colors and inverse highlights belong to the
				// application, just as they do in a native attachment.
				x := left + 5
				label := fit(ansi.Cut(line, 0, x), x)
				preview := fit(ansi.Cut(line, x, x+right), right)
				gutter := strings.Repeat(" ", max(0, m.width-x-right))
				line = paint(label) + "\x1b[0m" + preview + "\x1b[0m" + paint(gutter)
			} else {
				line = paint(fit(line, m.width))
			}
		}
		lines[i] = line
	}
	content := strings.Join(lines, "\n")
	if m.hasDialog() {
		content = m.dialog(content)
	}
	view := tea.NewView(content)
	view.AltScreen = true
	if m.monitoring && m.mode == "" && m.monitorFrame.Ready && m.monitorFrame.Err == nil && !m.monitorFrame.CursorHidden {
		left, right, height := m.monitorGeometry()
		if m.monitorFrame.CursorX < right && m.monitorFrame.CursorY < height {
			view.Cursor = tea.NewCursor(left+5+max(0, m.monitorFrame.CursorX), 5+max(0, m.monitorFrame.CursorY))
		}
	}
	return view
}
