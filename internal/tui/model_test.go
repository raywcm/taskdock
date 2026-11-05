package tui

import (
	"context"
	"fmt"
	"image/color"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/raywcm/taskdock/internal/config"
	"github.com/raywcm/taskdock/internal/monitor"
	"github.com/raywcm/taskdock/internal/service"
	"github.com/raywcm/taskdock/internal/task"
)

type fakeBackend struct {
	settings         config.Settings
	records          []task.Record
	saved            bool
	deleted, stopped task.ID
	renamed          task.ID
	renamedName      string
	logData          []byte
	logged           task.ID
	loggedRaw        bool
}

func (f *fakeBackend) Move(_ context.Context, id task.ID, direction int) error {
	for i, r := range f.records {
		if r.Spec.ID == id {
			next := i + direction
			if next >= 0 && next < len(f.records) {
				f.records[i], f.records[next] = f.records[next], f.records[i]
			}
			break
		}
	}
	return nil
}
func (f *fakeBackend) List(context.Context) ([]task.Record, error) { return f.records, nil }
func (f *fakeBackend) Settings() (config.Settings, error)          { return f.settings, nil }
func (f *fakeBackend) SaveSettings(_ context.Context, s config.Settings) error {
	f.settings = s
	f.saved = true
	return nil
}
func (f *fakeBackend) Resources(context.Context, []task.Record) map[task.ID]task.Resources {
	return nil
}
func (f *fakeBackend) Stop(_ context.Context, id string) error { f.stopped = task.ID(id); return nil }
func (f *fakeBackend) Rename(_ context.Context, id, name string) error {
	suggestion := service.SuggestedName(f.records, name, task.ID(id))
	if suggestion != name {
		return &service.NameConflictError{Name: name, Suggestion: suggestion}
	}
	f.renamed = task.ID(id)
	f.renamedName = name
	for i := range f.records {
		if string(f.records[i].Spec.ID) == id {
			f.records[i].Spec.Name = name
		}
	}
	return nil
}
func (f *fakeBackend) Delete(_ context.Context, id string) error { f.deleted = task.ID(id); return nil }
func (f *fakeBackend) NameSuggestion(_ context.Context, name string, exclude task.ID) (string, error) {
	return service.SuggestedName(f.records, name, exclude), nil
}
func (f *fakeBackend) Monitor(context.Context, task.ID, task.Size) (*monitor.Session, error) {
	return nil, fmt.Errorf("monitor unavailable in unit backend")
}
func (f *fakeBackend) Logs(_ context.Context, id string, raw bool, _ int) ([]byte, error) {
	f.logged = task.ID(id)
	f.loggedRaw = raw
	if f.logData != nil {
		return f.logData, nil
	}
	return []byte("first\nlast\n"), nil
}
func record(t *testing.T, name string, state task.State) task.Record {
	t.Helper()
	id, _ := task.NewID()
	code := 0
	now := time.Now()
	runtime := &task.Runtime{SchemaVersion: 1, TaskID: id, State: state, UpdatedAt: now}
	if state == task.Exited {
		runtime.ExitCode = &code
	}
	return task.Record{Spec: task.Spec{SchemaVersion: 1, ID: id, Name: name, CreatedAt: now.Add(-time.Hour), Dir: "/tmp/project", Command: []string{"/bin/sh"}}, Runtime: runtime}
}
func readyModel(t *testing.T) model {
	f := &fakeBackend{settings: config.Defaults()}
	f.settings.Language = "en"
	f.records = []task.Record{record(t, "中文会话", task.Running), record(t, "other", task.Running), record(t, "completed", task.Exited)}
	m := newModel(context.Background(), f, nil, Options{Version: "0.2.0-test"})
	m.tasks = f.records
	m.ready = true
	return m
}
func press(m model, code rune) (model, tea.Cmd) {
	next, cmd := m.Update(tea.KeyPressMsg{Code: code})
	return next.(model), cmd
}
func noAction(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, nested := range msg {
			noAction(t, nested)
		}
	case changed, saved, checkedName, tea.QuitMsg:
		t.Fatalf("confirmation scheduled an application action: %T", msg)
	}
}

