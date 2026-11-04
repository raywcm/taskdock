package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/raywcm/taskdock/internal/ipc"
	"github.com/raywcm/taskdock/internal/protocol"
	"github.com/raywcm/taskdock/internal/task"
	"reflect"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

func parseRecovery(t *testing.T, data string) *recovery {
	t.Helper()
	id, _ := task.NewID()
	instance, _ := task.NewID()
	r := newRecovery(task.Runtime{TaskID: id, InstanceID: instance}, task.TerminalModes{})
	parser := r.parser()
	for _, b := range []byte(data) {
		parser.Advance(b)
	}
	r.modes.Rendition = r.rendition()
	return r
}

func TestRestoreOnlyModesNeverHistoricalTextOrQueries(t *testing.T) {
	r := parseRecovery(t, "\x1b[?1049h\x1b[?25l\x1b[?1000;1006h\x1b[?2004h\x1b=\x1b[2 qTITLE\x1b]52;c;PRIVATE-CLIPBOARD\x07\x1b[6n")
	got := r.prefix()
	if !r.fullscreen() || !strings.HasPrefix(got, "\x1b[?1049h") {
		t.Fatalf("alternate buffer not restored: %q", got)
	}
	for _, mode := range []string{"\x1b[?25l", "\x1b[?1000h", "\x1b[?1006h", "\x1b[?2004h", "\x1b=", "\x1b[2 q"} {
		if !strings.Contains(got, mode) {
			t.Fatalf("missing %q in %q", mode, got)
		}
	}
	for _, unsafe := range []string{"TITLE", "PRIVATE-CLIPBOARD", "\x1b]", "\x1b[6n"} {
		if strings.Contains(got, unsafe) {
			t.Fatalf("replayed %q", unsafe)
		}
	}
	// A reset of another mouse alias must precede the active protocol enable.
	if strings.Index(got, "\x1b[?1003l") > strings.Index(got, "\x1b[?1000h") {
		t.Fatal("mouse reset disabled restored tracking")
	}
}

func TestAlternateAliasesAndPlainShell(t *testing.T) {
	r := parseRecovery(t, "\x1b[?47h\x1b[?1049l")
	if r.fullscreen() {
		t.Fatal("leaving an alternate alias did not return to normal screen")
	}
	if strings.Contains(r.prefix(), "\x1b[?1049h") {
		t.Fatal("forced normal app into alternate screen")
	}
	plain := parseRecovery(t, "normal shell output\r\n")
	if plain.prefix() != "" {
		t.Fatalf("modified plain shell: %q", plain.prefix())
	}
}

func TestCheckpointSurvivesMissingOldModeEntryAndRejectsOtherInstance(t *testing.T) {
	r := parseRecovery(t, "\x1b[?1049h\x1b[?1000h")
	same := newRecovery(task.Runtime{TaskID: r.modes.TaskID, InstanceID: r.modes.InstanceID}, r.modes)
	parser := same.parser()
	for _, b := range []byte("\x1b[5;1Hupdated counter") {
		parser.Advance(b)
	}
	if !same.fullscreen() {
		t.Fatal("forgot mode after original log entry rotated out")
	}
	other, _ := task.NewID()
	changed := newRecovery(task.Runtime{TaskID: r.modes.TaskID, InstanceID: other}, r.modes)
	if changed.fullscreen() {
		t.Fatal("reused another broker's terminal state")
	}
}

func TestRenditionSurvivesReconnectRotationAndInstanceChange(t *testing.T) {
	for _, style := range []string{
		"\x1b[1;3;38;2;229;237;247;48;2;20;26;38m",
		"\x1b[2;7;38;5;244;48;5;17m",
		"\x1b[4:3;58:2::1:2:3;31;44m",
		"\x1b[1;31m\x1b[22;39;49m",
	} {
		r := parseRecovery(t, style)
		want := r.pen
		// Only generated SGR leaves the boundary scanner. Parsing the output
		// must restore the same style over any stale manager/other-session pen.
		restored := parseRecovery(t, "\x1b[2;7;32;45m"+r.rendition())
		if !restored.pen.Equal(&want) {
			t.Fatalf("rendition changed across attach: %q", style)
		}
		same := newRecovery(task.Runtime{TaskID: r.modes.TaskID, InstanceID: r.modes.InstanceID}, r.modes)
		p := same.parser()
		for _, b := range []byte("\x1b[Hrotated log without new colors") {
			p.Advance(b)
		}
		if !same.pen.Equal(&want) {
			t.Fatalf("rotation forgot active rendition: %q", style)
		}
		other, _ := task.NewID()
		changed := newRecovery(task.Runtime{TaskID: r.modes.TaskID, InstanceID: other}, r.modes)
		if !changed.pen.IsZero() {
			t.Fatal("inherited rendition from another task instance")
		}
	}
	reset := parseRecovery(t, "\x1b[2;31;44m\x1bc")
	if !reset.pen.IsZero() || reset.rendition() != "\x1b[m" {
		t.Fatal("terminal reset retained the old rendition")
	}
}

