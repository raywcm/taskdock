package tui

import (
	"bytes"
	"os"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/raywcm/taskdock/internal/monitor"
)

func TestMonitorNavigationNeverReachesApplication(t *testing.T) {
	for _, test := range []struct{ raw, want string }{
		{"\x1b[A", "previous"}, {"\x1b[B", "next"},
		{"\x1bOA", "previous"}, {"\x1bOB", "next"},
		{"\x1b[57352u", "previous"}, {"\x1b[57353u", "next"},
		{"\x1b[27u", "back"},
		{"\x11", "back"}, {"\x1b[113;5u", "back"},
		{"\x1b[113;5:1u", "back"}, {"\x1b[27;5;113~", "back"},
		{"\x1b[24~", "back"},
	} {
		for split := 0; split <= len(test.raw); split++ {
			actions := make(chan monitorAction, 2)
			route := &monitorInput{notify: func(a monitorAction) { actions <- a }}
			route.Begin()
			// An unavailable Input implementation makes any accidental forwarding
			// an input-error rather than the expected navigation action.
			route.SetTarget(&monitor.Session{})
			route.forward([]byte(test.raw[:split]))
			route.forward([]byte(test.raw[split:]))
			select {
			case action := <-actions:
				if action.kind != test.want || action.err != nil {
					t.Fatalf("%q split %d: %+v", test.raw, split, action)
				}
			case <-time.After(time.Second):
				t.Fatalf("%q split %d was not handled", test.raw, split)
			}
			route.Stop()
		}
	}
}

func TestStandaloneEscReturnsAndStaleTimeoutCannotConsumeArrow(t *testing.T) {
	actions := make(chan monitorAction, 2)
	route := &monitorInput{notify: func(a monitorAction) { actions <- a }}
	target := &monitor.Session{}
	route.Begin()
	route.SetTarget(target)
	route.forward([]byte{0x1b})
	route.mu.Lock()
	old := route.timerGeneration
	route.mu.Unlock()
	route.forward([]byte{'['})
	route.flush(target, old)
	route.forward([]byte{'B'})
	if action := <-actions; action.kind != "next" {
		t.Fatalf("stale Esc timeout intercepted a split arrow: %+v", action)
	}
	route.Begin()
	route.SetTarget(target)
	route.forward([]byte{0x1b})
	select {
	case action := <-actions:
		if action.kind != "back" || action.err != nil {
			t.Fatalf("Esc reached application: %+v", action)
		}
	case <-time.After(time.Second):
		t.Fatal("standalone Esc did not return")
	}
	route.Stop()
}

func TestMonitorTypeaheadAndFailureRelease(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	route := &monitorInput{File: read}
	route.Arm(true)
	input := []byte("vinput\x03中文")
	if _, err := write.Write(input); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 1024)
	if n, err := route.Read(buffer); err != nil || n != 1 || buffer[0] != 'v' {
		t.Fatalf("activation: %d %v", n, err)
	}
	route.Begin()
	if !bytes.Equal(route.pending, input[1:]) {
		t.Fatalf("typeahead lost: %q", route.pending)
	}
	m := readyModel(t)
	m.monitorInput = route
	m, cmd := press(m, 'v')
	if cmd == nil {
		t.Fatal("monitor command missing")
	}
	var apply func(tea.Cmd)
	apply = func(cmd tea.Cmd) {
		if cmd == nil {
			return
		}
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, nested := range batch {
				apply(nested)
			}
			return
		}
		next, _ := m.Update(msg)
		m = next.(model)
	}
	apply(cmd) // The backend reports a connection error.
	if route.enabled || len(route.pending) > 0 || m.monitorFrame.Err == nil {
		t.Fatal("failed monitor retained the keyboard or buffered application input")
	}
	for _, key := range []tea.KeyPressMsg{{Code: 'q', Mod: tea.ModCtrl}, {Code: tea.KeyF12}} {
		failed := m
		next, _ := failed.Update(key)
		if next.(model).monitoring {
			t.Fatalf("%s did not return from failed monitoring", key.String())
		}
	}
}