func TestTabsKeepIndependentSelectionAndActionsUseFullIDs(t *testing.T) {
	m := readyModel(t)
	m, _ = press(m, tea.KeyDown)
	selected := m.tasks[1].Spec.ID
	m, _ = press(m, tea.KeyRight)
	if m.tab != 1 || len(m.rows(m.tab)) != 1 {
		t.Fatal("right arrow did not select stopped sessions")
	}
	// Enter on history never tries to attach to an already ended process.
	m, cmd := press(m, tea.KeyEnter)
	if cmd == nil || m.selected.Kind != "" || m.mode != "history" {
		t.Fatal("did not open stopped-session history")
	}
	m, _ = press(m, tea.KeyEscape)
	m, _ = press(m, tea.KeyLeft)
	r, _ := m.current()
	if r.Spec.ID != selected {
		t.Fatal("lost selection across tab switch")
	}
	m, cmd = press(m, tea.KeyEnter)
	if cmd == nil || m.selected.ID != selected || m.selected.State.Tab != "running" {
		t.Fatal("short display ID leaked into actual attach identity")
	}
}
func TestSelectedSingleLineAndColumnsFitCJKViewport(t *testing.T) {
	m := readyModel(t)
	for _, size := range [][2]int{{100, 30}, {80, 24}, {44, 14}, {32, 12}, {20, 7}} {
		m.width, m.height = size[0], size[1]
		view := m.View().Content
		lines := strings.Split(view, "\n")
		if len(lines) > m.height {
			t.Fatalf("height overflow at %v: %d", size, len(lines))
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > m.width {
				t.Fatalf("width overflow %v: %q", size, line)
			}
		}
		if strings.Contains(ansi.Strip(view), string(m.tasks[0].Spec.ID)) {
			t.Fatal("displayed full 32-character ID")
		}
	}
	m.width, m.height = 100, 30
	card := m.card(m.tasks[0], 96, true)
	if len(strings.Split(card, "\n")) != 1 {
		t.Fatalf("selected row wrapped: %q", ansi.Strip(card))
	}
	if strings.Count(ansi.Strip(card), "│") != 2 || ansi.StringWidth(card) != 96 {
		t.Fatal("single-line frame lost side edges or previous horizontal width")
	}
	if !strings.Contains(card, "\x1b[1;7m") && !strings.Contains(card, "\x1b[7m") {
		t.Fatalf("selection not inverted: %q", card)
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "TaskDock") || strings.Contains(view, "v0.2.0-test") {
		t.Fatal("main header should show the brand and shortcuts without the version")
	}
}
func TestSettingsPreviewSaveAndCancel(t *testing.T) {
	m := readyModel(t)
	m, _ = press(m, ',')
	m, _ = press(m, tea.KeyLeft)
	if m.locale().Language != "zh-CN" {
		t.Fatal("language did not preview immediately")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "设置") {
		t.Fatal("settings not localized")
	}
	m, _ = press(m, tea.KeyEscape)
	if m.settings.Language != "en" || m.locale().Language != "en" {
		t.Fatal("cancel changed preferences")
	}
	m, _ = press(m, ',')
	m, _ = press(m, tea.KeyLeft)
	m, cmd := press(m, tea.KeyEnter)
	if cmd == nil {
		t.Fatal("save did not invoke persistence")
	}
	msg := cmd()
	next, _ := m.Update(msg)
	m = next.(model)
	f := m.catalog.(*fakeBackend)
	if !f.saved || f.settings.Language != "zh-CN" || m.mode != "settings" || m.tab != tabSettings || m.settingsEditing {
		t.Fatal("settings were not saved/applied")
	}
}
func TestStopAndDeleteTabs(t *testing.T) {
	m := readyModel(t)
	m, _ = press(m, 'x')
	if m.mode == "delete" {
		t.Fatal("offered stopped-session deletion for active task")
	}
	m, _ = press(m, tea.KeyRight)
	r, _ := m.current()
	m, _ = press(m, 'x')
	m, cmd := press(m, 'y')
	if cmd == nil {
		t.Fatal("missing delete action")
	}
	cmd()
	if m.catalog.(*fakeBackend).deleted != r.Spec.ID {
		t.Fatal("deleted a task from the wrong tab")
	}
}

func TestUnreachableRunningSessionCanBeDeletedWithConfirmation(t *testing.T) {
	for _, key := range []rune{tea.KeyDelete, tea.KeyBackspace, 'x'} {
		m := readyModel(t)
		m.tasks[0].Health = "unreachable"
		id := m.tasks[0].Spec.ID
		m, cmd := press(m, key)
		noAction(t, cmd)
		if m.mode != "delete" || m.target != id {
			t.Fatal("unreachable session was trapped behind Stop")
		}
		cancel, cmd := press(m, tea.KeyEnter)
		noAction(t, cmd)
		if cancel.mode != "" || m.catalog.(*fakeBackend).deleted != "" {
			t.Fatal("default confirmation deleted an unreachable session")
		}
		m, cmd = press(m, 'y')
		if cmd == nil {
			t.Fatal("confirmed deletion did not reach the backend lock check")
		}
		cmd()
		if m.catalog.(*fakeBackend).deleted != id {
			t.Fatal("deleted the wrong unreachable session")
		}
	}
}

