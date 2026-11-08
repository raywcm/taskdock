package tui

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/raywcm/taskdock/internal/monitor"
	"github.com/raywcm/taskdock/internal/terminal"
)

type monitorAction struct {
	background tea.BackgroundColorMsg
	kind       string
	session    *monitor.Session
	err        error
	raw        []byte
	ready      chan struct{}
}

// The same cancelable TTY reader serves Bubble Tea and the embedded terminal.
// Control input is intercepted before key parsing, preserving opaque bytes,
// bracketed paste, Unicode and Ctrl+C/D without a second competing reader.
type monitorInput struct {
	*os.File
	mu                     sync.Mutex
	enabled, armed, closed bool
	target                 *monitor.Session
	pending                []byte
	decoder                terminal.EscapeDecoder
	timer                  *time.Timer
	commands               []string
	notify                 func(monitorAction)
	barrier                chan struct{}
	ctx                    context.Context
	timerGeneration        uint64
}

func (r *monitorInput) resetLocked() {
	r.timerGeneration++
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	r.decoder = terminal.EscapeDecoder{Command: func(raw []byte, key uint16) bool {
		command := monitorNavigation(raw)
		if key == 0x06 {
			command = "search"
		}
		if command == "" {
			return false
		}
		r.commands = append(r.commands, command)
		return true
	}, Ignore: monitorIgnoredKey, Transform: monitorApplicationKey, Report: func(raw []byte) bool {
		if len(raw) < 3 || raw[0] != 0x1b || raw[1] != ']' {
			return false
		}
		var decoder uv.EventDecoder
		n, event := decoder.Decode(raw)
		if color, ok := event.(uv.BackgroundColorEvent); ok && n == len(raw) {
			if r.notify != nil {
				r.notify(monitorAction{kind: "background", background: tea.BackgroundColorMsg(color)})
			}
			return true
		}
		return false
	}}
	r.commands = nil
}
func (r *monitorInput) Arm(armed bool) { r.mu.Lock(); r.armed = armed; r.mu.Unlock() }
func (r *monitorInput) Begin() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.target != nil {
		r.pending = nil
	}
	r.target = nil
	r.enabled = true
	r.resetLocked()
}
func (r *monitorInput) Disable() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.enabled = false
	r.target = nil
	r.pending = nil
	r.resetLocked()
}
func (r *monitorInput) Stop() { r.Disable(); r.mu.Lock(); r.closed = true; r.mu.Unlock() }
func (r *monitorInput) SetTarget(s *monitor.Session) {
	r.mu.Lock()
	if r.closed || !r.enabled {
		r.mu.Unlock()
		return
	}
	r.target = s
	pending := r.pending
	r.pending = nil
	r.mu.Unlock()
	if len(pending) > 0 {
		r.forward(pending)
	}
}
func (r *monitorInput) Read(p []byte) (int, error) {
	n, err := r.File.Read(p)
	if n == 0 {
		return n, err
	}
	r.waitSearch()
	r.mu.Lock()
	active := r.enabled
	// Preserve typeahead when v and application input arrive in one read.
	if !active && r.armed && p[0] == 'v' {
		r.enabled = true
		r.pending = append([]byte(nil), p[1:n]...)
		r.resetLocked()
		r.mu.Unlock()
		p[0] = 'v'
		return 1, err
	}
	r.mu.Unlock()
	if !active {
		return n, err
	}
	remainder := r.route(p[:n], true)
	r.waitSearch()
	return copy(p, remainder), err
}
func (r *monitorInput) waitSearch() {
	r.mu.Lock()
	barrier := r.barrier
	r.mu.Unlock()
	if barrier != nil {
		ctx := r.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		select {
		case <-barrier:
		case <-ctx.Done():
		}
	}
}
func (r *monitorInput) forward(raw []byte) {
	r.route(raw, false)
}
func (r *monitorInput) route(raw []byte, fromRead bool) []byte {
	r.mu.Lock()
	if !r.enabled || r.closed {
		r.mu.Unlock()
		return nil
	}
	target := r.target
	if target == nil {
		if len(r.pending)+len(raw) <= 64*1024 {
			r.pending = append(r.pending, raw...)
			r.mu.Unlock()
			return nil
		}
		r.enabled = false
		r.pending = nil
		r.mu.Unlock()
		r.notify(monitorAction{kind: "input-error", err: errors.New("monitor input buffer is full")})
		return nil
	}
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	r.timerGeneration++
	data, detach := r.decoder.Feed(raw)
	commands := append([]string(nil), r.commands...)
	r.commands = nil
	var ready chan struct{}
	var remainder []byte
	if detach {
		if len(commands) == 0 {
			commands = []string{"back"}
		}
		r.enabled = len(commands) > 0 && (commands[0] == "next" || commands[0] == "previous")
		if r.enabled {
			r.pending = append([]byte(nil), r.decoder.Remaining...)
		}
		if commands[0] == "search" {
			ready = make(chan struct{})
			r.barrier = ready
			remainder = append([]byte(nil), r.decoder.Remaining...)
		}
		r.target = nil
		r.resetLocked()
	} else if r.decoder.PendingEscape() {
		generation := r.timerGeneration
		delay := 50 * time.Millisecond
		if r.decoder.PendingReport() {
			delay = 500 * time.Millisecond
		}
		r.timer = time.AfterFunc(delay, func() { r.flush(target, generation) })
	}
	r.mu.Unlock()
	if len(data) > 0 {
		if err := target.Input(data); err != nil {
			r.notify(monitorAction{kind: "input-error", session: target, err: err})
			if ready != nil {
				close(ready)
			}
			return nil
		}
	}
	for _, command := range commands {
		action := monitorAction{kind: command, session: target, ready: ready}
		if !fromRead {
			action.raw = remainder
		}
		r.notify(action)
	}
	return remainder
}
func (r *monitorInput) flush(target *monitor.Session, generation uint64) {
	r.mu.Lock()
	if r.closed || !r.enabled || r.target != target || generation != r.timerGeneration {
		r.mu.Unlock()
		return
	}
	data := r.decoder.FlushEscape()
	commands := append([]string(nil), r.commands...)
	r.commands = nil
	r.timer = nil
	if len(commands) > 0 {
		r.enabled = false
		r.target = nil
		r.pending = nil
		r.resetLocked()
	}
	r.mu.Unlock()
	if len(data) > 0 {
		if err := target.Input(data); err != nil {
			r.notify(monitorAction{kind: "input-error", session: target, err: err})
		}
	}
	for _, command := range commands {
		r.notify(monitorAction{kind: command, session: target})
	}
}

