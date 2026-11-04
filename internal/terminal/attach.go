package terminal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/raywcm/taskdock/internal/config"
	"github.com/raywcm/taskdock/internal/i18n"
	"github.com/raywcm/taskdock/internal/ipc"
	"github.com/raywcm/taskdock/internal/protocol"
	"github.com/raywcm/taskdock/internal/store"
	"github.com/raywcm/taskdock/internal/task"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

type AttachResult struct {
	Detached bool
	ExitCode int
}

var ErrConnect = errors.New("could not attach to broker")

type AttachOptions struct {
	NoEscape bool
	Mode     string
}

func CurrentSize(input *os.File) task.Size {
	cols, rows, err := term.GetSize(int(input.Fd()))
	if err != nil || rows < 1 || cols < 1 || rows > 65535 || cols > 65535 {
		return task.Size{Rows: 24, Cols: 80}
	}
	return task.Size{Rows: uint16(rows), Cols: uint16(cols)}
}

// Attach owns a separately opened, pollable tty so cancellation never leaves an
// orphaned reader competing with the next Bubble Tea program for standard input.
func Attach(ctx context.Context, state string, record task.Record, input, output *os.File, noEscape bool) (AttachResult, error) {
	return AttachWithOptions(ctx, state, record, input, output, AttachOptions{NoEscape: noEscape})
}

func AttachWithOptions(ctx context.Context, state string, record task.Record, input, output *os.File, options AttachOptions) (AttachResult, error) {
	if record.Runtime == nil {
		return AttachResult{}, errors.New("task has not started")
	}
	r := *record.Runtime
	if !term.IsTerminal(int(input.Fd())) || !term.IsTerminal(int(output.Fd())) {
		return AttachResult{}, errors.New("attach requires an interactive terminal")
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return AttachResult{}, err
	}
	defer tty.Close()
	size := CurrentSize(tty)
	repository, err := store.Open(state)
	if err != nil {
		return AttachResult{}, err
	}
	cached, _ := repository.LoadTerminalModes(r.TaskID)
	restore := newRecovery(r, cached)
	if err := restore.inspect(repository); err != nil {
		return AttachResult{}, fmt.Errorf("recover terminal modes: %w", err)
	}
	_ = repository.SaveTerminalModes(restore.modes)
	// Scan before taking the output subscription, so historical log size cannot
	// make a fast live producer overflow the attach queue while we inspect it.
	session, err := ipc.ConnectAttachment(ctx, state, r, &size, options.Mode)
	if err != nil {
		return AttachResult{}, fmt.Errorf("%w: %w", ErrConnect, err)
	}
	defer session.Close()
	preferences, settingsErr := repository.Settings()
	if settingsErr != nil {
		preferences = config.Defaults()
	}
	locale := i18n.New(preferences.Language)
	identity := newIdentity(record.Spec, locale, size, options.NoEscape)
	readOnly := session.Welcome.AttachMode == protocol.AttachReadOnly
	var sizeOwner atomic.Bool
	// Older brokers have no size-owner field and retain the exclusive semantics.
	sizeOwner.Store(session.Welcome.AttachMode == "" || session.Welcome.SizeOwner)
	if session.Welcome.AttachMode != "" && session.Welcome.AttachMode != protocol.AttachExclusive {
		label := locale.Text("mode_" + session.Welcome.AttachMode)
		identity = identity.withMode(label, size)
	}
	var result AttachResult
	titleActive := false
	err = WithRaw(tty, func() error {
		attachCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		fd := int(tty.Fd())
		if err := unix.SetNonblock(fd, true); err != nil {
			return err
		}
		resize := make(chan os.Signal, 1)
		signal.Notify(resize, syscall.SIGWINCH)
		defer signal.Stop(resize)
		type event struct {
			result AttachResult
			err    error
		}
		events := make(chan event, 2)
		var wg sync.WaitGroup
		frames := make(chan Incoming, 64)
		wg.Go(func() {
			defer close(frames)
			for {
				frame, err := protocol.Read(session.Conn)
				select {
				case frames <- Incoming{Frame: frame, Err: err}:
				case <-attachCtx.Done():
					return
				}
				if err != nil {
					return
				}
			}
		})
		defer func() {
			cancel()
			_ = session.Close()
			wg.Wait()
			if restore.inspect(repository) == nil {
				_ = repository.SaveTerminalModes(restore.modes)
			}
		}()
		if err := writeTTY(attachCtx, fd, []byte(identity.start())); err != nil {
			return err
		}
		titleActive = true
		var pending []Incoming
		if session.Welcome.Replay != "initial" {
			if err := writeTTY(attachCtx, fd, []byte(restore.prefix())); err != nil {
				return err
			}
		}
		if err := writeTTY(attachCtx, fd, []byte(identity.banner())); err != nil {
			return err
		}
		if err := writeTTY(attachCtx, fd, []byte(restore.rendition())); err != nil {
			return err
		}
		if session.Welcome.Replay != "initial" {
			if restore.fullscreen() && sizeOwner.Load() {
				var err error
				pending, err = Repaint(attachCtx, session.Send, frames, size, 2*time.Second)
				if err != nil {
					return err
				}
			}
		}

		wg.Go(func() {
			decoder := EscapeDecoder{Disable: options.NoEscape}
			buf := make([]byte, 32*1024)
			var deadline time.Time
			for {
				n, readErr := readTTYUntil(attachCtx, fd, buf, deadline)
				var data []byte
				var detach bool
				if errors.Is(readErr, os.ErrDeadlineExceeded) {
					data = decoder.flushEscape()
					readErr = nil
				} else if n > 0 {
					data, detach = decoder.Feed(buf[:n])
				}
				deadline = time.Time{}
				if len(decoder.escape) > 0 {
					deadline = time.Now().Add(50 * time.Millisecond)
				}
				if len(data) > 0 || detach {
					if len(data) > 0 && !readOnly {
						if err := session.Send(protocol.Frame{Kind: protocol.Input, Payload: data}); err != nil {
							events <- event{err: err}
							return
						}
					}
					if detach {
						// Closing after selecting this event is itself a detach. Sending
						// a frame first could race the server's EOF against this result.
						events <- event{result: AttachResult{Detached: true}}
						return
					}
				}
				if readErr != nil {
					events <- event{err: readErr}
					return
				}
			}
		})
		wg.Go(func() {
			for {
				var next Incoming
				if len(pending) > 0 {
					next, pending = pending[0], pending[1:]
				} else {
					select {
					case <-attachCtx.Done():
						return
					case value, ok := <-frames:
						if !ok {
							return
						}
						next = value
					}
				}
				if next.Err != nil {
					events <- event{err: fmt.Errorf("connection ended; check task status: %w", next.Err)}
					return
				}
				frame := next.Frame
				switch frame.Kind {
				case protocol.Output:
					if err := writeTTY(attachCtx, fd, frame.Payload); err != nil {
						events <- event{err: err}
						return
					}
				case protocol.Exit:
					var exit protocol.ExitMessage
					if err := ipc.Decode(frame.Payload, &exit); err != nil {
						events <- event{err: err}
						return
					}
					events <- event{result: AttachResult{ExitCode: exit.Code}}
					return
				case protocol.Error:
					err := ipc.FrameError(frame)
					var remote *ipc.RemoteError
					if errors.As(err, &remote) && remote.Code == "taken_over" {
						err = errors.New(locale.Text("taken_over"))
					}
					events <- event{err: err}
					return
				case protocol.Notice:
					var notice protocol.NoticeMessage
					if err := ipc.Decode(frame.Payload, &notice); err != nil {
						events <- event{err: err}
						return
					}
					switch notice.Type {
					case "role":
						wasOwner := sizeOwner.Swap(notice.SizeOwner)
						if notice.SizeOwner && !wasOwner && !readOnly {
							if err := session.Send(ipc.JSON(protocol.Resize, CurrentSize(tty))); err != nil {
								events <- event{err: err}
								return
							}
						}
					case "refresh":
						if !readOnly && sizeOwner.Load() && restore.inspect(repository) == nil && restore.fullscreen() {
							redraw, err := Repaint(attachCtx, session.Send, frames, CurrentSize(tty), 2*time.Second)
							if err != nil {
								events <- event{err: err}
								return
							}
							pending = append(pending, redraw...)
						}
					default:
						events <- event{err: errors.New("unexpected broker notice")}
						return
					}
				case protocol.Pong:
				default:
					events <- event{err: errors.New("unexpected broker frame")}
					return
				}
			}
		})
		wg.Go(func() {
			// Also cover a resize during log inspection or the repaint handshake.
			if sizeOwner.Load() && !readOnly {
				_ = session.Send(ipc.JSON(protocol.Resize, CurrentSize(tty)))
			}
			for {
				select {
				case <-attachCtx.Done():
					return
				case <-resize:
					if sizeOwner.Load() && !readOnly {
						_ = session.Send(ipc.JSON(protocol.Resize, CurrentSize(tty)))
					}
				}
			}
		})
		var failure error
		select {
		case e := <-events:
			result = e.result
			failure = e.err
		case <-ctx.Done():
			failure = ctx.Err()
		}
		return failure
	})
	// termios restoration must precede this ANSI cleanup. TUI will start only after
	// all readers exit. These cover common app modes without parsing live output.
	cleanupErr := ResetModes(output)
	if titleActive {
		_, titleErr := io.WriteString(output, restoreWindowTitle)
		cleanupErr = errors.Join(cleanupErr, titleErr)
	}
	return result, errors.Join(err, cleanupErr)
}