func TestStopRequiresExplicitConfirmation(t *testing.T) {
	for _, key := range []rune{tea.KeyEscape, tea.KeyEnter, 'n', 's', 'q'} {
		m := readyModel(t)
		m, cmd := press(m, 's')
		f := m.catalog.(*fakeBackend)
		noAction(t, cmd)
		if m.busy || m.mode != "stop" || f.stopped != "" {
			t.Fatal("stop must wait for confirmation")
		}
		m, cmd = press(m, key)
		noAction(t, cmd)
		if m.mode != "" || m.busy || f.stopped != "" || m.tab != tabRunning {
			t.Fatalf("key %v did not cancel safely", key)
		}
	}
	m := readyModel(t)
	m, _ = press(m, tea.KeyRight)
	m, cmd := press(m, 's')
	if cmd != nil || m.mode != "" {
		t.Fatal("offered stop for a completed session")
	}
}

func TestStopConfirmationKeepsOriginalTargetAcrossRefresh(t *testing.T) {
	m := readyModel(t)
	original := m.tasks[0]
	m, _ = press(m, 's')
	// A refresh can move the cursor to a different record while the prompt is open.
	next, _ := m.Update(loaded{tasks: []task.Record{m.tasks[1]}, generation: m.generation})
	m = next.(model)
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, original.Spec.Name) || !strings.Contains(view, string(original.Spec.ID)[:8]) {
		t.Fatal("confirmation no longer identifies the original target")
	}
	m, cmd := press(m, 'y')
	if cmd == nil || !m.busy || m.mode != "" {
		t.Fatal("confirmation did not schedule stop")
	}
	msg := cmd()
	f := m.catalog.(*fakeBackend)
	if f.stopped != original.Spec.ID || f.deleted != "" {
		t.Fatal("confirmation stopped the wrong session or deleted its history")
	}
	next, _ = m.Update(msg)
	m = next.(model)
	if m.busy {
		t.Fatal("stop did not finish")
	}
	m.cancelLoad()
}
func TestDurationFreezesAndOldBrokerFallback(t *testing.T) {
	m := readyModel(t)
	r := m.tasks[2]
	a := elapsed(r, time.Now())
	b := elapsed(r, time.Now().Add(24*time.Hour))
	if a != b {
		t.Fatal("history duration keeps increasing")
	}
	active := m.tasks[0]
	if elapsed(active, time.Now().Add(time.Minute)) <= elapsed(active, time.Now()) {
		t.Fatal("running elapsed time froze")
	}
}

func TestConventionalKeysAndLegacyAliases(t *testing.T) {
	for _, key := range []rune{tea.KeyF2, 'e'} {
		m := readyModel(t)
		m, _ = press(m, key)
		if m.mode != "rename" || m.target != m.tasks[0].Spec.ID {
			t.Fatalf("%v did not rename selected session", key)
		}
		m, _ = press(m, tea.KeyEscape)
		if m.mode != "" || m.selected.Kind != "" {
			t.Fatal("Esc did not cancel rename")
		}
	}
	for _, key := range []rune{tea.KeyDelete, tea.KeyBackspace, 'x'} {
		m := readyModel(t)
		m, _ = press(m, key)
		if m.mode == "delete" {
			t.Fatal("delete offered for active session")
		}
		m, _ = press(m, tea.KeyRight)
		m, _ = press(m, key)
		if m.mode != "delete" || m.target != m.tasks[2].Spec.ID {
			t.Fatalf("%v did not confirm deletion of stopped session", key)
		}
	}
	for _, key := range []rune{tea.KeyF5, 'r'} {
		m := readyModel(t)
		m, cmd := press(m, key)
		if cmd == nil || !m.loading {
			t.Fatalf("%v did not refresh", key)
		}
		m.cancelLoad()
	}
	m := readyModel(t)
	m, _ = press(m, ',')
	if m.mode != "settings" {
		t.Fatal("comma did not open settings")
	}
}

func TestKeyboardHelpAndTabNavigation(t *testing.T) {
	for _, key := range []rune{tea.KeyF1, '?'} {
		m := readyModel(t)
		m, _ = press(m, key)
		if m.mode != "help" {
			t.Fatal("missing help page")
		}
		view := ansi.Strip(m.View().Content)
		for _, text := range []string{"F12", "Ctrl+B", "F2", "F5", "Delete", "Keyboard shortcuts"} {
			if !strings.Contains(view, text) {
				t.Fatalf("help missing %q", text)
			}
		}
		if strings.Contains(view, "Ctrl+D") || strings.Contains(view, "Ctrl+]") {
			t.Fatal("compatibility shortcuts should stay in the documentation")
		}
		m, _ = press(m, tea.KeyEscape)
		if m.mode != "" {
			t.Fatal("Esc did not close help")
		}
	}
	m := readyModel(t)
	m, _ = press(m, tea.KeyTab)
	if m.tab != 1 {
		t.Fatal("Tab did not switch labels")
	}
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	m = next.(model)
	if m.tab != 0 {
		t.Fatal("Shift+Tab did not switch back")
	}
	m, cmd := press(m, tea.KeyEscape)
	if cmd == nil {
		t.Fatal("Esc did not quit list")
	}
}

