package terminal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/raywcm/taskdock/internal/ipc"
	"github.com/raywcm/taskdock/internal/protocol"
	"github.com/raywcm/taskdock/internal/store"
	"github.com/raywcm/taskdock/internal/task"
	"golang.org/x/sys/unix"
)

var privateModes = map[int]bool{1: true, 6: true, 7: true, 9: true, 25: true, 47: true, 1047: true, 1049: true, 1000: true, 1002: true, 1003: true, 1004: true, 1005: true, 1006: true, 1015: true, 1016: true, 2004: true}

type recovery struct {
	modes task.TerminalModes
	pen   uv.Style
}

func newRecovery(r task.Runtime, cached task.TerminalModes) *recovery {
	modes := task.TerminalModes{SchemaVersion: 1, TaskID: r.TaskID, InstanceID: r.InstanceID, Private: map[int]bool{}}
	if cached.TaskID == r.TaskID && cached.InstanceID == r.InstanceID {
		for mode, set := range cached.Private {
			if privateModes[mode] {
				modes.Private[mode] = set
			}
		}
		modes.Insert = cached.Insert
		modes.Keypad = cached.Keypad
		if cached.CursorStyle != nil && *cached.CursorStyle >= 0 && *cached.CursorStyle <= 6 {
			modes.CursorStyle = cached.CursorStyle
		}
	}
	state := &recovery{modes: modes}
	if cached.TaskID == state.modes.TaskID && cached.InstanceID == state.modes.InstanceID && len(cached.Rendition) <= 512 {
		// Checkpoints contain generated SGR, but still parse them as untrusted
		// input. Never replay saved strings or let them change other modes.
		p := ansi.NewParser()
		p.SetDataSize(1)
		p.SetHandler(ansi.Handler{HandleCsi: func(cmd ansi.Cmd, params ansi.Params) {
			if cmd == 'm' {
				uv.ReadStyle(params, &state.pen)
			}
		}})
		for _, b := range []byte(cached.Rendition) {
			p.Advance(b)
		}
		state.modes.Rendition = state.rendition()
	}
	return state
}

func (r *recovery) parser() *ansi.Parser {
	p := ansi.NewParser()
	p.SetDataSize(1) // OSC/DCS contents are ignored, never replayed.
	p.SetHandler(ansi.Handler{
		HandleCsi: func(cmd ansi.Cmd, params ansi.Params) {
			if cmd == 'm' {
				uv.ReadStyle(params, &r.pen)
				return
			}
			if cmd.Intermediate() == ' ' && cmd.Prefix() == 0 && cmd.Final() == 'q' {
				value, _, _ := params.Param(0, 0)
				if value >= 0 && value <= 6 {
					r.modes.CursorStyle = &value
				}
				return
			}
			if cmd.Intermediate() != 0 || (cmd.Final() != 'h' && cmd.Final() != 'l') {
				return
			}
			set := cmd.Final() == 'h'
			params.ForEach(0, func(_ int, mode int, more bool) {
				if more {
					return
				}
				if cmd.Prefix() == 0 && mode == 4 {
					v := set
					r.modes.Insert = &v
					return
				}
				if cmd.Prefix() != '?' || !privateModes[mode] {
					return
				}
				if mode == 47 || mode == 1047 || mode == 1049 {
					for _, alias := range []int{47, 1047, 1049} {
						r.modes.Private[alias] = false
					}
				}
				if set && (mode == 9 || mode == 1000 || mode == 1002 || mode == 1003) {
					for _, alias := range []int{9, 1000, 1002, 1003} {
						r.modes.Private[alias] = false
					}
				}
				r.modes.Private[mode] = set
			})
		},
		HandleEsc: func(cmd ansi.Cmd) {
			if cmd.Prefix() != 0 || cmd.Intermediate() != 0 {
				return
			}
			switch cmd.Final() {
			case '=':
				v := true
				r.modes.Keypad = &v
			case '>':
				v := false
				r.modes.Keypad = &v
			case 'c':
				r.modes.Private = map[int]bool{}
				r.modes.Insert = nil
				r.modes.Keypad = nil
				r.modes.CursorStyle = nil
				r.pen = uv.Style{}
				r.modes.Rendition = ""
			}
		},
	})
	return p
}

