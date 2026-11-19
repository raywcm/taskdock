//go:build linux || darwin

package ptyprocess

import (
	"bytes"
	"errors"
	"io"
	"os"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/raywcm/taskdock/internal/broker"
	"github.com/raywcm/taskdock/internal/task"
)

func TestSessionEnvironmentUsesTrueColorWithSSHXterm(t *testing.T) {
	const id = task.ID("new")
	cases := []struct {
		name string
		env  []string
		want []string
	}{
		{"SSH xterm", []string{"TERM=xterm-256color", "TDOCK_SESSION=old"}, []string{"TERM=xterm-256color", "COLORTERM=truecolor"}},
		{"explicit color", []string{"TERM=xterm-256color", "COLORTERM=24bit"}, []string{"TERM=xterm-256color", "COLORTERM=24bit"}},
		{"no color", []string{"TERM=xterm-256color", "NO_COLOR=1"}, []string{"TERM=xterm-256color", "NO_COLOR=1"}},
		{"forced profile", []string{"TERM=xterm-256color", "FORCE_COLOR=0"}, []string{"TERM=xterm-256color", "FORCE_COLOR=0"}},
		{"dumb terminal", []string{"TERM=dumb"}, []string{"TERM=dumb"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			want := append(slices.Clone(c.want), "TDOCK_SESSION=new")
			if got := sessionEnvironment(c.env, id); !slices.Equal(got, want) {
				t.Fatalf("environment %q, want %q", got, want)
			}
		})
	}
}

// A real PTY test: arguments with shell syntax must remain literal arguments,
// the child sees a tty and its dimensions, and non-zero exit is preserved.
func TestPTYArgumentsDimensionsOutputAndExit(t *testing.T) {
	id, _ := task.NewID()
	literal := "$(printf INJECTED); 中文"
	spec := task.Spec{
		SchemaVersion: 1, ID: id, Name: "pty-test", Dir: t.TempDir(), CreatedAt: time.Now().UTC(),
		Command: []string{"/bin/sh", "-c", `test -t 0 && test -t 1 || exit 99; stty size; printf '%s' "$1"; exit 7`, "test", literal},
	}
	process, err := (Factory{}).Start(broker.SpawnOptions{Spec: spec, Size: task.Size{Rows: 31, Cols: 91}, Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	defer process.Close()
	timer := time.AfterFunc(5*time.Second, func() { _ = syscall.Kill(process.PID(), syscall.SIGKILL); _ = process.Close() })
	defer timer.Stop()
	output, readErr := io.ReadAll(process)
	status, waitErr := process.Wait()
	if readErr != nil || waitErr != nil {
		t.Fatalf("read=%v wait=%v", readErr, waitErr)
	}
	if !bytes.Contains(output, []byte("31 91")) || !bytes.Contains(output, []byte(literal)) {
		t.Fatalf("unexpected PTY output: %q", output)
	}
	if status.Code != 7 {
		t.Fatalf("exit code: %+v", status)
	}
}

func TestPTYReadRemainsCancellableAfterResize(t *testing.T) {
	id, _ := task.NewID()
	spec := task.Spec{SchemaVersion: 1, ID: id, Name: "poll-test", Dir: t.TempDir(), CreatedAt: time.Now().UTC(), Command: []string{"/bin/sh", "-c", "read value"}}
	process, err := (Factory{}).Start(broker.SpawnOptions{Spec: spec, Size: task.Size{Rows: 24, Cols: 80}, Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = syscall.Kill(process.PID(), syscall.SIGKILL)
		_ = process.Close()
		_, _ = process.Wait()
	}()
	if err := process.Resize(task.Size{Rows: 40, Cols: 120}); err != nil {
		t.Fatal(err)
	}
	p := process.(*Process)
	if err := p.master.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if _, err := process.Read(b[:]); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("poller deadline not honored: %v", err)
	}
}