func TestSettingsIsThirdTabAndNavigationPreservesSelections(t *testing.T) {
	m := readyModel(t)
	m, _ = press(m, tea.KeyDown)
	selected := m.tasks[1].Spec.ID
	m, _ = press(m, tea.KeyRight)
	m, _ = press(m, tea.KeyRight)
	if m.tab != tabSettings || m.mode != "settings" || m.settingsEditing {
		t.Fatal("settings is not the third navigation tab")
	}
	if _, ok := m.current(); ok || len(m.rows(tabSettings)) != 0 || m.state().Selected != "" {
		t.Fatal("settings tab exposed a task action target")
	}
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "Running 2") || !strings.Contains(view, "Stop 1") || !strings.Contains(view, "Settings") {
		t.Fatal("settings lost the common tab strip", view)
	}
	m, _ = press(m, tea.KeyRight)
	r, _ := m.current()
	if m.tab != tabRunning || r.Spec.ID != selected {
		t.Fatal("three-tab cycle lost running selection")
	}
	m, _ = press(m, tea.KeyLeft)
	if m.tab != tabSettings {
		t.Fatal("left did not wrap to settings")
	}
	m, _ = press(m, tea.KeyLeft)
	if m.tab != tabStop {
		t.Fatal("settings navigation arrow edited instead of switching tab")
	}
}

func TestLeavingSettingsDiscardsPreviewAndHelpReturnsToSettings(t *testing.T) {
	m := readyModel(t)
	m, _ = press(m, tea.KeyLeft)
	m, _ = press(m, tea.KeyEnter)
	m, _ = press(m, tea.KeyLeft)
	if m.locale().Language != "zh-CN" {
		t.Fatal("edit did not preview")
	}
	m, _ = press(m, tea.KeyTab)
	if m.tab != tabRunning || m.locale().Language != "en" || m.settingsEditing || m.catalog.(*fakeBackend).saved {
		t.Fatal("leaving settings persisted or retained unsaved preview")
	}
	m, _ = press(m, tea.KeyLeft)
	m, _ = press(m, tea.KeyF1)
	m, _ = press(m, tea.KeyEscape)
	if m.tab != tabSettings || m.mode != "settings" || m.settingsEditing {
		t.Fatal("help did not return to settings navigation")
	}
	m, _ = press(m, tea.KeyEscape)
	if m.tab != tabRunning {
		t.Fatal("settings Esc did not return to previous list")
	}
}

func TestRefreshOnSettingsDoesNotIndexTaskCursor(t *testing.T) {
	m := readyModel(t)
	m.initial = m.tasks[1].Spec.ID
	m, _ = press(m, tea.KeyLeft)
	next, _ := m.Update(loaded{tasks: m.tasks, generation: m.generation})
	m = next.(model)
	if m.tab != tabSettings || m.state().Tab != "settings" {
		t.Fatal("refresh left settings")
	}
	for _, size := range [][2]int{{80, 24}, {44, 14}, {32, 12}} {
		m.width, m.height = size[0], size[1]
		for _, line := range strings.Split(m.View().Content, "\n") {
			if ansi.StringWidth(line) > m.width {
				t.Fatal("settings tab overflow", size)
			}
		}
	}
}

func TestCompactListKeepsSelectionVisibleWhileScrolling(t *testing.T) {
	m := readyModel(t)
	m.tasks = nil
	for i := range 30 {
		m.tasks = append(m.tasks, record(t, fmt.Sprintf("session-%02d", i), task.Running))
	}
	for _, size := range [][2]int{{80, 24}, {44, 14}} {
		m.width, m.height = size[0], size[1]
		for _, index := range []int{0, 10, 29} {
			m.cursors[tabRunning] = index
			view := ansi.Strip(m.View().Content)
			if !strings.Contains(view, fmt.Sprintf("session-%02d", index)) || !strings.Contains(view, "│") {
				t.Fatalf("selection disappeared at %v, item %d", size, index)
			}
			if len(strings.Split(view, "\n")) > m.height {
				t.Fatal("viewport height exceeded")
			}
		}
	}
}