// inspect scans retained logs only at connection boundaries. The normal PTY and
// socket output path remains an opaque byte stream, with no parser or renderer.
func (r *recovery) inspect(repository *store.JSON) error {
	defer func() { r.modes.Rendition = r.rendition() }()
	parser := r.parser()
	for _, name := range []string{"output.log.1", "output.log"} {
		path, err := repository.Path(r.modes.TaskID, name)
		if err != nil {
			return err
		}
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		file := os.NewFile(uintptr(fd), path)
		info, err := file.Stat()
		if err != nil {
			file.Close()
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || int(stat.Uid) != os.Getuid() {
			file.Close()
			return errors.New("unsafe terminal recovery log")
		}
		reader := io.LimitReader(file, 8*1024*1024)
		buf := make([]byte, 32*1024)
		for {
			n, readErr := reader.Read(buf)
			for _, b := range buf[:n] {
				parser.Advance(b)
			}
			if readErr != nil {
				if !errors.Is(readErr, io.EOF) {
					file.Close()
					return readErr
				}
				break
			}
		}
		file.Close()
	}
	return nil
}
func (r *recovery) fullscreen() bool {
	return r.modes.Private[47] || r.modes.Private[1047] || r.modes.Private[1049]
}

// Apply after the identity banner, before any application bytes. Incremental
// redraws can assume their previous foreground, background and attributes still
// exist even though the manager (or another session) changed the physical pen.
func (r *recovery) rendition() string {
	if r.pen.IsZero() {
		return ansi.ResetStyle
	}
	return ansi.ResetStyle + r.pen.String()
}

// prefix synthesizes only whitelisted modes. No text, OSC, clipboard commands,
// terminal queries or other historical control sequences are replayed.
func (r *recovery) prefix() string {
	var out strings.Builder
	if r.fullscreen() {
		out.WriteString("\x1b[?1049h\x1b[0m\x1b[r\x1b[?6l\x1b[H\x1b[2J")
	}
	modes := make([]int, 0, len(r.modes.Private))
	for mode := range r.modes.Private {
		if mode != 47 && mode != 1047 && mode != 1049 {
			modes = append(modes, mode)
		}
	}
	sort.Ints(modes)
	// Mouse protocol resets can clear the active protocol, even when resetting
	// an inactive alias. Apply resets first, then enable the requested modes.
	for _, set := range []bool{false, true} {
		for _, mode := range modes {
			if r.modes.Private[mode] != set {
				continue
			}
			suffix := 'l'
			if set {
				suffix = 'h'
			}
			fmt.Fprintf(&out, "\x1b[?%d%c", mode, suffix)
		}
	}
	if r.modes.Insert != nil {
		suffix := 'l'
		if *r.modes.Insert {
			suffix = 'h'
		}
		fmt.Fprintf(&out, "\x1b[4%c", suffix)
	}
	if r.modes.Keypad != nil {
		if *r.modes.Keypad {
			out.WriteString("\x1b=")
		} else {
			out.WriteString("\x1b>")
		}
	}
	if r.modes.CursorStyle != nil {
		fmt.Fprintf(&out, "\x1b[%d q", *r.modes.CursorStyle)
	}
	return out.String()
}

// Incoming is read by one continuous frame reader. Recovery never interrupts a
// partially read frame with a socket deadline, which would lose framing.
type Incoming struct {
	Frame protocol.Frame
	Err   error
}

func Repaint(ctx context.Context, send func(protocol.Frame) error, frames <-chan Incoming, size task.Size, timeout time.Duration) ([]Incoming, error) {
	// Wait for the application's redraw before restoring the real size. A fixed
	// sleep can coalesce the two SIGWINCH signals under load, leaving ncurses'
	// cached screen unchanged. Existing protocol v1 brokers remain compatible.
	smaller := size
	if smaller.Rows > 1 {
		smaller.Rows--
	} else if smaller.Cols > 1 {
		smaller.Cols--
	} else {
		return nil, nil
	}
	if err := send(ipc.JSON(protocol.Resize, smaller)); err != nil {
		return nil, err
	}
	var pending []Incoming
	var bytes int
	redrawn := false
	parser := ansi.NewParser()
	parser.SetDataSize(1)
	parser.SetHandler(ansi.Handler{
		HandleCsi: func(cmd ansi.Cmd, params ansi.Params) {
			if cmd.Final() == 'J' && cmd.Prefix() == 0 && cmd.Intermediate() == 0 {
				mode, _, _ := params.Param(0, 0)
				if mode == 2 || mode == 3 {
					redrawn = true
				}
			}
		},
		HandleEsc: func(cmd ansi.Cmd) {
			if cmd.Final() == 'c' && cmd.Prefix() == 0 && cmd.Intermediate() == 0 {
				redrawn = true
			}
		},
	})
	timer := time.NewTimer(timeout)
	defer timer.Stop()
wait:
	for bytes < 2*1024*1024 && len(pending) < 256 {
		select {
		case <-ctx.Done():
			break wait
		case <-timer.C:
			break wait
		case next, ok := <-frames:
			if !ok {
				break wait
			}
			pending = append(pending, next)
			bytes += len(next.Frame.Payload)
			if next.Err != nil {
				return pending, next.Err
			}
			if next.Frame.Kind == protocol.Error {
				return pending, ipc.FrameError(next.Frame)
			}
			if next.Frame.Kind == protocol.Exit {
				return pending, nil
			}
			if next.Frame.Kind == protocol.Output {
				for _, b := range next.Frame.Payload {
					parser.Advance(b)
				}
				if redrawn {
					break wait
				}
			}
		}
	}
	// Always restore dimensions, including cancellation and apps which do not
	// emit a recognizable clear. Preserve every frame in its original order.
	err := send(ipc.JSON(protocol.Resize, size))
	return pending, errors.Join(ctx.Err(), err)
}
