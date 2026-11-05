package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/raywcm/taskdock/internal/config"
	"github.com/raywcm/taskdock/internal/i18n"
	"github.com/raywcm/taskdock/internal/ipc"
	"github.com/raywcm/taskdock/internal/monitor"
	"github.com/raywcm/taskdock/internal/protocol"
	"github.com/raywcm/taskdock/internal/service"
	"github.com/raywcm/taskdock/internal/task"
)

const (
	tabRunning = iota
	tabStop
	tabSettings
	tabCount
)

const settingsCount = 6

var tabNames = [...]string{"running", "stop", "settings"}

type Backend interface {
	service.TaskList
	Stop(context.Context, string) error
	Rename(context.Context, string, string) error
	Delete(context.Context, string) error
	Settings() (config.Settings, error)
	SaveSettings(context.Context, config.Settings) error
	Resources(context.Context, []task.Record) map[task.ID]task.Resources
	NameSuggestion(context.Context, string, task.ID) (string, error)
	Monitor(context.Context, task.ID, task.Size) (*monitor.Session, error)
	Logs(context.Context, string, bool, int) ([]byte, error)
	Move(context.Context, task.ID, int) error
}
type State struct {
	Tab      string
	Selected task.ID
	Monitor  bool
}
type Options struct {
	Version      string
	State        State
	Conflict     *task.Spec
	NameConflict *service.NameConflictError
}
type Selection struct {
	Kind  string
	ID    task.ID
	Name  string
	State State
	Mode  string
}
type loaded struct {
	tasks      []task.Record
	err        error
	generation uint64
}
type changed struct{ err error }
type saved struct {
	settings config.Settings
	err      error
}
type tick time.Time
type exitRequested struct{}

type model struct {
	ctx                                  context.Context
	catalog                              Backend
	tasks                                []task.Record
	cursors                              [2]int
	tab                                  int
	err                                  error
	selected                             Selection
	ready                                bool
	width, height                        int
	mode                                 string
	target                               task.ID
	targetName                           string
	connectChoice                        int
	confirmChoice                        int
	nameKind, proposedName, conflictName string
	monitoring                           bool
	monitorSession                       *monitor.Session
	monitorCancel                        context.CancelFunc
	monitorID                            task.ID
	monitorGeneration                    uint64
	monitorFrame                         monitor.Frame
	monitorInput                         *monitorInput
	searchCursor                         int
	repaintPending                       bool
	quitting                             bool
	history                              historyState
	input                                textinput.Model
	busy, loading                        bool
	settings, draft                      config.Settings
	settingRow                           int
	settingsEditing                      bool
	lastTaskTab                          int
	version                              string
	initial                              task.ID
	now, lastLoad                        time.Time
	generation                           uint64
	cancelLoad                           context.CancelFunc
	message                              string
	backgroundKnown, darkBackground      bool
}

func newModel(ctx context.Context, catalog Backend, notice error, options Options) model {
	settings, err := catalog.Settings()
	if err != nil {
		settings = config.Defaults()
		if notice == nil {
			notice = fmt.Errorf("settings: %w", err)
		}
	}
	edit := textinput.New()
	edit.CharLimit = 128
	m := model{ctx: ctx, catalog: catalog, input: edit, width: 80, height: 24, settings: settings, err: notice, version: options.Version, initial: options.State.Selected, now: time.Now()}
	tab := options.State.Tab
	if tab == "" {
		tab = settings.DefaultTab
	}
	if tab == "stop" {
		m.tab = tabStop
	}
	m.lastTaskTab = m.tab
	m.monitoring = options.State.Monitor && m.tab == tabRunning
	if tab == "settings" {
		m.tab = tabSettings
		m.mode = "settings"
		m.draft = m.settings
	}
	if options.Conflict != nil {
		m.mode = "connect"
		m.target = options.Conflict.ID
		m.targetName = options.Conflict.Name
		m.connectChoice = 2
		m.err = nil
		m.message = m.locale().Text("busy_hint")
	}
	if options.NameConflict != nil {
		m.nameKind = "new"
		m.conflictName = options.NameConflict.Name
		m.proposedName = options.NameConflict.Suggestion
		m.input.SetValue(m.conflictName)
		m.mode = "name-conflict"
		m.err = nil
	}
	return m
}