func TestSingleLineHelpAndAllLanguages(t *testing.T) {
	for _, option := range config.Languages() {
		m := readyModel(t)
		m.settings.Language = option.Code
		m.width, m.height = 160, 24
		for _, width := range []int{22, 44, 76, 116, 156} {
			line := m.helpLine(width)
			if strings.Contains(line, "\n") || ansi.StringWidth(line) > width {
				t.Fatalf("%s help exceeded one line at %d", option.Code, width)
			}
		}
		full := ansi.Strip(m.helpLine(500))
		for _, key := range []string{"←→", "↑↓", "Enter", "v", "n", "Ctrl+F", "?", "q"} {
			if !strings.Contains(full, key) {
				t.Fatalf("%s help lost %s", option.Code, key)
			}
		}
		m, _ = press(m, ',')
		if !strings.Contains(ansi.Strip(m.View().Content), config.LanguageName(option.Code)) {
			t.Fatal("header/setting language name wrong", option.Code)
		}
		view := ansi.Strip(m.View().Content)
		if !strings.Contains(view, "v0.2.0-test") {
			t.Fatal("version missing from settings")
		}
		if strings.Contains(view, "RSS") || strings.Contains(view, "metrics_hint") || strings.Contains(view, "100%") {
			t.Fatal("technical metrics explanation still shown")
		}
		for _, width := range []int{32, 80, 160} {
			m.width = width
			for _, line := range strings.Split(m.View().Content, "\n") {
				if ansi.StringWidth(line) > width {
					t.Fatalf("%s setting overflow at %d", option.Code, width)
				}
			}
		}
	}
}

func TestRetentionSettingPreviewsAndSaves(t *testing.T) {
	m := readyModel(t)
	m, _ = press(m, ',')
	m, _ = press(m, tea.KeyUp)
	if m.settingRow != settingsCount-1 || m.draft.HistoryRetentionSeconds != 3600 {
		t.Fatal("retention setting not available")
	}
	m, _ = press(m, tea.KeyLeft)
	if m.draft.HistoryRetentionSeconds != 1800 || m.settings.HistoryRetentionSeconds != 3600 {
		t.Fatal("retention preview changed committed policy")
	}
	m, cmd := press(m, tea.KeyEnter)
	next, _ := m.Update(cmd())
	m = next.(model)
	if m.settings.HistoryRetentionSeconds != 1800 || m.catalog.(*fakeBackend).settings.HistoryRetentionSeconds != 1800 {
		t.Fatal("retention did not persist")
	}
	m, _ = press(m, tea.KeyEnter)
	m.settingRow = settingsCount - 1
	m.draft.HistoryRetentionSeconds = 300
	m, _ = press(m, tea.KeyLeft)
	if m.draft.HistoryRetentionSeconds != 0 || m.retentionLabel(0) != "Never" {
		t.Fatal("cannot disable cleanup")
	}
	m, _ = press(m, tea.KeyEscape)
	if m.settings.HistoryRetentionSeconds != 1800 || m.draft.HistoryRetentionSeconds != 1800 {
		t.Fatal("cancel changed retention")
	}
}

func TestConnectionChoicesAndTakeoverConfirmation(t *testing.T) {
	m := readyModel(t)
	original := m.tasks[0]
	m, cmd := press(m, 'c')
	if cmd != nil || m.mode != "connect" || m.connectChoice != 2 {
		t.Fatal("connection menu must default to shared control")
	}
	m, cmd = press(m, tea.KeyEnter)
	if cmd == nil || m.selected.Mode != "shared" || m.selected.ID != original.Spec.ID {
		t.Fatal("shared choice did not attach the selected full ID")
	}
	m = readyModel(t)
	m, _ = press(m, 'c')
	m, _ = press(m, tea.KeyUp)
	m, cmd = press(m, tea.KeyEnter)
	if cmd == nil || m.selected.Mode != "readonly" {
		t.Fatal("read-only connection choice missing")
	}
	for _, cancel := range []rune{tea.KeyEscape, tea.KeyEnter, 'n'} {
		m = readyModel(t)
		m, _ = press(m, 'c')
		m, _ = press(m, tea.KeyDown)
		m, cmd = press(m, tea.KeyEnter)
		noAction(t, cmd)
		if m.mode != "takeover" {
			t.Fatal("takeover skipped confirmation")
		}
		m, cmd = press(m, cancel)
		noAction(t, cmd)
		if m.selected.Kind != "" || m.mode != "" {
			t.Fatal("takeover cancellation had side effects")
		}
	}
	m = readyModel(t)
	original = m.tasks[0]
	m, _ = press(m, 'c')
	m, _ = press(m, tea.KeyDown)
	m, _ = press(m, tea.KeyEnter)
	next, _ := m.Update(loaded{tasks: []task.Record{m.tasks[1]}, generation: m.generation})
	m = next.(model)
	m, cmd = press(m, 'y')
	if cmd == nil || m.selected.Mode != "takeover" || m.selected.ID != original.Spec.ID {
		t.Fatal("takeover lost its target during refresh")
	}
}

