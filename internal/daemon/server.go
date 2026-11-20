// Package daemon runs one detached PTY broker per task.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/raywcm/taskdock/internal/broker"
	"github.com/raywcm/taskdock/internal/ipc"
	"github.com/raywcm/taskdock/internal/protocol"
	"github.com/raywcm/taskdock/internal/ptyprocess"
	"github.com/raywcm/taskdock/internal/store"
	"github.com/raywcm/taskdock/internal/task"
)

// stopGrace is how long a task may take to exit after SIGTERM before it is
// killed. Programs that flush state on SIGTERM need more than the previous
// one-second window; TDOCK_STOP_GRACE overrides it (e.g. "30s").
var stopGrace = func() time.Duration {
	if v := os.Getenv("TDOCK_STOP_GRACE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return task.DefaultStopGrace
}()

type Ready struct {
	Runtime *task.Runtime `json:"runtime,omitempty"`
	Error   string        `json:"error,omitempty"`
}
type server struct {
	store    *store.JSON
	process  broker.Process
	runtime  task.Runtime
	mu       sync.Mutex
	hub      broker.Hub
	stop     chan struct{}
	stopOnce sync.Once
	finished chan struct{}
	clients  sync.WaitGroup
	connsMu  sync.Mutex
	conns    map[*net.UnixConn]bool
	slots    chan struct{}
	attachMu sync.Mutex // Serializes lease changes with PTY input and resizing.
	size     task.Size
}

func Run(ctx context.Context, state string, id task.ID, ready io.Writer) (result error) {
	announced := false
	announce := func(r Ready) {
		if !announced {
			_ = json.NewEncoder(ready).Encode(r)
			announced = true
			if c, ok := ready.(io.Closer); ok {
				_ = c.Close()
			}
		}
	}
	defer func() {
		if !announced {
			message := "broker failed to start"
			if result != nil {
				message = result.Error()
			}
			announce(Ready{Error: message})
		}
	}()
	repository, err := store.Open(state)
	if err != nil {
		return err
	}
	state = repository.Root()
	release, err := repository.BrokerLock(id)
	if err != nil {
		return err
	}
	defer release()
	record, err := repository.Load(ctx, id)
	if err != nil {
		return err
	}
	if record.Runtime != nil {
		return errors.New("task has already been launched; create a new task to restart")
	}
	instance, err := task.NewID()
	if err != nil {
		return err
	}
	s := &server{store: repository, runtime: task.Runtime{SchemaVersion: 1, TaskID: id, InstanceID: instance, State: task.Starting, BrokerPID: os.Getpid(), UpdatedAt: time.Now().UTC(), StopGrace: stopGrace}, stop: make(chan struct{}), finished: make(chan struct{}), conns: make(map[*net.UnixConn]bool), slots: make(chan struct{}, 32)}
	if err := s.save(task.Starting); err != nil {
		return err
	}
	fail := func(err error) error {
		s.mu.Lock()
		s.runtime.Error = err.Error()
		s.mu.Unlock()
		_ = s.save(task.Failed)
		return err
	}
	socket, err := ipc.SocketPath(state, id)
	if err != nil {
		return fail(err)
	}
	// The exclusive per-task lock proves no live broker owns this task socket.
	if info, err := os.Lstat(socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fail(errors.New("refusing to remove non-socket runtime path"))
		}
		if err := os.Remove(socket); err != nil {
			return fail(err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	}
	oldMask := syscall.Umask(0077)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	syscall.Umask(oldMask)
	if err != nil {
		return fail(err)
	}
	defer listener.Close()
	if err := os.Chmod(socket, 0600); err != nil {
		return fail(err)
	}
	size := record.Spec.Size
	if size.Validate() != nil {
		size = task.Size{Rows: 24, Cols: 80}
	}
	process, err := (ptyprocess.Factory{}).Start(broker.SpawnOptions{Spec: record.Spec, Size: size, Env: os.Environ()})
	if err != nil {
		return fail(err)
	}
	s.process = process
	s.size = size
	if record.Spec.CaptureInitial {
		s.hub.CaptureInitial()
	}
	defer process.Close()
	s.runtime.ChildPID = process.PID()
	started := time.Now().UTC()
	s.runtime.StartedAt = &started
	// Every started child is waited, even if persistence or readiness fails.
	waited := make(chan struct{})
	var exit broker.ExitStatus
	var waitErr error
	go func() { exit, waitErr = process.Wait(); close(waited) }()
	cleanup := func() { _ = ptyprocess.SignalSession(process.PID(), syscall.SIGKILL); _ = process.Close(); <-waited }
	if err := s.save(task.Running); err != nil {
		cleanup()
		return fail(err)
	}
	logPath, _ := repository.Path(id, "output.log")
	output, err := newOutputLog(logPath)
	if err != nil {
		cleanup()
		return fail(err)
	}
	defer output.Close()
	drained := make(chan error, 1)
	go func() {
		buf := make([]byte, broker.OutputChunkSize)
		logging := true
		for {
			n, readErr := process.Read(buf)
			if n > 0 {
				if logging {
					if _, err := output.Write(buf[:n]); err != nil {
						fmt.Fprintln(os.Stderr, "output log:", err)
						logging = false
					}
				}
				s.hub.Publish(buf[:n])
			}
			if readErr != nil {
				if errors.Is(readErr, io.EOF) {
					readErr = nil
				}
				drained <- readErr
				return
			}
		}
	}()
	acceptDone := make(chan struct{})
	go func() { defer close(acceptDone); s.accept(listener) }()
	announce(Ready{Runtime: ptr(s.snapshot())})

	stopping := false
	stopRequest := s.stop
	contextDone := ctx.Done()
	var force <-chan time.Time
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
loop:
	for {
		select {
		case <-waited:
			break loop
		case <-stopRequest:
			stopRequest = nil
			if !stopping {
				stopping = true
				_ = s.save(task.Stopping)
				_ = ptyprocess.SignalSession(process.PID(), syscall.SIGTERM)
				timer = time.NewTimer(s.runtime.StopGrace)
				force = timer.C
			}
		case <-contextDone:
			contextDone = nil
			s.requestStop()
		case <-force:
			_ = ptyprocess.SignalSession(process.PID(), syscall.SIGKILL)
			force = nil
		}
	}
	// A shell may exit while a child still holds the PTY. During an explicit
	// stop, let that child use the remaining grace period without a second TERM.
	if !stopping {
		_ = ptyprocess.SignalSession(process.PID(), syscall.SIGTERM)
	}
	if force == nil {
		cleanupTimer := time.NewTimer(250 * time.Millisecond)
		defer cleanupTimer.Stop()
		force = cleanupTimer.C
	}
	var drainErr error
	select {
	case drainErr = <-drained:
	case <-force:
		_ = ptyprocess.SignalSession(process.PID(), syscall.SIGKILL)
		_ = process.Close()
		drainErr = <-drained
	}
	_ = ptyprocess.SignalSession(process.PID(), syscall.SIGKILL)
	s.mu.Lock()
	s.runtime.ExitCode = &exit.Code
	finished := time.Now().UTC()
	s.runtime.FinishedAt = &finished
	s.runtime.Signal = exit.Signal
	if waitErr != nil {
		s.runtime.Error = waitErr.Error()
	} else if drainErr != nil && !errors.Is(drainErr, os.ErrClosed) {
		s.runtime.Error = drainErr.Error()
	}
	s.mu.Unlock()
	final := task.Exited
	if waitErr != nil {
		final = task.Failed
	}
	persistErr := s.save(final)
	close(s.finished)
	s.hub.Close()
	_ = listener.Close()
	<-acceptDone
	// Give fast clients time to receive queued output + Exit, then close all peers.
	clientsDone := make(chan struct{})
	go func() { s.clients.Wait(); close(clientsDone) }()
	select {
	case <-clientsDone:
	case <-time.After(300 * time.Millisecond):
		s.connsMu.Lock()
		for conn := range s.conns {
			conn.Close()
		}
		s.connsMu.Unlock()
		<-clientsDone
	}
	return persistErr
}
func ptr(r task.Runtime) *task.Runtime { return &r }
func (s *server) snapshot() task.Runtime {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runtime
	if r.ExitCode != nil {
		code := *r.ExitCode
		r.ExitCode = &code
	}
	return r
}
func (s *server) save(state task.State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runtime.State = state
	s.runtime.UpdatedAt = time.Now().UTC()
	return s.store.SaveRuntime(context.Background(), s.runtime)
}
func (s *server) requestStop() { s.stopOnce.Do(func() { close(s.stop) }) }
func (s *server) accept(listener *net.UnixListener) {
	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			return
		}
		select {
		case s.slots <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		s.clients.Add(1)
		s.connsMu.Lock()
		s.conns[conn] = true
		s.connsMu.Unlock()
		go func() {
			defer s.clients.Done()
			defer func() { <-s.slots; s.connsMu.Lock(); delete(s.conns, conn); s.connsMu.Unlock(); conn.Close() }()
			s.serve(conn)
		}()
	}
}
func (s *server) detach(sub *broker.Subscription) {
	s.attachMu.Lock()
	defer s.attachMu.Unlock()
	s.hub.Detach(sub)
}
func (s *server) serve(conn *net.UnixConn) {
	if err := ipc.Authenticate(conn); err != nil {
		return
	}
	wire := &ipc.Session{Conn: conn, Encoder: protocol.NewEncoder(conn)}
	reject := func(code string, err error) {
		_ = wire.Send(ipc.JSON(protocol.Error, protocol.ErrorMessage{Code: code, Message: err.Error()}))
	}
	_ = conn.SetReadDeadline(time.Now().Add(ipc.Timeout))
	frame, err := protocol.Read(conn)
	if err != nil {
		return
	}
	var hello protocol.HelloMessage
	if frame.Kind != protocol.Hello || ipc.Decode(frame.Payload, &hello) != nil {
		reject("invalid_request", errors.New("expected valid hello"))
		return
	}
	current := s.snapshot()
	if hello.TaskID != current.TaskID || hello.InstanceID != current.InstanceID {
		reject("identity_mismatch", errors.New("task instance changed"))
		return
	}
	if hello.Mode != "attach" && hello.Mode != "control" {
		reject("invalid_request", errors.New("unknown connection mode"))
		return
	}
	mode := hello.AttachMode
	if mode == "" {
		mode = protocol.AttachExclusive
	}
	if !protocol.ValidAttachMode(mode) || (hello.Mode == "control" && hello.AttachMode != "") {
		reject("invalid_request", errors.New("invalid attachment mode"))
		return
	}
	modern := slices.Contains(hello.Capabilities, protocol.AttachModesCapability)
	if mode != protocol.AttachExclusive && !modern {
		reject("invalid_request", errors.New("connection modes require capability negotiation"))
		return
	}
	var sub *broker.Subscription
	s.attachMu.Lock()
	if hello.Mode == "attach" {
		if hello.Size == nil || hello.Size.Validate() != nil {
			s.attachMu.Unlock()
			reject("invalid_request", errors.New("attach requires terminal size"))
			return
		}
		sub, err = s.hub.AttachMode(mode, modern)
		if err != nil {
			s.attachMu.Unlock()
			reject("busy", err)
			return
		}
		defer s.detach(sub)
		if s.hub.OwnsSize(sub) && *hello.Size != s.size {
			if err = s.process.Resize(*hello.Size); err != nil {
				s.attachMu.Unlock()
				reject("internal", err)
				return
			}
			s.size = *hello.Size
		}
	}
	replay := "none"
	if sub != nil && sub.Initial {
		replay = "initial"
	}
	size := s.size
	welcome := protocol.WelcomeMessage{
		TaskID: current.TaskID, InstanceID: current.InstanceID, Replay: replay, Size: &size,
		Capabilities: []string{protocol.AttachModesCapability, protocol.OutputByteQueueCapability},
	}
	if sub != nil {
		welcome.AttachMode = mode
		welcome.SizeOwner = s.hub.OwnsSize(sub)
	}
	err = wire.Send(ipc.JSON(protocol.Welcome, welcome))
	s.attachMu.Unlock()
	if err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	outputDone := make(chan struct{})
	if sub != nil {
		go func() {
			defer close(outputDone)
			defer conn.Close()
			finish := func() {
				select {
				case <-s.finished:
					// Preserve queued final bytes before Exit.
					for data := sub.TakeOutput(); len(data) > 0; data = sub.TakeOutput() {
						if wire.Send(protocol.Frame{Kind: protocol.Output, Payload: data}) != nil {
							return
						}
					}
					r := s.snapshot()
					code := 1
					if r.ExitCode != nil {
						code = *r.ExitCode
					}
					_ = wire.Send(ipc.JSON(protocol.Exit, protocol.ExitMessage{Code: code, Signal: r.Signal}))
				default:
					reason := sub.Reason()
					if reason.Code != "" {
						_ = wire.Send(ipc.JSON(protocol.Error, reason))
					}
				}
			}
			for {
				select {
				case <-sub.Done:
					finish()
					return
				default:
				}
				select {
				case <-sub.Done:
					finish()
					return
				case <-sub.OutputReady:
					data := sub.TakeOutput()
					if len(data) > 0 && wire.Send(protocol.Frame{Kind: protocol.Output, Payload: data}) != nil {
						return
					}
				case notice := <-sub.Notices:
					s.attachMu.Lock()
					size := s.size
					s.attachMu.Unlock()
					notice.Size = &size
					if wire.Send(ipc.JSON(protocol.Notice, notice)) != nil {
						return
					}
				}
			}
		}()
		defer func() { s.detach(sub); conn.Close(); <-outputDone }()
		if !hello.Preview && !s.hub.RequestRefresh(sub) {
			if redraw, ok := s.process.(interface{ Redraw() error }); ok {
				_ = redraw.Redraw()
			}
		}
	} else {
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	}
	denied := func(code, message string) {
		// A resize/input can arrive just after takeover revoked this lease.
		// Let its output worker send the revocation before closing the socket.
		select {
		case <-sub.Done:
			<-outputDone
		default:
			reject(code, errors.New(message))
		}
	}
	for {
		frame, err := protocol.Read(conn)
		if err != nil {
			return
		}
		switch frame.Kind {
		case protocol.Input:
			if sub == nil {
				reject("invalid_request", errors.New("control connection cannot send input"))
				return
			}
			s.attachMu.Lock()
			allowed := s.hub.CanInput(sub)
			if allowed {
				for p := frame.Payload; len(p) > 0; {
					n, writeErr := s.process.Write(p)
					if writeErr != nil || n == 0 {
						err = errors.Join(writeErr, io.ErrShortWrite)
						break
					}
					p = p[n:]
				}
			}
			s.attachMu.Unlock()
			if !allowed {
				denied("read_only", "connection has no input control")
				return
			}
			if err != nil {
				return
			}
		case protocol.Resize:
			var size task.Size
			if sub == nil || ipc.Decode(frame.Payload, &size) != nil || size.Validate() != nil {
				reject("invalid_request", errors.New("invalid resize"))
				return
			}
			s.attachMu.Lock()
			allowed := s.hub.CanInput(sub)
			if allowed && s.hub.OwnsSize(sub) && size != s.size {
				err = s.process.Resize(size)
				if err == nil {
					s.size = size
					s.hub.NotifySize()
				}
			}
			s.attachMu.Unlock()
			if !allowed {
				denied("read_only", "read-only connections cannot resize")
				return
			}
			if err != nil {
				return
			}
		case protocol.Detach:
			if len(frame.Payload) != 0 {
				reject("invalid_request", errors.New("detach must be empty"))
			}
			return
		case protocol.Ping, protocol.Stop:
			if len(frame.Payload) != 0 {
				reject("invalid_request", errors.New("control request must be empty"))
				return
			}
			if frame.Kind == protocol.Stop {
				s.attachMu.Lock()
				allowed := sub == nil || s.hub.CanInput(sub)
				if allowed {
					s.requestStop()
				}
				s.attachMu.Unlock()
				if !allowed {
					denied("read_only", "read-only connections cannot stop the task")
					return
				}
			}
			if wire.Send(ipc.JSON(protocol.Pong, s.snapshot())) != nil {
				return
			}
		default:
			reject("invalid_request", errors.New("unexpected client frame"))
			return
		}
	}
}
