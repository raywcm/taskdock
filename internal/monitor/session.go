// Package monitor reconstructs a screen from bounded task output logs.
// Shared attachments carry keyboard input. The first controller owns the PTY
// dimensions; other previews render the same screen and crop it to their pane.
package monitor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/raywcm/taskdock/internal/ipc"
	"github.com/raywcm/taskdock/internal/protocol"
	"github.com/raywcm/taskdock/internal/store"
	"github.com/raywcm/taskdock/internal/task"
	"github.com/raywcm/taskdock/internal/terminal"
	"github.com/raywcm/taskdock/internal/vtscreen"
	"golang.org/x/sys/unix"
)

type Frame struct {
	Rows                            []string
	Width, Height, CursorX, CursorY int
	CursorHidden                    bool
	Alternate                       bool
	Ready, Ended                    bool
	Err                             error
}
type Session struct {
	mu         sync.RWMutex
	frame      Frame
	cancel     context.CancelFunc
	done       chan struct{}
	resize     chan task.Size
	connection *ipc.Session
	connected  chan struct{}
}

var ErrEmulation = errors.New("terminal preview parser failed")

func Open(ctx context.Context, repository *store.JSON, record task.Record, size task.Size) *Session {
	ctx, cancel := context.WithCancel(ctx)
	s := &Session{cancel: cancel, done: make(chan struct{}), connected: make(chan struct{}), resize: make(chan task.Size, 1)}
	go s.supervise(func() { s.run(ctx, repository, record, size) })
	return s
}

// A preview is disposable. A dependency panic must unwind its leases/readers,
// then become a pane error instead of terminating the terminal-owning process.
func (s *Session) supervise(run func()) {
	defer close(s.done)
	defer s.cancel()
	defer func() {
		if failure := recover(); failure != nil {
			s.mu.Lock()
			s.frame.Err = fmt.Errorf("%w: %v", ErrEmulation, failure)
			s.mu.Unlock()
		}
	}()
	run()
}
func (s *Session) Snapshot() Frame { s.mu.RLock(); defer s.mu.RUnlock(); return s.frame }
func (s *Session) Resize(size task.Size) {
	if size.Validate() != nil {
		return
	}
	select {
	case s.resize <- size:
	default:
		select {
		case <-s.resize:
		default:
		}
		select {
		case s.resize <- size:
		default:
		}
	}
}