func ResetModes(output io.Writer) error {
	_, err := io.WriteString(output, "\x1b[0m\x1b[?9l\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1005l\x1b[?1006l\x1b[?1015l\x1b[?1016l\x1b[?1004l\x1b[?2004l\x1b[?1l\x1b>\x1b[4l\x1b[<u\x1b[>4;0m\x1b[?1049l\x1b[0 q\x1b[?25h")
	return err
}

// Bounded native waits allow cancellation without closing the descriptor
// before termios has been restored. macOS /dev/tty requires select, not poll.
func readTTY(ctx context.Context, fd int, p []byte) (int, error) {
	return readTTYUntil(ctx, fd, p, time.Time{})
}

func readTTYUntil(ctx context.Context, fd int, p []byte, deadline time.Time) (int, error) {
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		wait := 100
		if !deadline.IsZero() {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return 0, os.ErrDeadlineExceeded
			}
			wait = min(wait, max(1, int((remaining+time.Millisecond-1)/time.Millisecond)))
		}
		ready, err := waitTTY(fd, unix.POLLIN, wait)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if !ready {
			continue
		}
		count, err := unix.Read(fd, p)
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
			continue
		}
		if count == 0 && err == nil {
			return 0, io.EOF
		}
		return count, err
	}
}
func writeTTY(ctx context.Context, fd int, p []byte) error {
	deadline := time.Now().Add(2 * time.Second)
	for len(p) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return os.ErrDeadlineExceeded
		}
		ready, err := waitTTY(fd, unix.POLLOUT, 100)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if !ready {
			continue
		}
		count, err := unix.Write(fd, p)
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if count <= 0 {
			return io.ErrShortWrite
		}
		p = p[count:]
	}
	return nil
}
