package tui

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/raywcm/taskdock/internal/config"
	"github.com/raywcm/taskdock/internal/ipc"
	"github.com/raywcm/taskdock/internal/task"
	"github.com/raywcm/taskdock/internal/terminal"
)

type palette struct {
	accent, muted lipgloss.Style
	terminal      bool
	colorMode     string
}

func (p palette) rule(width int) string {
	if p.terminal {
		return strings.Repeat("━", width)
	}
	return p.muted.Render(strings.Repeat("─", width))
}

func (p palette) arrow(label string, selected bool) string {
	if selected {
		label = "› " + label
		if p.terminal {
			return p.accent.Bold(true).Render(label)
		}
		return label
	}
	return "  " + label
}

func (p palette) choice(label string, selected bool) string {
	if p.terminal {
		return p.arrow(label, selected)
	}
	if selected {
		return lipgloss.NewStyle().Bold(true).Reverse(true).Render(label)
	}
	return label
}

func (p palette) listChoice(label string, selected bool) string {
	if !p.terminal {
		label = "  " + label
	}
	return p.choice(label, selected)
}

func (m model) palette() palette {
	settings := m.settings
	if m.mode == "settings" {
		settings = m.draft
	}
	mode := terminal.ResolveColorMode(settings.ColorMode, m.background)
	if settings.Theme == "terminal" {
		return palette{accent: lipgloss.NewStyle().Foreground(terminal.TerminalAccent(mode)), muted: lipgloss.NewStyle().Faint(true), terminal: true, colorMode: mode}
	}
	accent, muted := "#70E1C8", "#909BAE"
	if mode == "light" {
		accent, muted = "#006C70", "#536175"
	}
	return palette{accent: lipgloss.NewStyle().Foreground(lipgloss.Color(accent)), muted: lipgloss.NewStyle().Foreground(lipgloss.Color(muted)), colorMode: mode}
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
	t, p := m.locale().Text, m.palette()
	labels := make([]string, tabCount)
	occupied := 2 * tabCount // an arrow or two padding cells per tab
	for i, name := range tabNames {
		labels[i] = t(name)
		occupied += ansi.StringWidth(labels[i])
	}
	if width < 3*tabCount+(tabCount-1) {
		if p.terminal {
			return ansi.Truncate(p.arrow(labels[m.tab], true), width, "")
		}
		return p.accent.Bold(true).Render(ansi.Truncate(labels[m.tab], width, "…"))
	}
	gap := " "
	countSpace := width - occupied - 3*(tabCount-1) - 2
	if occupied+(tabCount-1) > width {
		labelWidth := (width - 2*tabCount - (tabCount - 1)) / tabCount
		for i, label := range labels {
			labels[i] = ansi.Truncate(label, labelWidth, "…")
		}
	} else if countSpace >= 2 {
		// Tab counts need only one pass, regardless of how many rows fit on screen.
		running := 0
		for i := range m.tasks {
			if active(m.tasks[i]) {
				running++
			}
		}
		live, history := strconv.Itoa(running), strconv.Itoa(len(m.tasks)-running)
		if len(live)+len(history) <= countSpace {
			gap = "   "
			labels[tabRunning] += " " + live
			labels[tabStop] += " " + history
		}
	}
	for i, text := range labels {
		if !p.terminal {
			text = " " + text + " "
		}
		if m.tab != i {
			text = p.muted.Render(text)
		}
		labels[i] = p.choice(text, m.tab == i)
	}
	return strings.Join(labels, gap)
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
	return c.render(marker+" "+safe(r.Spec.Name), m.shortID(r.Spec.ID), duration(elapsed(r, m.now)), cpu, mem)
}
func (m model) columns(width int) columns {
	c := layout(width, m.settings.ShowResources)
	if c.id > 0 {
		longest := c.id
		for _, r := range m.tasks {
			longest = max(longest, len(m.shortID(r.Spec.ID)))
		}
		c.name = max(1, c.name-(longest-c.id))
		c.id = longest
	}
	return c
}
func (p palette) card(line string, width int, selected bool) string {
	if p.terminal {
		return fit(p.arrow(line, selected), width)
	}
	style := lipgloss.NewStyle().Width(max(1, width)).Padding(0, 2)
	if selected {
		// Keep the previous horizontal layout, but use a one-line side frame.
		style = style.Padding(0, 1).Border(lipgloss.NormalBorder()).BorderTop(false).BorderBottom(false).Reverse(true).Bold(true).BorderForeground(p.accent.GetForeground())
	}
	return style.Render(line)
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
	bindings := [][2]string{{"←→", t("tabs_short")}, {"↑↓", t("select")}, {"Enter", enterLabel}, {"v", t("monitor")}, {"t", "tmux"}, {"n", t("new")}, {"Ctrl+F", t("search_sessions")}, {"?", t("help")}, {"q", t("quit")}}
	items := []string{}
	for _, binding := range bindings {
		items = append(items, p.accent.Render(binding[0])+" "+p.muted.Render(binding[1]))
	}
	line := strings.Join(items, " ")
	if ansi.StringWidth(line) <= width {
		return line
	}
	for _, compact := range []string{"←→ ↑↓ Enter v t n Ctrl+F ? q", "Enter v Ctrl+F ? q", "v ? q"} {
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
	err := m.loadErr
	if err == nil {
		err = m.err
	}
	if err != nil {
		var remote *ipc.RemoteError
		if errors.As(err, &remote) && remote.Code == "unsupported_attach_mode" {
			return ansi.Truncate(m.locale().Text("unsupported_attach_mode"), width, "…")
		}
		return ansi.Truncate(m.locale().Text("error")+": "+safe(err.Error()), width, "…")
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
	options := [settingsCount][2]string{
		{t("language"), language}, {t("theme"), t(m.draft.Theme)}, {t("color_mode"), t(m.draft.ColorMode)},
		{t("interval"), m.retentionLabel(int64(m.draft.RefreshSeconds))}, {t("resources"), on},
		{t("default_tab"), t(m.draft.DefaultTab)}, {t("auto_delete"), m.retentionLabel(m.draft.HistoryRetentionSeconds)},
	}
	version := t("version") + "  v" + strings.TrimPrefix(m.version, "v")
	lines := []string{m.header(width), "", m.tabs(width), p.rule(width), p.muted.Render(version), ""}
	labelWidth := min(22, max(10, width/2))
	valueWidth := max(1, width-labelWidth-5)
	visible := max(1, m.height-len(lines)-2)
	start := 0
	if m.settingsEditing {
		start = max(0, m.settingRow-visible+1)
	}
	for i := start; i < min(len(options), start+visible); i++ {
		option := options[i]
		line := fit(option[0], labelWidth) + " " + fit("‹ "+option[1]+" ›", valueWidth)
		lines = append(lines, p.listChoice(fit(line, width-2), m.settingsEditing && i == m.settingRow))
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
	lines := []string{m.header(width), "", p.accent.Bold(true).Render(t("keyboard_help")), p.rule(width)}
	bindings := [][2]string{
		{"↑ / ↓ · j / k", t("select")},
		{"Shift+↑ / Shift+↓", t("move_session")},
		{tabKeys, t("tabs")},
		{"Enter", enterAction},
		{"v", t("monitor")},
		{"t", "tmux"},
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
	suffix := " [" + m.shortID(m.target) + "]"
	name := ansi.Truncate(safe(m.targetName), max(1, width-ansi.StringWidth(suffix)), "…") + suffix
	lines := []string{m.header(width), "", p.accent.Bold(true).Render(t("connection_modes")), name, ""}
	if m.mode == "takeover" {
		return append(lines, t("takeover_prompt"), t("takeover_hint"))
	}
	for i, mode := range connectionModes {
		line := t("mode_"+mode) + " · " + t("desc_"+mode)
		lines = append(lines, p.listChoice(line, i == m.connectChoice))
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
	if m.monitoring || m.mode == "history" || m.mode == "tmux-view" {
		width = max(1, m.width-4)
	}
	t := m.locale().Text
	p := m.palette()
	var lines []string
	showMonitor := false
	app := m.appArea()
	frame := &m.monitorFrame
	if m.tooSmall() {
		lines = []string{m.header(width), t("small_window"), "q " + t("quit")}
	} else if m.mode == "tmux" || m.mode == "tmux-view" {
		lines = m.tmuxLines(width)
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
		showMonitor = !app.Empty()
	} else if m.tab == tabSettings {
		lines = m.settingsView(width)
	} else {
		compact := m.height < 18
		lines = []string{m.header(width), "", m.tabs(width), p.rule(width)}
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
				lines = append(lines, p.card(m.rowText(rows[i], c), width, i == cursor))
				drawn++
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
	padding := strings.Repeat(" ", min(2, m.width-1))
	for i, line := range lines {
		lines[i] = padding + ansi.Truncate(line, max(1, m.width-4), "…")
	}
	appReady := showMonitor && frame.Ready && frame.Err == nil
	if !p.terminal && p.colorMode != "auto" {
		base := "\x1b[0m\x1b[38;2;229;237;247m\x1b[48;2;20;26;38m"
		if p.colorMode == "light" {
			base = "\x1b[0m\x1b[38;2;31;42;59m\x1b[48;2;246;248;252m"
		}
		reset := strings.NewReplacer("\x1b[m", base, "\x1b[0m", base)
		paint := func(text string) string { return base + reset.Replace(text) + "\x1b[m" }
		for i, line := range lines {
			if appReady && i >= app.Min.Y && i < app.Max.Y {
				// Only the surrounding manager UI owns the theme background.
				label := fit(ansi.Cut(line, 0, app.Min.X), app.Min.X)
				preview := fit(ansi.Cut(line, app.Min.X, app.Max.X), app.Dx())
				gutter := strings.Repeat(" ", max(0, m.width-app.Max.X))
				lines[i] = paint(label) + "\x1b[0m" + preview + "\x1b[0m" + paint(gutter)
			} else {
				lines[i] = paint(fit(line, m.width))
			}
		}
	}
	content := strings.Join(lines, "\n")
	if m.hasDialog() && !m.tooSmall() {
		content = m.dialog(content)
	}
	view := tea.NewView(content)
	view.AltScreen = true
	if scroll, _ := m.scrollableText(); scroll != nil {
		view.MouseMode = tea.MouseModeCellMotion
	}
	if appReady && !frame.Ended && m.mode == "" {
		tracking := frame.Mouse.CellTracking()
		if tracking == ansi.ModeMouseAnyEvent {
			view.MouseMode = tea.MouseModeAllMotion
		} else if tracking != 0 || frame.Scrollable {
			view.MouseMode = tea.MouseModeCellMotion
		}
		if !frame.CursorHidden && frame.CursorX >= 0 && frame.CursorX < app.Dx() && frame.CursorY >= 0 && frame.CursorY < app.Dy() {
			view.Cursor = tea.NewCursor(app.Min.X+frame.CursorX, app.Min.Y+frame.CursorY)
		}
	}
	return view
}
