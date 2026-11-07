package terminal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/raywcm/taskdock/internal/protocol"
	"github.com/raywcm/taskdock/internal/task"
	"github.com/raywcm/taskdock/internal/vtscreen"
	"golang.org/x/sys/unix"
)

// A fixed status row requires a bounded screen. The application's cursor and
// saved cursor belong to the emulator, independently of the physical status row.
// --no-status keeps the native byte-forwarding path available.
type statusScreen struct {
	mu          sync.Mutex
	e           *vt.Emulator
	size        task.Size
	name        string
	id          string
	mode        string
	memory      string
	previous    []string
	controls    strings.Builder
	live        bool
	relay       atomic.Bool
	dirty       bool
	repliesDone chan struct{}
	replayed    chan struct{}
}

const replayBarrier = "\x00TDOCK-REPLAY-END\x00"

func sessionSize(size task.Size, status bool) task.Size {
	if status && size.Rows > 1 {
		size.Rows--
	}
	return size
}

func newStatusScreen(size task.Size, spec task.Spec, send func(protocol.Frame) error) *statusScreen {
	app := sessionSize(size, true)
	s := &statusScreen{e: vtscreen.New(int(app.Cols), int(app.Rows)), size: size, name: spec.Name, id: string(spec.ID), memory: "—", dirty: true, repliesDone: make(chan struct{}), replayed: make(chan struct{})}
	// Device replies use the same serialized input channel as keyboard input.
	// Drain even after a send failure so a disconnected client cannot deadlock
	// the emulator's synchronous reply pipe during teardown.
	go func() {
		defer close(s.repliesDone)
		buf := make([]byte, 4096)
		for {
			n, err := s.e.Read(buf)
			if bytes.Equal(buf[:n], []byte(replayBarrier)) {
				close(s.replayed)
				continue
			}
			if n > 0 && s.relay.Load() && send != nil {
				_ = send(protocol.Frame{Kind: protocol.Input, Payload: append([]byte(nil), buf[:n]...)})
			}
			if err != nil {
				return
			}
		}
	}()
	s.e.SetCallbacks(vt.Callbacks{
		Bell:        func() { s.controls.WriteByte('\a') },
		EnableMode:  func(mode ansi.Mode) { s.inputMode(mode, true) },
		DisableMode: func(mode ansi.Mode) { s.inputMode(mode, false) },
	})
	// Palette queries must reach the real terminal. Its replies pass through the
	// existing input decoder, preserving light/dark detection and custom palettes.
	for _, code := range []int{4, 10, 11, 12, 52, 104, 110, 111, 112} {
		s.e.RegisterOscHandler(code, func(data []byte) bool {
			if s.live {
				s.controls.WriteString("\x1b]" + string(data) + "\x1b\\")
			}
			return true
		})
	}
	// Keep enhanced keyboard negotiations in the host terminal. Geometry and
	// cursor reports are answered by the emulator at the application dimensions.
	for _, command := range []int{'u' | '>'<<8, 'u' | '<'<<8, 'u' | '='<<8, 'u' | '?'<<8, 'm' | '>'<<8} {
		s.e.RegisterCsiHandler(command, func(params ansi.Params) bool {
			if s.live {
				s.controls.WriteString("\x1b[")
				s.controls.WriteByte(byte(command >> 8))
				for i, param := range params {
					if i > 0 {
						s.controls.WriteByte(';')
					}
					fmt.Fprint(&s.controls, param.Param(0))
				}
				s.controls.WriteByte(byte(command))
			}
			return true
		})
	}
	for _, command := range []int{'=', '>'} {
		s.e.RegisterEscHandler(command, func() bool {
			if s.live {
				s.controls.WriteString("\x1b" + string(rune(command)))
			}
			return false
		})
	}
	return s
}

func (s *statusScreen) start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	// A pipe write completes when Read copies bytes, before its consumer has
	// processed them. This barrier drains all historical replies before relay
	// becomes live, including a reply from the final byte of the retained log.
	_, _ = io.WriteString(s.e.InputPipe(), replayBarrier)
	<-s.replayed
	s.controls.Reset()
	s.live = true
	s.relay.Store(true)
}

func (s *statusScreen) source(size task.Size) {
	if size.Validate() != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.e.Resize(min(512, int(size.Cols)), min(256, int(size.Rows)))
	s.previous = nil
	s.dirty = true
}

func (s *statusScreen) inputMode(mode ansi.Mode, enabled bool) {
	// Only physical input encoding follows the application. Screen modes stay
	// private to the viewport, so a clear/reset cannot erase the status row.
	switch mode {
	case ansi.DECMode(1), ansi.DECMode(9), ansi.DECMode(1000), ansi.DECMode(1002), ansi.DECMode(1003), ansi.DECMode(1004), ansi.DECMode(1005), ansi.DECMode(1006), ansi.DECMode(1015), ansi.DECMode(1016), ansi.DECMode(2004):
		if enabled {
			s.controls.WriteString(ansi.SetMode(mode))
		} else {
			s.controls.WriteString(ansi.ResetMode(mode))
		}
	}
}