var connectionModes = [...]string{protocol.AttachTakeover, protocol.AttachReadOnly, protocol.AttachShared}

func (m model) connectionSelection() Selection {
	return Selection{Kind: "attach", ID: m.target, State: State{Tab: "running", Selected: m.target, Monitor: m.monitoring}, Mode: connectionModes[m.connectChoice]}
}
func (m model) locale() i18n.Locale {
	if m.mode == "settings" {
		return i18n.New(m.draft.Language)
	}
	return i18n.New(m.settings.Language)
}
func active(r task.Record) bool {
	return r.Runtime != nil && (r.Runtime.State == task.Running || r.Runtime.State == task.Starting || r.Runtime.State == task.Stopping)
}
func (m model) rows(tab int) []task.Record {
	if tab < tabRunning || tab > tabStop {
		return nil
	}
	rows := make([]task.Record, 0, len(m.tasks))
	for _, r := range m.tasks {
		if active(r) == (tab == 0) {
			rows = append(rows, r)
		}
	}
	return rows
}
func (m model) current() (task.Record, bool) {
	if m.tab == tabSettings {
		return task.Record{}, false
	}
	rows := m.rows(m.tab)
	i := m.cursors[m.tab]
	if i < 0 || i >= len(rows) {
		return task.Record{}, false
	}
	return rows[i], true
}
func (m model) state() State {
	s := State{Tab: tabNames[m.tab], Monitor: m.monitoring}
	if r, ok := m.current(); ok {
		s.Selected = r.Spec.ID
	}
	return s
}

// selectTab cancels unsaved previews and keeps task-list cursors independent.
func (m *model) selectTab(tab int) {
	if m.busy {
		return
	}
	if m.tab != tabSettings {
		m.lastTaskTab = m.tab
	}
	m.tab = (tab + tabCount) % tabCount
	if m.tab != tabRunning {
		m.monitoring = false
		m.closeMonitor()
	}
	m.mode = ""
	m.settingsEditing = false
	m.draft = m.settings
	m.err = nil
	m.message = ""
	if m.tab == tabSettings {
		m.mode = "settings"
	}
}