// Input uses the monitor's existing shared display lease. Switching between
// preview and input never reconnects or changes the PTY dimensions.
func (s *Session) Input(data []byte) error {
	if s.connected == nil {
		return errors.New("session is unavailable for input")
	}
	select {
	case <-s.connected:
	case <-s.done:
		return errors.New("session is unavailable for input")
	}
	s.mu.RLock()
	conn, frame := s.connection, s.frame
	s.mu.RUnlock()
	if conn == nil || frame.Ended || frame.Err != nil {
		return errors.New("session is unavailable for input")
	}
	for len(data) > 0 {
		n := min(len(data), protocol.MaxPayload)
		if err := conn.Send(protocol.Frame{Kind: protocol.Input, Payload: data[:n]}); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}
func (s *Session) Close() { s.cancel(); <-s.done }

func dimensions(size task.Size) (int, int) {
	if size.Validate() != nil {
		return 80, 24
	}
	return min(512, int(size.Cols)), min(256, int(size.Rows))
}
func newScreen(width, height int) *vt.Emulator {
	e := vtscreen.New(width, height)
	// Previews discard device replies and never replay them into the application.
	if closer, ok := e.InputPipe().(io.Closer); ok {
		_ = closer.Close()
	}
	return e
}
func screenFrame(e *vt.Emulator) Frame {
	return Frame{Rows: vtscreen.Rows(e), Width: e.Width(), Height: e.Height(), CursorX: e.CursorPosition().X, CursorY: e.CursorPosition().Y, CursorHidden: e.CursorHidden(), Alternate: e.IsAltScreen(), Ready: true}
}
func (s *Session) publish(e *vt.Emulator, err error, ended bool) {
	frame := screenFrame(e)
	frame.Err = err
	frame.Ended = ended
	s.mu.Lock()
	s.frame = frame
	s.mu.Unlock()
}
func openLog(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	var stat unix.Stat_t
	if err = unix.Fstat(fd, &stat); err == nil && (stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0077 != 0 || int(stat.Uid) != os.Getuid()) {
		err = errors.New("unsafe monitor output log")
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func feed(ctx context.Context, e *vt.Emulator, f *os.File, limit int64) (bool, error) {
	buf := make([]byte, 32*1024)
	for limit > 0 {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		n, err := f.Read(buf[:min(int64(len(buf)), limit)])
		if n > 0 {
			if _, writeErr := e.Write(buf[:n]); writeErr != nil {
				return false, writeErr
			}
			limit -= int64(n)
		}
		if errors.Is(err, io.EOF) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
	}
	return false, nil
}
func actualSize(ctx context.Context, repository *store.JSON, r task.Record, fallback task.Size) task.Size {
	if r.Runtime == nil {
		return fallback
	}
	probe, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	conn, err := ipc.Connect(probe, repository.Root(), *r.Runtime, "control", nil)
	if err != nil {
		return fallback
	}
	defer conn.Close()
	if conn.Welcome.Size != nil && conn.Welcome.Size.Validate() == nil {
		return *conn.Welcome.Size
	}
	return fallback
}
func (s *Session) run(ctx context.Context, repository *store.JSON, r task.Record, wanted task.Size) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	original := actualSize(ctx, repository, r, r.Spec.Size)
	width, height := dimensions(original)
	wantedWidth, wantedHeight := dimensions(wanted)
	wanted = task.Size{Rows: uint16(wantedHeight), Cols: uint16(wantedWidth)}
	if ctx.Err() != nil {
		return
	}
	e := newScreen(width, height)
	defer e.Close()
	if r.Runtime == nil {
		s.publish(e, errors.New("task has not started"), false)
		return
	}
	// Enter directly at the pane size, including with old brokers whose
	// Welcome cannot report the current size. Never bounce through Spec.Size.
	conn, err := ipc.ConnectMonitor(ctx, repository.Root(), *r.Runtime, &wanted)
	if err != nil {
		if ctx.Err() == nil {
			s.publish(e, err, false)
		}
		return
	}
	s.mu.Lock()
	s.connection = conn
	close(s.connected)
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.connection = nil; s.mu.Unlock() }()

	frames := make(chan terminal.Incoming, 64)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer close(frames)
		for {
			frame, err := protocol.Read(conn.Conn)
			select {
			case frames <- terminal.Incoming{Frame: frame, Err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	readerFinished := false
	defer func() {
		_ = conn.Send(protocol.Frame{Kind: protocol.Detach})
		cancel()
		_ = conn.Close()
		<-readerDone
	}()
	owner := conn.Welcome.AttachMode == "" || conn.Welcome.SizeOwner
	// A revocation notice can race a failed resize/write. Preserve the broker's
	// reason rather than hiding a deliberate takeover behind a broken-pipe error.
	send := func(frame protocol.Frame) error {
		cause := conn.Send(frame)
		if cause == nil {
			return nil
		}
		timer := time.NewTimer(100 * time.Millisecond)
		defer timer.Stop()
		for {
			select {
			case next, ok := <-frames:
				if !ok {
					return cause
				}
				if next.Frame.Kind == protocol.Error {
					return ipc.FrameError(next.Frame)
				}
				if next.Err != nil {
					return cause
				}
			case <-timer.C:
				return cause
			case <-ctx.Done():
				return cause
			}
		}
	}
	sourceSize := wanted
	if conn.Welcome.Size != nil {
		sourceSize = *conn.Welcome.Size
	}
	sizeNotices := false
	lastProbe := time.Now()
	var pending []terminal.Incoming

	current, _ := repository.Path(r.Spec.ID, "output.log")
	previous, _ := repository.Path(r.Spec.ID, "output.log.1")
	var file *os.File
	defer func() {
		if file != nil {
			file.Close()
		}
	}()
	// Prime both retained generations, through the offscreen emulator only.
	if first, err := openLog(previous); err == nil {
		_, err = feed(ctx, e, first, 8*1024*1024)
		file = first
		if err != nil {
			if ctx.Err() == nil {
				s.publish(e, err, false)
			}
			return
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		s.publish(e, err, false)
		return
	}
	// Seed at the task's original dimensions before resizing the live PTY.
	if first, err := openLog(current); err == nil {
		if file != nil {
			file.Close()
		}
		file = first
		if _, err = feed(ctx, e, file, 8*1024*1024); err != nil {
			if ctx.Err() == nil {
				s.publish(e, err, false)
			}
			return
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		s.publish(e, err, false)
		return
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	needsSize := true
	appliedSize := sourceSize // Only the size owner changes the PTY.
	endSeen := false
	var missingSince time.Time

	for {
		if ctx.Err() != nil {
			return
		}

		// One reader owns framing. Role changes and resize notices are consumed even
		// when output is quiet, so followers never fight the controlling window.
	drainFrames:
		for count := 0; count < 128 && !readerFinished; count++ {
			var next terminal.Incoming
			if len(pending) > 0 {
				next, pending = pending[0], pending[1:]
			} else {
				select {
				case value, ok := <-frames:
					if !ok {
						s.publish(e, io.ErrUnexpectedEOF, false)
						return
					}
					next = value
				default:
					break drainFrames
				}
			}
			if next.Err != nil {
				s.publish(e, next.Err, false)
				return
			}
			switch next.Frame.Kind {
			case protocol.Exit:
				readerFinished = true
			case protocol.Error:
				s.publish(e, ipc.FrameError(next.Frame), false)
				return
			case protocol.Notice:
				var notice protocol.NoticeMessage
				if err := ipc.Decode(next.Frame.Payload, &notice); err != nil {
					s.publish(e, err, false)
					return
				}
				if notice.Type == "role" {
					if notice.SizeOwner && !owner {
						needsSize = true
					}
					owner = notice.SizeOwner
					if notice.Size != nil && notice.Size.Validate() == nil {
						sourceSize = *notice.Size
						sizeNotices = true
						if !owner {
							needsSize = true
						}
					}
				} else if notice.Type == "refresh" && owner {
					redraw, err := terminal.Repaint(ctx, send, frames, wanted, 2*time.Second)
					if err != nil {
						if ctx.Err() == nil {
							s.publish(e, err, false)
						}
						return
					}
					pending = append(pending, redraw...)
				}
			}
		}
		// Older brokers lack size notices. A follower probes without resizing the
		// application, and stops probing once the broker advertises live sizes.
		if !owner && !sizeNotices && time.Since(lastProbe) >= 500*time.Millisecond {
			lastProbe = time.Now()
			latest := actualSize(ctx, repository, r, sourceSize)
			if latest != sourceSize {
				sourceSize = latest
				needsSize = true
			}
		}

		select {
		case wanted = <-s.resize:
			needsSize = true
		default:
		}
		if needsSize {
			w, h := dimensions(wanted)
			wanted = task.Size{Rows: uint16(h), Cols: uint16(w)}
			if owner && wanted != appliedSize {
				if err := send(ipc.JSON(protocol.Resize, wanted)); err != nil {
					s.publish(e, err, false)
					return
				}
				appliedSize = wanted
				sourceSize = wanted
			}
			if !owner {
				w, h = dimensions(sourceSize)
			}
			if e.Width() != w || e.Height() != h {
				e.Resize(w, h)
			}
			needsSize = false
		}
		if file == nil {
			var err error
			file, err = openLog(current)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				s.publish(e, err, false)
				return
			}
			if errors.Is(err, os.ErrNotExist) {
				if missingSince.IsZero() {
					missingSince = time.Now()
				}
				if time.Since(missingSince) >= 2*time.Second {
					s.publish(e, errors.New("task output is unavailable"), false)
					return
				}
			} else {
				missingSince = time.Time{}
			}
		}
		atEnd := true
		if file != nil {
			var err error
			atEnd, err = feed(ctx, e, file, 2*1024*1024)
			if err != nil {
				if ctx.Err() == nil {
					s.publish(e, err, false)
				}
				return
			}
			if atEnd {
				opened, openErr := file.Stat()
				latest, statErr := os.Stat(current)
				if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
					s.publish(e, statErr, false)
					return
				}
				if openErr != nil {
					s.publish(e, openErr, false)
					return
				}
				if statErr == nil && !os.SameFile(opened, latest) {
					// If several generations passed while behind, read the latest retained
					// predecessor before current; never loop over a generation already read.
					candidate := current
					if older, err := os.Stat(previous); err == nil && !os.SameFile(opened, older) {
						candidate = previous
					}
					next, err := openLog(candidate)
					if err == nil {
						file.Close()
						file = next
						s.publish(e, nil, false)
						continue
					}
					if !errors.Is(err, os.ErrNotExist) {
						s.publish(e, err, false)
						return
					}
				}
			}
		}
		latest, err := repository.Load(ctx, r.Spec.ID)
		if err != nil {
			if ctx.Err() == nil {
				s.publish(e, err, false)
			}
			return
		}
		ended := latest.Runtime != nil && (latest.Runtime.State == task.Exited || latest.Runtime.State == task.Failed)
		if file != nil || ended {
			s.publish(e, nil, ended)
		}
		if ended && atEnd {
			if endSeen {
				return
			}
			endSeen = true
			continue
		}
		endSeen = false

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