func TestBusyConnectionOpensSafeLocalizedMenu(t *testing.T) {
	base := readyModel(t)
	spec := base.tasks[0].Spec
	m := newModel(context.Background(), base.catalog, fmt.Errorf("busy"), Options{Conflict: &spec})
	if m.mode != "connect" || m.target != spec.ID || m.connectChoice != 2 || m.err != nil {
		t.Fatal("busy connection did not open safe choices")
	}
	view := ansi.Strip(m.View().Content)
	for _, text := range []string{"Connection modes", "Take over", "Read only", "Shared control", spec.Name, string(spec.ID)[:8]} {
		if !strings.Contains(view, text) {
			t.Fatalf("connection menu missing %q", text)
		}
	}
	m, _ = press(m, tea.KeyEscape)
	if m.mode != "" || m.selected.Kind != "" {
		t.Fatal("Esc did not cancel connection choices")
	}
	m = readyModel(t)
	m, _ = press(m, tea.KeyRight)
	m, cmd := press(m, 'c')
	if cmd != nil || m.mode != "" {
		t.Fatal("offered connections to a completed task")
	}
}

func TestNumberedNameRequiresConfirmationForNewAndRename(t *testing.T) {
	for _, kind := range []string{"new", "rename"} {
		m := readyModel(t)
		target := m.tasks[0].Spec.ID
		key := rune('n')
		if kind == "rename" {
			key = tea.KeyF2
		}
		m, _ = press(m, key)
		m.input.SetValue("other")
		m, cmd := press(m, tea.KeyEnter)
		if cmd == nil || !m.busy {
			t.Fatal("name was not checked")
		}
		next, _ := m.Update(cmd())
		m = next.(model)
		if m.mode != "name-conflict" || m.proposedName != "other-2" || m.selected.Kind != "" {
			t.Fatal("missing numbered name confirmation")
		}
		view := ansi.Strip(m.View().Content)
		if !strings.Contains(view, "╭") || !strings.Contains(view, "╰") || !strings.Contains(view, "Name already exists") || !strings.Contains(view, "other-2") {
			t.Fatalf("name conflict is not an obvious dialog: %s", view)
		}
		m, _ = press(m, tea.KeyEnter)
		if m.mode != kind || m.catalog.(*fakeBackend).renamed != "" || m.selected.Kind != "" {
			t.Fatal("default Enter must cancel the numbered name")
		}
		m, cmd = press(m, tea.KeyEnter)
		next, _ = m.Update(cmd())
		m = next.(model)
		m, cmd = press(m, 'y')
		if cmd == nil {
			t.Fatal("confirmation did not submit")
		}
		if kind == "new" {
			if m.selected.Kind != "new" || m.selected.Name != "other-2" {
				t.Fatal("new did not submit the exact reviewed suggestion")
			}
		} else {
			cmd()
			f := m.catalog.(*fakeBackend)
			if f.renamed != target || f.renamedName != "other-2" {
				t.Fatal("rename did not submit the exact reviewed target/name")
			}
		}
	}
}
func TestConfirmationButtonsDefaultCancelAndCanSelectAction(t *testing.T) {
	m := readyModel(t)
	m, _ = press(m, 's')
	if m.confirmChoice != 0 {
		t.Fatal("destructive action selected by default")
	}
	m, _ = press(m, tea.KeyRight)
	if m.confirmChoice != 1 || m.mode != "stop" {
		t.Fatal("arrow did not select the action button")
	}
	m, cmd := press(m, tea.KeyEnter)
	if cmd == nil {
		t.Fatal("Enter did not activate the selected button")
	}
	cmd()
	if m.catalog.(*fakeBackend).stopped != m.tasks[0].Spec.ID {
		t.Fatal("confirmed the wrong session")
	}
	for _, size := range [][2]int{{100, 30}, {80, 24}, {44, 14}, {26, 9}} {
		m = readyModel(t)
		m.width, m.height = size[0], size[1]
		m, _ = press(m, 's')
		lines := strings.Split(m.View().Content, "\n")
		if len(lines) > m.height {
			t.Fatalf("dialog height overflow: %v", size)
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > m.width {
				t.Fatalf("dialog width overflow: %v", size)
			}
		}
	}
}

func TestEmptyNameInputFitsInsideDialogWithoutWrapping(t *testing.T) {
	m := readyModel(t)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(model)
	m, _ = press(m, 'n')
	if view := ansi.Strip(m.View().Content); strings.Contains(view, "…") {
		t.Fatalf("empty input wrapped or was truncated inside its dialog: %s", view)
	}
}
func TestMonitorSplitSelectionFailureAndReturn(t *testing.T) {
	m := readyModel(t)
	m.width, m.height = 110, 28
	m, cmd := press(m, 'v')
	if !m.monitoring || cmd == nil || m.monitorID != m.tasks[0].Spec.ID {
		t.Fatal("v did not open the selected monitor")
	}
	m.monitorFrame = monitor.Frame{Ready: true, Rows: []string{"RIGHT-SCREEN", "LIVE-OUTPUT"}}
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "中文会话") || !strings.Contains(view, "other") || !strings.Contains(view, "RIGHT-SCREEN") || !strings.Contains(view, " │ ") {
		t.Fatalf("split view is missing list or screen: %s", view)
	}
	m, cmd = press(m, tea.KeyDown)
	if cmd == nil || m.monitorID != m.tasks[1].Spec.ID || m.monitorFrame.Ready {
		t.Fatal("selection retained the old screen")
	}
	next, _ := m.Update(cmd())
	m = next.(model)
	if !strings.Contains(ansi.Strip(m.View().Content), "Unable to monitor") {
		t.Fatal("failed connection did not show an explicit message")
	}
	m, _ = press(m, tea.KeyEscape)
	if m.monitoring || m.monitorCancel != nil || m.monitorID != "" {
		t.Fatal("Esc did not leave/release monitor")
	}
	m, _ = press(m, tea.KeyRight)
	m, cmd = press(m, 'v')
	if m.monitoring || cmd != nil {
		t.Fatal("monitor offered outside Running")
	}
}