func (m *model) startLoad() tea.Cmd {
	if m.cancelLoad != nil {
		m.cancelLoad()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancelLoad = cancel
	m.generation++
	generation := m.generation
	m.loading = true
	m.lastLoad = m.now
	showResources := m.settings.ShowResources
	return func() tea.Msg {
		rows, err := m.catalog.List(ctx)
		if err == nil && showResources {
			samples := m.catalog.Resources(ctx, rows)
			for i := range rows {
				if sample, ok := samples[rows[i].Spec.ID]; ok {
					rows[i].Resources = &sample
				}
			}
		}
		return loaded{rows, err, generation}
	}
}
func tickCmd() tea.Cmd { return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tick(t) }) }
func (m model) Init() tea.Cmd {
	return tea.Batch(func() tea.Msg { return tick(time.Now()) }, tea.RequestBackgroundColor)
}
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	updated := next.(model)
	if updated.monitorInput != nil {
		updated.monitorInput.Arm(updated.tab == tabRunning && !updated.monitoring && updated.mode == "" && !updated.busy)
	}
	// Switching modal geometry over a live pane can confuse hardware-scroll
	// diffing. Invalidate once at this boundary; ordinary monitor frames still
	// use incremental rendering. Never queue a repaint when leaving for attach.
	repaint := m.repaintPending && !updated.busy
	if m.monitoring != updated.monitoring || ((m.monitoring || updated.monitoring) && m.mode != updated.mode) {
		repaint = true
	}
	if updated.selected.Kind == "" && m.mode != updated.mode && (m.hasDialog() || updated.hasDialog()) {
		if updated.busy {
			updated.repaintPending = true
		} else {
			repaint = true
		}
	}
	if repaint && updated.selected.Kind == "" {
		updated.repaintPending = false
		cmd = tea.Batch(cmd, tea.ClearScreen)
	}
	return updated, cmd
}
func (m model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		if msg.Color != nil {
			m.backgroundKnown, m.darkBackground = true, msg.IsDark()
			return m, tea.ClearScreen
		}
		return m, nil
	case monitorAction:
		if msg.kind == "background" {
			return m.update(msg.background)
		}
		if msg.ready != nil {
			defer close(msg.ready)
		}
		if !m.monitoring || (msg.session != nil && msg.session != m.monitorSession) {
			return m, nil
		}
		switch msg.kind {
		case "search":
			cmd := m.openSearch()
			return m.searchTypeahead(msg.raw, cmd)
		case "next", "previous":
			step := 1
			if msg.kind == "previous" {
				step = -1
			}
			m.cursors[0] = min(max(0, len(m.rows(0))-1), max(0, m.cursors[0]+step))
			current, ok := m.current()
			if ok && current.Spec.ID == m.monitorID && m.monitorInput != nil && msg.session == m.monitorSession {
				m.monitorInput.Begin()
				m.monitorInput.SetTarget(m.monitorSession)
			}
			return m, m.syncMonitor()
		case "back", "input-error":
			m.monitoring = false
			m.closeMonitor()
			if msg.err != nil {
				m.err = msg.err
			}
			return m, nil
		}
		return m, nil
	case exitRequested:
		return m.quit()
	case historyLoaded:
		if m.mode != "history" || msg.generation != m.history.generation {
			return m, nil
		}
		m.history.ready = true
		m.history.err = msg.err
		m.history.lines = msg.lines
		m.history.maxWidth = msg.maxWidth
		m.history.y = max(0, len(msg.lines)-m.historyHeight())
		return m, tea.ClearScreen
	case checkedName:
		m.busy = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		if msg.suggested != msg.name {
			m.nameKind = msg.kind
			m.conflictName = msg.name
			m.proposedName = msg.suggested
			m.mode = "name-conflict"
			m.confirmChoice = 0
			return m, nil
		}
		return m.submitName(msg.kind, msg.name)
	case monitorOpened:
		if msg.generation != m.monitorGeneration || !m.monitoring {
			if msg.session != nil {
				msg.session.Close()
			}
			return m, nil
		}
		m.monitorSession = msg.session
		if msg.err != nil {
			m.monitorFrame.Err = msg.err
			if m.monitorInput != nil {
				m.monitorInput.Disable()
			}
		} else if msg.session != nil {
			if m.monitorInput != nil {
				m.monitorInput.SetTarget(msg.session)
			}
			msg.session.Resize(m.monitorSize())
			return m, monitorTickCmd(m.monitorGeneration)
		}
		return m, nil
	case monitorTick:
		if !m.monitoring || msg.generation != m.monitorGeneration {
			return m, nil
		}
		if m.monitorSession != nil {
			m.monitorFrame = m.monitorSession.Snapshot()
			var remote *ipc.RemoteError
			if errors.As(m.monitorFrame.Err, &remote) && remote.Code == "taken_over" {
				m.monitoring = false
				m.closeMonitor()
				m.message = m.locale().Text("taken_over")
				return m, m.startLoad()
			}
			if m.monitorFrame.Err != nil && m.monitorInput != nil {
				m.monitorInput.Disable()
			}
			if m.monitorFrame.Ended {
				m.monitoring = false
				m.closeMonitor()
				return m, m.startLoad()
			}
		}
		return m, monitorTickCmd(m.monitorGeneration)
	case loaded:
		if msg.generation != m.generation {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			m.ready = true
			return m, nil
		}
		var ids [2]task.ID
		for tab := range 2 {
			rows := m.rows(tab)
			if m.cursors[tab] < len(rows) {
				ids[tab] = rows[m.cursors[tab]].Spec.ID
			}
		}
		m.tasks, m.ready = msg.tasks, true
		m.now = time.Now()
		if m.initial != "" && m.tab != tabSettings {
			ids[m.tab] = m.initial
			m.initial = ""
		}
		for tab := range 2 {
			rows := m.rows(tab)
			m.cursors[tab] = min(m.cursors[tab], max(0, len(rows)-1))
			for i, r := range rows {
				if r.Spec.ID == ids[tab] {
					m.cursors[tab] = i
					break
				}
			}
		}
		return m, m.syncMonitor()
	case changed:
		m.busy = false
		m.err = msg.err
		var conflict *service.NameConflictError
		if errors.As(msg.err, &conflict) && m.nameKind == "rename" {
			m.mode = "name-conflict"
			m.conflictName = conflict.Name
			m.proposedName = conflict.Suggestion
			m.confirmChoice = 0
			m.err = nil
		}
		return m, m.startLoad()
	case saved:
		m.busy = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.settings = msg.settings
		m.draft = msg.settings
		m.settingsEditing = false
		m.err = nil
		m.message = m.locale().Text("saved")
		return m, m.startLoad()
	case tick:
		m.now = time.Time(msg)
		var cmd tea.Cmd
		if !m.busy && !m.loading && (m.lastLoad.IsZero() || m.now.Sub(m.lastLoad) >= time.Duration(m.settings.RefreshSeconds)*time.Second) {
			cmd = m.startLoad()
		}
		return m, tea.Batch(cmd, tickCmd())
	case tea.WindowSizeMsg:
		changed := m.width != msg.Width || m.height != msg.Height
		m.width, m.height = max(1, msg.Width), max(1, msg.Height)
		m.input.SetWidth(max(4, min(68, m.width-6)-9))
		if m.monitorSession != nil {
			m.monitorSession.Resize(m.monitorSize())
		}
		cmd := m.syncMonitor()
		if changed {
			cmd = tea.Batch(cmd, tea.ClearScreen)
		}
		return m, cmd
	case tea.PasteMsg:
		if m.monitoring && m.mode == "" && m.monitorInput != nil && m.monitorFrame.Err == nil {
			m.monitorInput.forward([]byte("\x1b[200~" + msg.Content + "\x1b[201~"))
			return m, nil
		}
	case tea.KeyPressMsg:
		k := msg.String()
		if k == "ctrl+enter" {
			return m, nil
		}
		if m.mode == "search" {
			return m.updateSearch(msg)
		}
		if k == "ctrl+f" && m.mode == "" && !m.busy {
			return m, m.openSearch()
		}
		if m.monitoring && m.monitorID != "" && m.mode == "" && m.monitorInput != nil && m.monitorFrame.Err == nil && m.width >= 50 && m.height >= 12 {
			// Only events already parsed before monitor activation reach here.
			if raw := prefetchedMonitorKey(msg); len(raw) > 0 {
				m.monitorInput.forward(raw)
			}
			return m, nil
		}
		if k == "ctrl+c" {
			return m.quit()
		}
		if m.mode == "history" {
			return m.historyKey(k)
		}
		if m.mode == "settings" {
			return m.updateSettings(k)
		}
		if m.mode == "help" {
			switch k {
			case "esc", "q", "f1", "?":
				m.mode = ""
				if m.tab == tabSettings {
					m.mode = "settings"
				}
			}
			return m, nil
		}
		if m.mode == "connect" {
			switch k {
			case "esc", "q":
				m.mode = ""
				m.message = ""
			case "up", "k", "shift+tab":
				m.connectChoice = (m.connectChoice + 2) % 3
			case "down", "j", "tab":
				m.connectChoice = (m.connectChoice + 1) % 3
			case "enter":
				if m.monitoring {
					return m, nil
				}
				if connectionModes[m.connectChoice] == protocol.AttachTakeover {
					m.mode = "takeover"
					m.confirmChoice = 0
					return m, nil
				}
				m.selected = m.connectionSelection()
				return m.quit()
			}
			return m, nil
		}
		if m.mode == "takeover" {
			yes, done := m.confirmKey(k)
			if !done {
				return m, nil
			}
			m.mode = ""
			if yes && !m.monitoring {
				m.selected = m.connectionSelection()
				return m.quit()
			}
			m.message = ""
			return m, nil
		}
		if m.mode == "delete" {
			yes, done := m.confirmKey(k)
			if !done {
				return m, nil
			}
			if yes {
				id := m.target
				m.mode = ""
				m.busy = true
				return m, func() tea.Msg { return changed{m.catalog.Delete(m.ctx, string(id))} }
			}
			m.mode = ""
			return m, nil
		}
		if m.mode == "stop" {
			yes, done := m.confirmKey(k)
			if !done {
				return m, nil
			}
			m.mode = ""
			if !yes {
				return m, nil
			}
			id := m.target
			m.busy = true
			return m, func() tea.Msg {
				ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
				defer cancel()
				return changed{m.catalog.Stop(ctx, string(id))}
			}
		}
		if m.mode == "name-conflict" {
			yes, done := m.confirmKey(k)
			if !done {
				return m, nil
			}
			if yes {
				return m.submitName(m.nameKind, m.proposedName)
			}
			m.mode = m.nameKind
			m.input.SetValue(m.conflictName)
			return m, m.input.Focus()
		}
		if m.mode == "new" || m.mode == "rename" {
			if m.busy {
				return m, nil
			}
			if k == "esc" {
				m.mode = ""
				return m, nil
			}
			if k == "enter" {
				name := strings.TrimSpace(m.input.Value())
				kind, id := m.mode, m.target
				if kind == "new" && name == "" {
					return m.submitName(kind, name)
				}
				m.busy = true
				if kind == "new" {
					id = ""
				}
				return m, func() tea.Msg {
					suggestion, err := m.catalog.NameSuggestion(m.ctx, name, id)
					return checkedName{kind: kind, name: name, suggested: suggestion, err: err}
				}
			}
		} else {
			switch k {
			case "shift+up", "shift+down":
				if m.busy || m.monitoring {
					return m, nil
				}
				if r, ok := m.current(); ok {
					direction := 1
					if k == "shift+up" {
						direction = -1
					}
					m.busy = true
					m.initial = r.Spec.ID
					return m, func() tea.Msg { return changed{m.catalog.Move(m.ctx, r.Spec.ID, direction)} }
				}
			case "v":
				if m.tab == tabRunning {
					m.monitoring = !m.monitoring
					if !m.monitoring {
						m.closeMonitor()
						return m, nil
					}
					return m, m.syncMonitor()
				}
			case "f12":
				if m.monitoring {
					m.monitoring = false
					m.closeMonitor()
				}
				return m, nil
			case "q", "esc":
				if k == "esc" && m.monitoring {
					m.monitoring = false
					m.closeMonitor()
					return m, nil
				}
				return m.quit()
			case "left", "h", "shift+tab":
				if m.monitoring && k != "shift+tab" {
					return m, nil
				}
				m.selectTab(m.tab - 1)
			case "right", "l", "tab":
				if m.monitoring && k != "tab" {
					return m, nil
				}
				m.selectTab(m.tab + 1)
			case "up", "k":
				m.cursors[m.tab] = max(0, m.cursors[m.tab]-1)
				return m, m.syncMonitor()
			case "down", "j":
				m.cursors[m.tab] = min(max(0, len(m.rows(m.tab))-1), m.cursors[m.tab]+1)
				return m, m.syncMonitor()
			case "pgup", "pgdown", "home", "end":
				last := max(0, len(m.rows(m.tab))-1)
				step := max(1, m.height-8)
				if k == "pgup" {
					step = -step
				}
				m.cursors[m.tab] = min(last, max(0, m.cursors[m.tab]+step))
				if k == "home" {
					m.cursors[m.tab] = 0
				} else if k == "end" {
					m.cursors[m.tab] = last
				}
				return m, m.syncMonitor()
			case "r", "f5":
				m.err = nil
				m.message = ""
				m.confirmChoice = 0
				if m.monitoring {
					m.closeMonitor()
					return m, tea.Batch(m.startLoad(), m.syncMonitor())
				}
				return m, m.startLoad()
			case "f1", "?":
				if !m.busy {
					m.mode = "help"
				}
			case ",":
				if !m.busy {
					m.selectTab(tabSettings)
					m.settingsEditing = true
					m.settingRow = 0
				}
			case "n":
				if !m.busy {
					m.mode = "new"
					m.input.SetValue("")
					return m, m.input.Focus()
				}
			case "enter":
				if m.busy || m.monitoring {
					return m, nil
				}
				if r, ok := m.current(); ok {
					if !active(r) {
						return m, m.openHistory(r)
					}
					m.selected = Selection{Kind: "attach", ID: r.Spec.ID, State: m.state()}
					return m.quit()
				}
			case "e", "f2", "x", "delete", "backspace", "s", "c":
				r, ok := m.current()
				if !ok || m.busy {
					return m, nil
				}
				m.target = r.Spec.ID
				m.targetName = r.Spec.Name
				m.confirmChoice = 0
				m.err = nil
				m.message = ""
				if k == "c" {
					if m.monitoring {
						return m, nil
					}
					if !active(r) {
						m.message = m.locale().Text("finished_hint")
						return m, nil
					}
					m.mode = "connect"
					m.connectChoice = 2
					return m, nil
				}
				if k == "e" || k == "f2" {
					m.mode = "rename"
					m.input.SetValue(r.Spec.Name)
					return m, m.input.Focus()
				}
				if k == "x" || k == "delete" || k == "backspace" {
					if active(r) && r.Health != "unreachable" {
						m.message = m.locale().Text("stop_before_delete")
						return m, nil
					}
					m.mode = "delete"
					return m, nil
				}
				if !active(r) {
					return m, nil
				}
				m.mode = "stop"
				return m, nil
			}
			return m, nil
		}
	}
	if m.mode == "search" {
		return m.updateSearch(msg)
	}
	if m.mode == "new" || m.mode == "rename" {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}