func TestCtrlEnterIgnoredWithoutLeavingMonitor(t *testing.T) {
	for _, raw := range []string{"\x1b[13;5u", "\x1b[27;5;13~", "\x1b[13;5:2u", "\x1b[13;5:3u"} {
		for split := 0; split <= len(raw); split++ {
			route := &monitorInput{notify: func(a monitorAction) { t.Errorf("Ctrl+Enter produced an action: %+v", a) }}
			route.Begin()
			target := &monitor.Session{}
			route.SetTarget(target)
			route.forward([]byte(raw[:split]))
			route.forward([]byte(raw[split:]))
			if !route.enabled || route.target != target {
				t.Fatal("Ctrl+Enter changed the control connection")
			}
			route.Stop()
		}
	}
	for _, failed := range []bool{false, true} {
		m := readyModel(t)
		m, _ = press(m, 'v')
		if failed {
			m.monitorFrame.Err = os.ErrNotExist
		}
		for _, msg := range []tea.Msg{tea.KeyPressMsg{Code: tea.KeyEnter}, tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModCtrl}, monitorAction{kind: "attach"}} {
			next, _ := m.Update(msg)
			m = next.(model)
			if m.selected.Kind != "" || m.quitting || !m.monitoring {
				t.Fatal("monitor input opened a fullscreen attachment")
			}
		}
		m.closeMonitor()
	}
}

func TestShiftArrowsAreApplicationKeys(t *testing.T) {
	for raw, want := range map[string]string{
		"\x1b[1;2A": "\x1b[A", "\x1b[1;2B": "\x1b[B",
		"\x1b[1;5A":     "\x1b[1;5A",
		"\x1b[57352;2u": "\x1b[A", "\x1b[57353;2:2u": "\x1b[B",
		"\x1b[57352;2:3u": "",
	} {
		for split := 0; split <= len(raw); split++ {
			r := &monitorInput{}
			r.resetLocked()
			a, first := r.decoder.Feed([]byte(raw[:split]))
			b, second := r.decoder.Feed([]byte(raw[split:]))
			if first || second || string(append(a, b...)) != want || len(r.commands) != 0 {
				t.Fatalf("%q split %d did not become application arrow", raw, split)
			}
		}
	}
	r := &monitorInput{}
	r.resetLocked()
	paste := "\x1b[200~\x1b[1;2A\x1b[201~"
	out, handled := r.decoder.Feed([]byte(paste))
	if handled || string(out) != paste {
		t.Fatal("paste content was transformed")
	}
}

func TestMonitorKeysCannotTriggerListActions(t *testing.T) {
	m := readyModel(t)
	route := &monitorInput{}
	m.monitorInput = route
	m, _ = press(m, 'v')
	defer m.closeMonitor()
	for _, key := range []rune{'n', 's', 'q', 'v', tea.KeyLeft, tea.KeyDown, tea.KeyEnter, tea.KeyF12} {
		msg := tea.KeyPressMsg{Code: key}
		if key >= 32 && key < 127 {
			msg.Text = string(key)
		}
		next, _ := m.Update(msg)
		m = next.(model)
	}
	if !m.monitoring || m.mode != "" || m.selected.Kind != "" || m.cursors[0] != 0 {
		t.Fatal("application input triggered a list action")
	}
	if !bytes.Contains(route.pending, []byte("nsqv\x1b[D\x1b[B\r\x1b[24~")) {
		t.Fatalf("prefetched input lost: %q", route.pending)
	}
}

func TestBackgroundReportsStayOutOfMonitorInput(t *testing.T) {
	for _, raw := range []string{"\x1b]11;rgb:ffff/ffff/ffff\a", "\x1b]11;rgb:0000/0000/0000\x1b\\"} {
		for split := 0; split <= len(raw); split++ {
			actions := make(chan monitorAction, 2)
			r := &monitorInput{notify: func(a monitorAction) { actions <- a }}
			r.resetLocked()
			a, done := r.decoder.Feed([]byte(raw[:split]))
			b, last := r.decoder.Feed([]byte(raw[split:]))
			if len(a)+len(b) != 0 || done || last {
				t.Fatalf("reply leaked at split %d", split)
			}
			select {
			case action := <-actions:
				if action.kind != "background" || action.background.Color == nil {
					t.Fatal("background was not decoded")
				}
			default:
				t.Fatal("background reply was lost")
			}
		}
	}
	r := &monitorInput{notify: func(monitorAction) { t.Fatal("literal paste became a terminal reply") }}
	r.resetLocked()
	paste := "\x1b[200~\x1b]11;rgb:ffff/ffff/ffff\a\x1b[201~"
	out, handled := r.decoder.Feed([]byte(paste))
	if handled || string(out) != paste {
		t.Fatal("literal pasted OSC was changed")
	}
}