func (s *statusScreen) close() {
	// End Read before Close changes the dependency's unsynchronized closed flag.
	_ = s.e.InputPipe().(io.Closer).Close()
	<-s.repliesDone
	_ = s.e.Close()
}

func (s *statusScreen) feed(data []byte) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() {
		if failure := recover(); failure != nil {
			err = fmt.Errorf("session display failed: %v; retry with --no-status", failure)
		}
	}()
	_, err = s.e.Write(data)
	s.dirty = true
	return err
}

func (s *statusScreen) resize(size task.Size, owner bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if size == s.size {
		return
	}
	s.size = size
	app := sessionSize(size, true)
	if owner {
		s.e.Resize(min(512, int(app.Cols)), min(256, int(app.Rows)))
	}
	s.previous = nil
	s.dirty = true
}

func (s *statusScreen) update(name string, resources *task.Resources) {
	s.mu.Lock()
	defer s.mu.Unlock()
	memory := "—"
	if resources != nil {
		switch {
		case resources.MemoryBytes >= 1<<30:
			memory = fmt.Sprintf("%.1f GiB", float64(resources.MemoryBytes)/(1<<30))
		case resources.MemoryBytes >= 1<<20:
			memory = fmt.Sprintf("%.0f MiB", float64(resources.MemoryBytes)/(1<<20))
		default:
			memory = fmt.Sprintf("%d KiB", resources.MemoryBytes/1024)
		}
	}
	if s.name != name || s.memory != memory {
		s.name, s.memory, s.dirty = name, memory, true
	}
}

func (s *statusScreen) label() string {
	label := s.name
	if len(s.id) >= 8 {
		label += " [" + s.id[:8] + "]"
	}
	if s.mode != "" {
		label += " · " + s.mode
	}
	return label
}

func statusLine(name, memory string, width int) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	right := " RSS " + memory + " "
	if ansi.StringWidth(right)+4 > width {
		return ansi.Truncate(" "+name, width, "…")
	}
	left := ansi.Truncate(" "+name, width-ansi.StringWidth(right)-1, "…")
	return left + strings.Repeat(" ", width-ansi.StringWidth(left)-ansi.StringWidth(right)) + right
}

func (s *statusScreen) draw(ctx context.Context, fd int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty {
		return nil
	}
	rows := vtscreen.Rows(s.e)
	app := sessionSize(s.size, true)
	rows = rows[:min(len(rows), int(app.Rows))]
	for y, row := range rows {
		rows[y] = ansi.Truncate(row, int(app.Cols), "")
	}
	for len(rows) < int(app.Rows) {
		rows = append(rows, "")
	}
	if s.size.Rows > 1 {
		rows = append(rows, "\x1b[0;1;37;44m"+statusLine(s.label(), s.memory, int(s.size.Cols))+"\x1b[0m")
	}
	var out strings.Builder
	out.WriteString(s.controls.String())
	s.controls.Reset()
	// Never use save/restore cursor here. Applications keep their own saved
	// cursor and wrap state in the emulator, even across status-only updates.
	out.WriteString("\x1b[?25l\x1b[?7l\x1b[0m")
	for y, row := range rows {
		if y < len(s.previous) && row == s.previous[y] {
			continue
		}
		fmt.Fprintf(&out, "\x1b[%d;1H\x1b[0m\x1b[K%s", y+1, row)
	}
	position := s.e.CursorPosition()
	fmt.Fprintf(&out, "\x1b[0m\x1b[%d;%dH", min(position.Y+1, int(app.Rows)), min(position.X+1, int(app.Cols)))
	style, blink := s.e.CursorStyle()
	shape := int(style)*2 + 1
	if !blink {
		shape++
	}
	fmt.Fprintf(&out, "\x1b[%d q", shape)
	if !s.e.CursorHidden() {
		out.WriteString("\x1b[?25h")
	}
	if err := writeTTY(ctx, fd, []byte(out.String())); err != nil {
		return err
	}
	s.previous, s.dirty = rows, false
	return nil
}

// Replay is bounded and offscreen. Historical device queries, clipboard writes
// and title changes never reach the physical terminal or the running process.
func (s *statusScreen) replay(ctx context.Context, repositoryRoot string, id task.ID) error {
	for _, name := range []string{"output.log.1", "output.log"} {
		fd, err := unix.Open(repositoryRoot+"/tasks/"+string(id)+"/"+name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		log := os.NewFile(uintptr(fd), name)
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0077 != 0 || int(stat.Uid) != os.Getuid() {
			log.Close()
			return errors.New("unsafe session output log")
		}
		reader := io.LimitReader(log, 8*1024*1024)
		buf := make([]byte, 32*1024)
		for {
			if err := ctx.Err(); err != nil {
				log.Close()
				return err
			}
			n, readErr := reader.Read(buf)
			if n > 0 {
				if err := s.feed(buf[:n]); err != nil {
					log.Close()
					return err
				}
			}
			if readErr != nil {
				log.Close()
				if !errors.Is(readErr, io.EOF) {
					return readErr
				}
				break
			}
		}
	}
	s.controls.Reset()
	return nil
}