func (m model) updateSettings(k string) (tea.Model, tea.Cmd) {
	if m.busy {
		return m, nil
	}
	// Tab always navigates pages, including when editing a setting. Leaving a
	// page discards previews; only an explicit Enter save persists settings.
	switch k {
	case "tab":
		m.selectTab(m.tab + 1)
		return m, nil
	case "shift+tab":
		m.selectTab(m.tab - 1)
		return m, nil
	case ",":
		m.settingsEditing = true
		m.message = ""
		return m, nil
	}
	if !m.settingsEditing {
		switch k {
		case "left", "h":
			m.selectTab(m.tab - 1)
		case "right", "l":
			m.selectTab(m.tab + 1)
		case "enter":
			m.settingsEditing = true
		case "down", "j":
			m.settingsEditing = true
			m.settingRow = 0
		case "up", "k":
			m.settingsEditing = true
			m.settingRow = settingsCount - 1
		case "esc":
			m.selectTab(m.lastTaskTab)
		case "q":
			return m.quit()
		case "f1", "?":
			m.mode = "help"
		}
		if m.settingsEditing {
			m.message = ""
			m.err = nil
		}
		return m, nil
	}
	switch k {
	case "esc", "q":
		m.draft = m.settings
		m.settingsEditing = false
		m.err = nil
	case "up", "k":
		m.settingRow = (m.settingRow + settingsCount - 1) % settingsCount
	case "down", "j":
		m.settingRow = (m.settingRow + 1) % settingsCount
	case "left", "h":
		m.adjustSetting(-1)
	case "right", "l", " ":
		m.adjustSetting(1)
	case "enter":
		settings := m.draft
		m.busy = true
		return m, func() tea.Msg { return saved{settings, m.catalog.SaveSettings(m.ctx, settings)} }
	}
	return m, nil
}
func cycle(value string, options []string, step int) string {
	index := 0
	for i, item := range options {
		if item == value {
			index = i
			break
		}
	}
	return options[(index+step+len(options))%len(options)]
}
func (m *model) adjustSetting(step int) {
	switch m.settingRow {
	case 0:
		m.draft.Language = cycle(m.draft.Language, config.LanguageCodes(), step)
	case 1:
		m.draft.Theme = cycle(m.draft.Theme, []string{"auto", "dark", "light"}, step)
	case 2:
		values := []int{1, 2, 5}
		index := 0
		for i, v := range values {
			if v == m.draft.RefreshSeconds {
				index = i
			}
		}
		m.draft.RefreshSeconds = values[(index+step+3)%3]
	case 3:
		m.draft.ShowResources = !m.draft.ShowResources
	case 4:
		m.draft.DefaultTab = cycle(m.draft.DefaultTab, []string{"running", "stop"}, step)
	case 5:
		values := []int64{0, 300, 900, 1800, 3600, 21600, 86400, 604800}
		current := m.draft.HistoryRetentionSeconds
		if step > 0 {
			m.draft.HistoryRetentionSeconds = values[0]
			for _, value := range values {
				if value > current {
					m.draft.HistoryRetentionSeconds = value
					break
				}
			}
		} else {
			m.draft.HistoryRetentionSeconds = values[len(values)-1]
			for i := len(values) - 1; i >= 0; i-- {
				if values[i] < current {
					m.draft.HistoryRetentionSeconds = values[i]
					break
				}
			}
		}
	}
}