func TestMonitorThemeDoesNotRecolorApplicationCells(t *testing.T) {
	for _, theme := range []string{"light", "dark"} {
		m := readyModel(t)
		m.settings.Theme = theme
		m.width, m.height = 80, 24
		m.monitoring = true
		m.monitorFrame = monitor.Frame{Ready: true, Rows: []string{
			"PLAIN \x1b[7mREVERSE\x1b[0m \x1b[38;2;12;34;56mRGB\x1b[0m \x1b[38;5;202mINDEX\x1b[0m",
		}}
		line := strings.Split(m.View().Content, "\n")[5]
		screen := uv.NewScreenBuffer(m.width, 1)
		uv.NewStyledString(line).Draw(screen, uv.Rect(0, 0, m.width, 1))
		cells := screen.Buffer.Lines[0]
		left, _, _ := m.monitorGeometry()
		x := left + 5
		for _, test := range []struct {
			x     int
			style uv.Style
		}{{x, uv.Style{}}, {x + 6, uv.Style{Attrs: uv.AttrReverse}}, {x + 14, uv.Style{Fg: color.RGBA{R: 12, G: 34, B: 56, A: 255}}}, {x + 18, uv.Style{Fg: ansi.IndexedColor(202)}}} {
			if !cells[test.x].Style.Equal(&test.style) {
				t.Fatalf("%s theme recolored app cell %d: %+v", theme, test.x, cells[test.x].Style)
			}
		}
		if cells[0].Style.Bg == nil || cells[len(cells)-1].Style.Bg == nil {
			t.Fatal("application defaults escaped into the manager gutters")
		}
	}
}

func TestStoppedHistoryScrollAndReturn(t *testing.T) {
	m := readyModel(t)
	m, _ = press(m, tea.KeyRight)
	f := m.catalog.(*fakeBackend)
	var rows []string
	for i := 0; i < 80; i++ {
		rows = append(rows, fmt.Sprintf("LINE-%03d %s", i, strings.Repeat("x", 100)))
	}
	f.logData = []byte(strings.Join(rows, "\n") + "\n")
	id := m.tasks[2].Spec.ID
	m, cmd := press(m, tea.KeyEnter)
	if m.mode != "history" || cmd == nil || m.selected.Kind != "" {
		t.Fatal("stopped Enter did not open history in place")
	}
	next, _ := m.Update(cmd())
	m = next.(model)
	if f.logged != id || f.loggedRaw || f.stopped != "" || f.deleted != "" {
		t.Fatal("history did not use safe logs for the exact record")
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "Output history") || !strings.Contains(view, "LINE-079") {
		t.Fatal("history did not start at the latest output")
	}
	m, _ = press(m, tea.KeyHome)
	if m.history.y != 0 || !strings.Contains(ansi.Strip(m.View().Content), "LINE-000") {
		t.Fatal("Home did not reach the first retained line")
	}
	m, _ = press(m, tea.KeyPgDown)
	if m.history.y == 0 {
		t.Fatal("page down did not scroll")
	}
	m, _ = press(m, tea.KeyRight)
	if m.history.x == 0 {
		t.Fatal("history cannot pan long lines")
	}
	m.width = 200
	m.history.x, m.history.y = 0, 0
	m.history.lines = []string{strings.Repeat("x", 150) + "WIDE-HISTORY"}
	if !strings.Contains(ansi.Strip(m.View().Content), "WIDE-HISTORY") {
		t.Fatal("history clipped a long line despite available terminal width")
	}
	m, _ = press(m, tea.KeyEscape)
	if m.mode != "" || m.tab != tabStop || m.selected.Kind != "" || m.history.cancel != nil {
		t.Fatal("history did not return to Stop")
	}
	current, _ := m.current()
	if current.Spec.ID != id {
		t.Fatal("history lost the original selection")
	}
}
func TestHistorySanitizationAndStaleRead(t *testing.T) {
	m := readyModel(t)
	m, _ = press(m, tea.KeyRight)
	m.catalog.(*fakeBackend).logData = []byte("中文\r\nnext\rtab\tend\u009b[2J\x00\n")
	m, cmd := press(m, tea.KeyEnter)
	msg := cmd().(historyLoaded)
	if strings.Contains(strings.Join(msg.lines, "\n"), "\u009b") || strings.Contains(strings.Join(msg.lines, "\n"), "\r") || strings.Contains(strings.Join(msg.lines, "\n"), "\x00") {
		t.Fatal("history left unsafe controls")
	}
	m, _ = press(m, tea.KeyEscape)
	next, _ := m.Update(msg)
	m = next.(model)
	if m.mode != "" || len(m.history.lines) != 0 {
		t.Fatal("late history read reopened or changed the list")
	}
}
func TestMonitorHorizontalKeysDoNothing(t *testing.T) {
	m := readyModel(t)
	m, _ = press(m, 'v')
	defer m.closeMonitor()
	id, generation := m.monitorID, m.monitorGeneration
	for _, key := range []rune{tea.KeyLeft, tea.KeyRight, 'h', 'l'} {
		var cmd tea.Cmd
		m, cmd = press(m, key)
		if cmd != nil || !m.monitoring || m.tab != tabRunning || m.monitorID != id || m.monitorGeneration != generation {
			t.Fatalf("monitor key %v changed page, selection or connection", key)
		}
	}
	// Confirmation dialogs must retain their own left/right button navigation.
	m, _ = press(m, 's')
	m, _ = press(m, tea.KeyRight)
	if m.mode != "stop" || m.confirmChoice != 1 {
		t.Fatal("monitor suppression broke confirmation buttons")
	}
}

