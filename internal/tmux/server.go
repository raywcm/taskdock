// Package tmux reads sessions and active panes from an existing tmux server.
package tmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type Server struct{ Label, Socket string }
type Session struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (s Server) command(ctx context.Context, args ...string) *exec.Cmd {
	var flags []string
	if s.Socket != "" {
		flags = append(flags, "-S", s.Socket)
	} else if s.Label != "" {
		flags = append(flags, "-L", s.Label)
	}
	return exec.CommandContext(ctx, "tmux", append(flags, args...)...)
}

func (s Server) output(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := s.command(ctx, args...).Output()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if len(args) > 0 && args[0] == "list-sessions" && s.Label == "" && s.Socket == "" {
			detail := bytes.TrimSpace(exit.Stderr)
			noSocket := bytes.HasPrefix(detail, []byte("error connecting to ")) && bytes.HasSuffix(detail, []byte("(No such file or directory)"))
			if noSocket || bytes.HasPrefix(detail, []byte("no server running on ")) || bytes.Equal(detail, []byte("no sessions")) {
				return "", nil
			}
		}
		return "", fmt.Errorf("tmux: %s", strings.TrimSpace(string(exit.Stderr)))
	}
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func (s Server) List(ctx context.Context) ([]Session, error) {
	text, err := s.output(ctx, "list-sessions", "-F", "#{session_id} #{session_name}")
	if err != nil {
		return nil, err
	}
	sessions := []Session{}
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		if line == "" {
			continue
		}
		id, name, ok := strings.Cut(line, " ")
		digits, hasPrefix := strings.CutPrefix(id, "$")
		_, parseErr := strconv.ParseUint(digits, 10, 64)
		if !ok || !hasPrefix || parseErr != nil {
			return nil, fmt.Errorf("unexpected tmux list-sessions output")
		}
		sessions = append(sessions, Session{ID: id, Name: name})
	}
	return sessions, nil
}

func (s Server) Resolve(ctx context.Context, target string) (Session, error) {
	sessions, err := s.List(ctx)
	if err != nil {
		return Session{}, err
	}
	for _, v := range sessions {
		if v.ID == target {
			return v, nil
		}
	}
	for _, v := range sessions {
		if v.Name == target {
			return v, nil
		}
	}
	return Session{}, fmt.Errorf("tmux session %q not found", target)
}

// Capture the visible contents of the session's active pane, as plain text.
func (s Server) Capture(ctx context.Context, session Session) (string, error) {
	text, err := s.output(ctx, "capture-pane", "-p", "-t", session.ID+":")
	if err != nil {
		return "", err
	}
	return strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return -1
		}
		return r
	}, text), nil
}

func (s Server) Viewer(ctx context.Context, session Session) *exec.Cmd {
	// if-shell connects only to an existing server, including on tmux 3.0a
	// which lacks the global -N option. -F evaluates a constant without
	// invoking a shell. List validates the numeric session ID.
	return s.command(ctx, "if-shell", "-F", "1", "attach-session -r -E -t '"+session.ID+"'")
}