func TestCheckpointRenditionCannotReplayOtherControls(t *testing.T) {
	r := parseRecovery(t, "")
	r.modes.Rendition = "\x1b]52;c;SECRET\a\x1b[?1049h\x1b[6n\x1b[31mTEXT"
	got := newRecovery(task.Runtime{TaskID: r.modes.TaskID, InstanceID: r.modes.InstanceID}, r.modes)
	want := uv.Style{Fg: ansi.Red}
	if got.fullscreen() || !got.pen.Equal(&want) || strings.Contains(got.rendition(), "SECRET") || strings.Contains(got.rendition(), "TEXT") {
		t.Fatal("checkpoint replayed more than generated rendition")
	}
}

func TestRepaintWaitsForAppRedrawAndPreservesFrames(t *testing.T) {
	frames := make(chan Incoming, 4)
	first := Incoming{Frame: protocol.Frame{Kind: protocol.Output, Payload: []byte("delta\x1b[2")}}
	second := Incoming{Frame: protocol.Frame{Kind: protocol.Output, Payload: []byte("JSTATIC")}}
	var sizes []task.Size
	send := func(f protocol.Frame) error {
		var size task.Size
		if err := json.Unmarshal(f.Payload, &size); err != nil {
			return err
		}
		sizes = append(sizes, size)
		if len(sizes) == 1 {
			frames <- first
			frames <- second
		}
		return nil
	}
	pending, err := Repaint(context.Background(), send, frames, task.Size{Rows: 24, Cols: 80}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pending, []Incoming{first, second}) {
		t.Fatalf("changed frame order or bytes: %+v", pending)
	}
	if !reflect.DeepEqual(sizes, []task.Size{{Rows: 23, Cols: 80}, {Rows: 24, Cols: 80}}) {
		t.Fatal(sizes)
	}
}

func TestRepaintTimeoutIgnoresOSCAndRestoresSize(t *testing.T) {
	frames := make(chan Incoming, 1)
	frames <- Incoming{Frame: protocol.Frame{Kind: protocol.Output, Payload: []byte("\x1b]0;[2J\x07\x1b[J")}}
	var sizes []task.Size
	send := func(f protocol.Frame) error {
		var size task.Size
		if err := json.Unmarshal(f.Payload, &size); err != nil {
			return err
		}
		sizes = append(sizes, size)
		return nil
	}
	start := time.Now()
	pending, err := Repaint(context.Background(), send, frames, task.Size{Rows: 24, Cols: 80}, 30*time.Millisecond)
	if err != nil || len(pending) != 1 {
		t.Fatalf("lost live output: %v %v", pending, err)
	}
	if time.Since(start) < 25*time.Millisecond {
		t.Fatal("OSC or partial erase incorrectly acknowledged redraw")
	}
	if len(sizes) != 2 || sizes[1].Rows != 24 {
		t.Fatal("did not restore size", sizes)
	}
}

func TestRepaintPreservesTakeoverReasonWithoutRestoringLostLease(t *testing.T) {
	frames := make(chan Incoming, 1)
	frames <- Incoming{Frame: ipc.JSON(protocol.Error, protocol.ErrorMessage{Code: "taken_over", Message: "taken over"})}
	sends := 0
	_, err := Repaint(context.Background(), func(protocol.Frame) error { sends++; return nil }, frames, task.Size{Rows: 24, Cols: 80}, time.Second)
	var remote *ipc.RemoteError
	if !errors.As(err, &remote) || remote.Code != "taken_over" || sends != 1 {
		t.Fatalf("lost revocation or resized revoked lease: %d %v", sends, err)
	}
}