func TestContextHintsStayInTitleRow(t *testing.T) {
	for _, language := range config.LanguageCodes() {
		for _, width := range []int{44, 80, 160} {
			for _, mode := range []string{"", "help", "search", "history", "connect", "settings", "monitor"} {
				m := readyModel(t)
				m.settings.Language = language
				m.width = width
				m.mode = mode
				key := "Esc"
				switch mode {
				case "":
					key = "v"
				case "monitor":
					m.mode = ""
					m.monitoring = true
					key = "Shift+"
				case "settings":
					m.tab = tabSettings
					m.draft = m.settings
				case "history":
					key = "↑"
				}
				first := strings.Split(ansi.Strip(m.View().Content), "\n")[0]
				if !strings.Contains(first, "TaskDock") || !strings.Contains(first, key) || ansi.StringWidth(first) > width {
					t.Fatalf("%s width %d mode %s lost title hint: %q", language, width, mode, first)
				}
			}
		}
	}
}

func TestShiftMoveKeepsSelectedID(t *testing.T) {
	m := readyModel(t)
	selected := m.tasks[0].Spec.ID
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift})
	m = next.(model)
	if cmd == nil || !m.busy {
		t.Fatal("Shift+Down did not schedule a move")
	}
	next, cmd = m.Update(cmd())
	m = next.(model)
	next, _ = m.Update(cmd())
	m = next.(model)
	current, ok := m.current()
	if !ok || current.Spec.ID != selected || m.cursors[0] != 1 {
		t.Fatal("moved selection lost its ID")
	}
}

func TestDialogTitleHintsMatchTheDialogKeys(t *testing.T) {
	for _, code := range config.LanguageCodes() {
		m := readyModel(t)
		m.settings.Language = code
		for _, mode := range []string{"new", "rename", "stop", "delete", "takeover", "name-conflict"} {
			m.mode = mode
			expected := m.locale().Text("confirm_hint")
			if mode == "new" || mode == "rename" {
				expected = m.locale().Text("edit_hint")
			}
			if got := ansi.Strip(m.helpLine(300)); got != expected {
				t.Fatalf("%s %s: %q, expected %q", code, mode, got, expected)
			}
		}
	}
}

func TestAutomaticThemeMatchesExplicitTheme(t *testing.T) {
	t.Setenv("COLORFGBG", "")
	for _, test := range []struct {
		background color.Color
		theme      string
	}{{color.White, "light"}, {color.Black, "dark"}} {
		m := readyModel(t)
		m.settings.Theme = "auto"
		next, _ := m.Update(tea.BackgroundColorMsg{Color: test.background})
		m = next.(model)
		if m.resolvedTheme() != test.theme {
			t.Fatal("automatic background chose the wrong theme")
		}
		manual := m
		manual.settings.Theme = test.theme
		if m.palette().accent.GetForeground() != manual.palette().accent.GetForeground() || m.View().Content != manual.View().Content {
			t.Fatal("automatic and explicit palette differ")
		}
	}
	m := readyModel(t)
	m.settings.Theme = "light"
	next, _ := m.Update(tea.BackgroundColorMsg{Color: color.Black})
	m = next.(model)
	if m.resolvedTheme() != "light" {
		t.Fatal("terminal background overrode the explicit choice")
	}
}