func monitorNavigation(raw []byte) string {
	if len(raw) == 0 || raw[0] != 0x1b {
		return ""
	}
	var decoder uv.EventDecoder
	n, event := decoder.Decode(raw)
	if n != len(raw) {
		return ""
	}
	if key, ok := event.(uv.KeyPressEvent); ok {
		if key.Mod & ^(uv.ModCapsLock|uv.ModNumLock) != 0 {
			return ""
		}
		switch key.Code {
		case uv.KeyUp:
			return "previous"
		case uv.KeyDown:
			return "next"
		case uv.KeyEscape:
			return "back"
		}
	}
	return ""
}

func monitorApplicationKey(raw []byte) []byte {
	if len(raw) < 2 || raw[0] != 0x1b {
		return raw
	}
	var decoder uv.EventDecoder
	n, event := decoder.Decode(raw)
	if n != len(raw) {
		return raw
	}
	var key uv.Key
	switch e := event.(type) {
	case uv.KeyPressEvent:
		key = uv.Key(e)
	case uv.KeyReleaseEvent:
		key = uv.Key(e)
		if key.Mod & ^(uv.ModCapsLock|uv.ModNumLock) == uv.ModShift && (key.Code == uv.KeyUp || key.Code == uv.KeyDown) {
			return nil
		}
		return raw
	default:
		return raw
	}
	if key.Mod & ^(uv.ModCapsLock|uv.ModNumLock) == uv.ModShift {
		if key.Code == uv.KeyUp {
			return []byte("\x1b[A")
		}
		if key.Code == uv.KeyDown {
			return []byte("\x1b[B")
		}
	}
	return raw
}

func monitorIgnoredKey(raw []byte, key uint16) bool {
	if key == terminal.KeyCtrlEnter {
		return true
	}
	if len(raw) > 1 && raw[0] == 0x1b {
		var decoder uv.EventDecoder
		n, event := decoder.Decode(raw)
		if release, ok := event.(uv.KeyReleaseEvent); ok && n == len(raw) {
			return release.Code == uv.KeyEnter && release.Mod&^(uv.ModCapsLock|uv.ModNumLock) == uv.ModCtrl
		}
	}
	return false
}

// Preserve key events that were decoded just before v enabled the raw route.
func prefetchedMonitorKey(msg tea.KeyPressMsg) []byte {
	k := msg.Key()
	if k.Mod == tea.ModShift && (k.Code == tea.KeyUp || k.Code == tea.KeyDown) {
		if k.Code == tea.KeyUp {
			return []byte("\x1b[1;2A")
		}
		return []byte("\x1b[1;2B")
	}
	if k.Code == tea.KeyEnter && k.Mod == tea.ModCtrl {
		return []byte("\x1b[13;5u")
	}
	if k.Mod&tea.ModCtrl != 0 && k.Code >= '@' && k.Code <= 'z' {
		return []byte{byte(k.Code) & 0x1f}
	}
	if k.Text != "" {
		return []byte(k.Text)
	}
	switch k.Code {
	case tea.KeyEnter:
		return []byte{'\r'}
	case tea.KeyTab:
		return []byte{'\t'}
	case tea.KeyEscape:
		return []byte{0x1b}
	case tea.KeyBackspace:
		return []byte{0x7f}
	case tea.KeyUp:
		return []byte("\x1b[A")
	case tea.KeyDown:
		return []byte("\x1b[B")
	case tea.KeyLeft:
		return []byte("\x1b[D")
	case tea.KeyRight:
		return []byte("\x1b[C")
	case tea.KeyF12:
		return []byte("\x1b[24~")
	}
	return nil
}