// quit renders an empty alternate screen before Bubble Tea restores the shell.
// Keep AltScreen enabled in the final view so no frame is painted on the shell.
func (m model) quit() (tea.Model, tea.Cmd) {
	m.quitting = true
	return m, tea.Quit
}

func Run(ctx context.Context, catalog Backend, input io.Reader, output io.Writer, notice error, options Options) (Selection, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if ctx.Err() != nil {
		return Selection{}, nil
	}
	m := newModel(ctx, catalog, notice, options)
	var route *monitorInput
	if file, ok := input.(*os.File); ok {
		route = &monitorInput{File: file, ctx: ctx}
		m.monitorInput = route
		input = route
	}
	// main owns process signals. Bridge cancellation into a graceful model quit
	// instead of killing Bubble Tea's context: the killed path closes its input
	// reader without joining it, and skips the final empty alternate-screen frame.
	program := tea.NewProgram(m, tea.WithContext(context.WithoutCancel(ctx)), tea.WithoutSignalHandler(), tea.WithInput(input), tea.WithOutput(output))
	if route != nil {
		route.notify = func(action monitorAction) { go program.Send(action) }
		defer route.Stop()
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			program.Send(exitRequested{})
		case <-done:
		}
	}()
	result, err := program.Run()
	// Run returns the last model on interrupted/context-cancelled exits too.
	// Release its background work and display lease on every return path.
	final, ok := result.(model)
	if ok {
		if final.cancelLoad != nil {
			final.cancelLoad()
		}
		final.closeMonitor()
		final.closeHistory()
	}
	if errors.Is(err, tea.ErrInterrupted) ||
		(errors.Is(err, tea.ErrProgramKilled) && errors.Is(err, context.Canceled)) {
		return Selection{}, nil
	}
	if err != nil {
		return Selection{}, err
	}
	return final.selected, nil
}
