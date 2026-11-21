package terminal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/raywcm/taskdock/internal/config"
	"github.com/raywcm/taskdock/internal/protocol"
	"github.com/raywcm/taskdock/internal/task"
	"github.com/raywcm/taskdock/internal/vtscreen"
	"golang.org/x/sys/unix"
)

// A fixed status row requires a bounded screen. The application's cursor and
// saved cursor belong to the emulator, independently of the physical status row.
// --no-status keeps the native byte-forwarding path available.
type statusScreen struct {
	mu            sync.Mutex
	e             *vt.Emulator
	size          task.Size
	pixels        image.Point
	name          string
	windowName    string
	command       string
	title         string
	clock         string
	mode          string
	rowStyle      string
	memory        string
	previous      []string
	controls      strings.Builder
	viewport      vtscreen.Viewport
	appMouse      vtscreen.MouseModes
	hostMouse     vtscreen.MouseModes
	capture       vtscreen.MouseCapture
	scrollMouse   bool
	mouseReported bool
	rawInput      bool
	readOnly      bool
	live          bool
	relay         atomic.Bool
	follow        atomic.Bool
	dirty         bool
	repliesDone   chan struct{}
	replayed      chan struct{}
}

const replayBarrier = "\x00TDOCK-REPLAY-END\x00"

func sessionSize(size task.Size, status bool) task.Size {
	if status && size.Rows > 1 {
		size.Rows--
	}
	return size
}

func (s *statusScreen) visibleArea() image.Rectangle {
	app := sessionSize(s.size, true)
	return image.Rect(0, 0, min(int(app.Cols), s.e.Width()), min(int(app.Rows), s.e.Height()))
}

