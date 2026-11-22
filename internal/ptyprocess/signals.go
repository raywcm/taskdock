package ptyprocess

import (
	"errors"
	"golang.org/x/sys/unix"
	"syscall"
)

// SignalSession covers shell job-control groups as well as the session leader.
// Escaped/daemonized processes are outside the managed terminal session.
func SignalSession(session int, sig syscall.Signal) error {
	pids, err := sessionCandidates()
	if err != nil {
		return err
	}
	var result error
	for _, pid := range pids {
		sid, err := unix.Getsid(pid)
		if err != nil || sid != session {
			continue
		}
		if err := unix.Kill(pid, sig); err != nil && !errors.Is(err, unix.ESRCH) {
			result = errors.Join(result, err)
		}
	}
	return result
}
func (p *Process) Redraw() error {
	group, err := unix.IoctlGetInt(int(p.master.Fd()), unix.TIOCGPGRP)
	if err != nil {
		return err
	}
	if group <= 0 {
		return nil
	}
	return unix.Kill(-group, unix.SIGWINCH)
}

// ForegroundPID prevents automation from typing into a shell after its program exits.
func (p *Process) ForegroundPID(pid int) bool {
	if pid <= 0 {
		return false
	}
	session, err := unix.Getsid(pid)
	if err != nil || session != p.cmd.Process.Pid {
		return false
	}
	group, err := unix.Getpgid(pid)
	if err != nil {
		return false
	}
	foreground, err := unix.IoctlGetInt(int(p.master.Fd()), unix.TIOCGPGRP)
	return err == nil && foreground == group
}
