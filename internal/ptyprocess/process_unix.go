//go:build linux || darwin

// Package ptyprocess is the only adapter that starts commands and owns a PTY.
package ptyprocess

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"github.com/raywcm/taskdock/internal/broker"
	"github.com/raywcm/taskdock/internal/task"
)

type Factory struct{}

var _ broker.Spawner = Factory{}

type Process struct {
	cmd    *exec.Cmd
	master *os.File
}

func (Factory) Start(opts broker.SpawnOptions) (broker.Process, error) {
	if err := opts.Spec.Validate(); err != nil {
		return nil, err
	}
	if err := opts.Size.Validate(); err != nil {
		return nil, err
	}
	if opts.Env == nil {
		return nil, errors.New("launch environment must be explicit")
	}
	// Deliberately not CommandContext: the command lifetime must not follow a client.
	cmd := exec.Command(opts.Spec.Command[0], opts.Spec.Command[1:]...)
	cmd.Dir = opts.Spec.Dir
	cmd.Env = sessionEnvironment(opts.Env, opts.Spec.ID)
	// StartWithSize creates a new session and controlling terminal for the child.
	master, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: opts.Size.Rows, Cols: opts.Size.Cols})
	if err != nil {
		return nil, err
	}
	// The macOS PTY library constructs os.File before setting O_NONBLOCK.
	// Re-wrap a duplicated, already nonblocking FD to register with Go's poller.
	// F_DUPFD_CLOEXEC avoids leaking this descriptor through concurrent execs.
	pollMaster, err := pollableMaster(master)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = master.Close()
		_ = cmd.Wait()
		return nil, err
	}
	_ = master.Close()
	return &Process{cmd: cmd, master: pollMaster}, nil
}

func sessionEnvironment(env []string, id task.ID) []string {
	result := make([]string, 0, len(env)+2)
	var terminal string
	colorConfigured := false
	insideMux := false
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		switch key {
		case "TDOCK_SESSION":
			continue
		case "TMUX", "TMUX_PANE":
			if value == "" {
				continue
			}
			insideMux = true
		case "TERM":
			terminal = value
		case "COLORTERM", "NO_COLOR", "FORCE_COLOR", "CLICOLOR", "CLICOLOR_FORCE":
			colorConfigured = true
		}
		result = append(result, entry)
	}
	// SSH commonly forwards TERM alone. Advertise the RGB support of the
	// TaskDock viewport to apps that use xterm and check COLORTERM.
	if terminal == "xterm-256color" && !colorConfigured {
		result = append(result, "COLORTERM=truecolor")
	}
	// Codex accepts a pane marker independently of a tmux server. Use a
	// namespaced, non-numeric target so a probe cannot select a real pane.
	if !insideMux {
		result = append(result, "TMUX_PANE=%tdock-"+string(id))
	}
	// The new session ID follows shells and subprocesses across state roots.
	return append(result, "TDOCK_SESSION="+string(id))
}

func pollableMaster(master *os.File) (*os.File, error) {
	fd, err := unix.FcntlInt(master.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if err := syscall.SetNonblock(fd, true); err != nil {
		_ = syscall.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), master.Name()), nil
}

func (p *Process) PID() int { return p.cmd.Process.Pid }

func (p *Process) Read(buf []byte) (int, error) {
	n, err := p.master.Read(buf)
	// Linux PTY masters return EIO after the last slave closes; macOS returns EOF.
	if runtime.GOOS == "linux" && errors.Is(err, syscall.EIO) {
		err = io.EOF
	}
	return n, err
}

func (p *Process) Write(buf []byte) (int, error) {
	if err := p.master.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return 0, err
	}
	return p.master.Write(buf)
}
func (p *Process) Close() error { return p.master.Close() }

func (p *Process) Resize(size task.Size) error {
	if err := size.Validate(); err != nil {
		return err
	}
	return pty.Setsize(p.master, &pty.Winsize{Rows: size.Rows, Cols: size.Cols})
}

func (p *Process) Wait() (broker.ExitStatus, error) {
	err := p.cmd.Wait()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return broker.ExitStatus{}, err
	}
	status := broker.ExitStatus{Code: p.cmd.ProcessState.ExitCode()}
	if ws, ok := p.cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		status.Code = 128 + int(ws.Signal())
		status.Signal = ws.Signal().String()
	}
	return status, nil // A non-zero child exit is a result, not a broker failure.
}
