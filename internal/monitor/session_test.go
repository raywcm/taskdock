package monitor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/raywcm/taskdock/internal/store"
	"github.com/raywcm/taskdock/internal/task"
)

func TestOffscreenEmulationStylesCJKAndAlternateScreen(t *testing.T) {
	e := newScreen(40, 10)
	defer e.Close()
	_, _ = e.Write([]byte("SHELL\r\n"))
	_, _ = e.Write([]byte("\x1b[?1049h\x1b[2J\x1b[H\x1b[31m标题\x1b[3;1HSTATIC\x1b[5;1Hcounter:1"))
	_, _ = e.Write([]byte("\x1b[5;9H2"))
	frame := screenFrame(e, nil)
	if !frame.Alternate || !strings.Contains(ansi.Strip(frame.Rows[0]), "标题") || !strings.Contains(ansi.Strip(frame.Rows[2]), "STATIC") || !strings.Contains(ansi.Strip(frame.Rows[4]), "counter:2") {
		t.Fatalf("screen was not reconstructed: %#v", frame.Rows)
	}
	for _, row := range frame.Rows {
		if !utf8.ValidString(row) || ansi.StringWidth(row) != 40 {
			t.Fatalf("invalid cell row: %q", row)
		}
	}
	_, _ = e.Write([]byte("\x1b[?1049l"))
	if got := screenFrame(e, nil); got.Alternate || !strings.Contains(ansi.Strip(got.Rows[0]), "SHELL") {
		t.Fatal("normal buffer was lost")
	}
}
func TestQueriesAndOSCStayInsideEmulator(t *testing.T) {
	e := newScreen(40, 8)
	defer e.Close()
	done := make(chan struct{})
	go func() {
		_, _ = e.Write([]byte("\x1b]2;HOST-TITLE\a\x1b]52;c;SECRET\a\x1b[6n\x1b[c\x1b]8;;https://example.invalid\aLINK\x1b]8;;\a"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("device query blocked the preview")
	}
	frame := screenFrame(e, nil)
	for _, row := range frame.Rows {
		if strings.Contains(row, "\x1b]") || strings.Contains(row, "HOST-TITLE") || strings.Contains(row, "SECRET") || strings.Contains(row, "\x1b[6n") {
			t.Fatalf("unsafe terminal commands escaped the virtual screen: %q", row)
		}
	}
	if !strings.Contains(ansi.Strip(frame.Rows[0]), "LINK") {
		t.Fatal("safe printable content was lost")
	}
}
func TestMonitorLogsArePrivateRegularFiles(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "output.log")
	if err := os.WriteFile(target, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := openLog(target)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	link := filepath.Join(root, "symlink")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if f, err := openLog(link); err == nil {
		f.Close()
		t.Fatal("followed a symlink")
	}
	if err := os.Chmod(target, 0644); err != nil {
		t.Fatal(err)
	}
	if f, err := openLog(target); err == nil {
		f.Close()
		t.Fatal("accepted a public log file")
	}
}
func TestCancelledSessionExitsAndDimensionsAreBounded(t *testing.T) {
	repository, err := store.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	id, _ := task.NewID()
	s := Open(ctx, repository, task.Record{Spec: task.Spec{ID: id, Size: task.Size{Rows: 24, Cols: 80}}}, task.Size{Rows: 10, Cols: 40})
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled monitor leaked")
	}
	w, h := dimensions(task.Size{Rows: 65535, Cols: 65535})
	if w > 512 || h > 256 {
		t.Fatal("unbounded virtual screen")
	}
}

func TestHistoricalMarginsOutsidePreviewStayWithinScreen(t *testing.T) {
	for _, size := range []task.Size{{Rows: 24, Cols: 80}, {Rows: 17, Cols: 54}, {Rows: 4, Cols: 20}} {
		for _, sequence := range []string{
			"\x1b[1;999r\x1b[H\x1bM",
			"\x1b[1;999r\x1b[H\x1b[L",
			"\x1b[1;999r\x1b[H\x1b[M",
			"\x1b[?69h\x1b[1;999s\x1b[H\x1b[L",
		} {
			e := newScreen(int(size.Cols), int(size.Rows))
			for _, b := range []byte(sequence) {
				if _, err := e.Write([]byte{b}); err != nil {
					t.Fatal(err)
				}
			}
			e.Write([]byte("\x1b[?69l\x1b[r\x1b[HSTILL-RENDERING"))
			frame := screenFrame(e, nil)
			if !strings.Contains(ansi.Strip(frame.Rows[0]), "STILL-RENDERING") {
				t.Fatal("invalid margin poisoned the screen")
			}
			e.Close()
		}
	}
	// Valid regions keep their scroll behavior, including after a resize.
	e := newScreen(20, 6)
	defer e.Close()
	e.Write([]byte("\x1b[1;1HTOP\x1b[2;1HSECOND\x1b[2;4r\x1b[2;1H\x1bM"))
	f := screenFrame(e, nil)
	if !strings.Contains(f.Rows[0], "TOP") || !strings.Contains(f.Rows[2], "SECOND") {
		t.Fatal("valid scroll region was changed")
	}
	e.Resize(20, 4)
	e.Write([]byte("\x1b[1;6r\x1b[H\x1b[L"))
}

func TestPreviewPanicIsContainedAndUnwindsCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{done: make(chan struct{}), cancel: cancel}
	cleaned := make(chan struct{})
	go s.supervise(func() { defer close(cleaned); panic("parser fixture") })
	select {
	case <-s.done:
	case <-time.After(time.Second):
		t.Fatal("panicked preview did not finish")
	}
	if !errors.Is(s.Snapshot().Err, ErrEmulation) {
		t.Fatal("panic was not published as a preview error")
	}
	select {
	case <-cleaned:
	default:
		t.Fatal("preview cleanup did not unwind")
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("preview work was not cancelled")
	}
	// A failure in one preview must not poison subsequent sessions.
	healthy := &Session{done: make(chan struct{}), cancel: func() {}}
	go healthy.supervise(func() {})
	<-healthy.done
	if healthy.Snapshot().Err != nil {
		t.Fatal("subsequent preview inherited failure")
	}
}

// Optional private replay fixture. The file stays on the machine being tested;
// neither its contents nor the screen cells are included in test output.
func TestReplayLogFromEnvironment(t *testing.T) {
	path := os.Getenv("TDOCK_TEST_REPLAY_LOG")
	if path == "" {
		t.Skip("no private replay fixture")
	}
	for _, size := range []task.Size{{Cols: 80, Rows: 24}, {Cols: 54, Rows: 17}, {Cols: 85, Rows: 23}, {Cols: 120, Rows: 30}} {
		file, err := openLog(path)
		if err != nil {
			t.Fatal(err)
		}
		e := newScreen(int(size.Cols), int(size.Rows))
		_, err = feed(context.Background(), e, file, 16*1024*1024)
		file.Close()
		if err != nil {
			t.Fatal(err)
		}
		frame := screenFrame(e, nil)
		e.Close()
		if !frame.Ready || frame.Width != int(size.Cols) || frame.Height != int(size.Rows) {
			t.Fatal("replay did not produce a bounded screen")
		}
	}
}