func newStatusScreen(size task.Size, spec task.Spec, send func(protocol.Frame) error) *statusScreen {
	app := sessionSize(size, true)
	s := &statusScreen{e: vtscreen.New(int(app.Cols), int(app.Rows)), size: size, name: spec.Name, windowName: spec.WindowName, clock: time.Now().Format("15:04 02-Jan-06"), memory: "—", dirty: true, repliesDone: make(chan struct{}), replayed: make(chan struct{})}
	if len(spec.Command) != 0 {
		s.command = filepath.Base(spec.Command[0])
	}
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
	// x/vt splits title payloads on every semicolon. Only the first
	// semicolon separates the OSC number from the application-supplied text.
	for _, code := range []int{0, 2} {
		s.e.RegisterOscHandler(code, func(data []byte) bool {
			_, title, ok := strings.Cut(string(data), ";")
			if ok {
				s.title = ansi.Truncate(statusText(title), 256, "…")
			}
			return true
		})
	}
	s.e.RegisterEscHandler('c', func() bool {
		s.title = ""
		s.appMouse = vtscreen.MouseModes{}
		s.capture.Reset()
		s.viewport.Reset()
		return false
	})
	// Palette queries must reach the real terminal. Its replies pass through the
	// existing input decoder, preserving light/dark detection and custom palettes.
	for _, code := range []int{4, 9, 10, 11, 12, 52, 104, 110, 111, 112, 777} {
		s.e.RegisterOscHandler(code, func(data []byte) bool {
			if s.live {
				// OSC 9 also has command extensions on some hosts. Relay plain
				// notifications only, including the OSC 777 notify form.
				if code == 9 || code == 777 {
					_, message, _ := strings.Cut(string(data), ";")
					first, _, _ := strings.Cut(message, ";")
					if _, err := strconv.Atoi(first); (code == 9 && err == nil) || (code == 777 && first != "notify") ||
						len(data) > 4096 || statusText(message) != message {
						return true
					}
				}
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
	s.viewport.Reset()
	s.previous = nil
	s.dirty = true
}

func (s *statusScreen) inputMode(mode ansi.Mode, enabled bool) {
	before := s.appMouse
	if s.appMouse.Set(mode, enabled) {
		if before != s.appMouse {
			s.capture.Reset()
		}
		return
	}
	// Only physical input encoding follows the application. Screen modes stay
	// private to the viewport, so a clear/reset cannot erase the status row.
	switch mode {
	case ansi.DECMode(1), ansi.DECMode(1004), ansi.DECMode(2004):
		if enabled {
			s.controls.WriteString(ansi.SetMode(mode))
		} else {
			s.controls.WriteString(ansi.ResetMode(mode))
		}
	}
}

func (s *statusScreen) updateMouse(out *strings.Builder) {
	next := s.appMouse
	s.scrollMouse = !s.rawInput && next.Tracking == 0 && !s.e.IsAltScreen() && s.e.ScrollbackLen() > 0
	if s.scrollMouse {
		next = vtscreen.MouseModes{Tracking: ansi.ModeMouseNormal, Encoding: ansi.ModeMouseExtSgr}
	} else if s.readOnly {
		next = vtscreen.MouseModes{}
	} else if !s.rawInput && next.CellTracking() != 0 {
		// Use cell coordinates on the host, then encode for the task after hit testing.
		next.Encoding = ansi.ModeMouseExtSgr
	}
	for _, change := range [][2]ansi.DECMode{{s.hostMouse.Tracking, next.Tracking}, {s.hostMouse.Encoding, next.Encoding}} {
		if before, after := change[0], change[1]; before != after && before != 0 {
			out.WriteString(ansi.ResetMode(before))
		}
	}
	for _, change := range [][2]ansi.DECMode{{s.hostMouse.Encoding, next.Encoding}, {s.hostMouse.Tracking, next.Tracking}} {
		if before, after := change[0], change[1]; before != after && after != 0 {
			out.WriteString(ansi.SetMode(after))
		}
	}
	s.hostMouse = next
	if next.Tracking != 0 {
		s.mouseReported = true
	}
}

func (s *statusScreen) mouseInput(raw []byte) []byte {
	event := vtscreen.DecodeMouse(raw)
	if event == nil {
		return raw
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applyFollow()
	area := s.visibleArea()
	modes := s.appMouse
	switch {
	case modes.Tracking == 0:
		mouse := event.Mouse()
		if s.scrollMouse && image.Pt(mouse.X, mouse.Y).In(area) {
			if lines := vtscreen.WheelLines(mouse.Button); lines != 0 {
				s.viewport.Scroll(s.e, lines)
				s.dirty = true
			}
		}
		// Keep delayed reports out of the shell after an application relinquishes
		// the mouse. A frontend that never enabled tracking preserves opaque input.
		if s.mouseReported {
			return nil
		}
		return raw
	case s.readOnly:
		return nil
	case modes.Tracking == ansi.ModeMouseHighlight:
		return raw
	case modes.Encoding == ansi.ModeMouseExtSgrPixel:
		if s.pixels.X == 0 || s.pixels.Y == 0 {
			return raw
		}
		area.Max.X = area.Max.X * s.pixels.X / int(s.size.Cols)
		area.Max.Y = area.Max.Y * s.pixels.Y / int(s.size.Rows)
		// Pixel reports use the SGR syntax with pixel coordinates.
		modes.Encoding = ansi.ModeMouseExtSgr
	}
	return s.capture.Input(modes, event, area)
}

func (s *statusScreen) followLive() {
	// The renderer holds the screen lock while writing to the terminal.
	// A slow viewer must not prevent its input reaching other attachments.
	s.follow.Store(true)
}

func (s *statusScreen) applyFollow() {
	if s.follow.Swap(false) && s.viewport.Scrolled() {
		s.viewport.Reset()
		s.dirty = true
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
	if s.appMouse.Tracking != 0 || s.e.IsAltScreen() || s.e.ScrollbackLen() == 0 {
		s.viewport.Reset()
	}
	s.dirty = true
	return err
}

func (s *statusScreen) resize(size task.Size, pixels image.Point, owner bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pixels = pixels
	if size == s.size {
		return
	}
	s.size = size
	app := sessionSize(size, true)
	if owner {
		s.e.Resize(min(512, int(app.Cols)), min(256, int(app.Rows)))
	}
	s.viewport.Reset()
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
	command := s.command
	if resources != nil && resources.ForegroundCommand != "" {
		command = resources.ForegroundCommand
	}
	clock := time.Now().Format("15:04 02-Jan-06")
	if s.name != name || s.command != command || s.memory != memory || s.clock != clock {
		s.name, s.command, s.memory, s.clock, s.dirty = name, command, memory, clock, true
	}
}

func (s *statusScreen) setAppearance(settings config.Settings, background color.Color) {
	style := ansi.NewStyle().Reset().Bold()
	mode := ResolveColorMode(settings.ColorMode, background)
	var fg, bg color.Color = ansi.White, ansi.Blue
	if mode == "light" {
		fg, bg = ansi.Blue, ansi.White
	}
	if settings.Theme == "terminal" {
		switch colorprofile.Env(os.Environ()) {
		case colorprofile.TrueColor:
			fg, bg = ansi.HexColor("#dce6f3"), ansi.HexColor("#263b58")
			if mode == "light" {
				fg, bg = ansi.HexColor("#2f517f"), ansi.HexColor("#dce6f3")
			}
		case colorprofile.ANSI256:
			if mode == "light" {
				bg = ansi.IndexedColor(153) // Pale blue in the xterm palette.
			}
		}
	}
	if os.Getenv("NO_COLOR") == "" {
		style = style.ForegroundColor(fg).BackgroundColor(bg)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rowStyle, s.dirty = style.String(), true
}

func statusText(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) || r == '\u2028' || r == '\u2029' {
			return -1
		}
		return r
	}, text)
}

func (s *statusScreen) statusLine(width int) string {
	if width <= 0 {
		return ""
	}
	window := s.windowName
	if window == "" {
		window = s.command
	}
	name := statusText(s.name)
	window = statusText(window)
	right := " RSS " + s.memory
	if width >= 48 {
		right += "  " + s.clock
	}
	room := width - ansi.StringWidth(right)
	if room < 8 {
		label := ansi.Truncate("["+name+"]", width, "…")
		return label + strings.Repeat(" ", width-ansi.StringWidth(label))
	}
	if s.title != "" && room >= 18 {
		right = " \"" + ansi.Truncate(s.title, room/2-2, "…") + "\"" + right
	}
	// Reserve the delimiters and active-window mark, even for long CJK names.
	available := width - ansi.StringWidth(right) - 7
	name = ansi.Truncate(name, max(1, available-min(available/2, ansi.StringWidth(window))), "…")
	window = ansi.Truncate(window, max(1, available-ansi.StringWidth(name)), "…")
	left := "[" + name + "] 0:" + window + "*"
	if s.mode != "" && ansi.StringWidth(left+" · "+s.mode+right) < width {
		left += " · " + s.mode
	}
	return left + strings.Repeat(" ", width-ansi.StringWidth(left+right)) + right
}

func (s *statusScreen) draw(ctx context.Context, fd int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applyFollow()
	if !s.dirty {
		return nil
	}
	rows := s.viewport.Rows(s.e)
	app := sessionSize(s.size, true)
	rows = rows[:min(len(rows), int(app.Rows))]
	for y, row := range rows {
		rows[y] = ansi.Truncate(row, int(app.Cols), "")
	}
	for len(rows) < int(app.Rows) {
		rows = append(rows, "")
	}
	if s.size.Rows > 1 {
		rows = append(rows, s.rowStyle+s.statusLine(int(s.size.Cols))+"\x1b[0m")
	}
	var out strings.Builder
	out.WriteString(s.controls.String())
	s.controls.Reset()
	s.updateMouse(&out)
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
	if !s.e.CursorHidden() && !s.viewport.Scrolled() && position.In(s.visibleArea()) {
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
