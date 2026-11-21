package service

import (
	"context"
	"errors"
	"io"
	"os"
	"time"
	"unicode/utf8"

	"github.com/raywcm/taskdock/internal/task"
)

// logFilter retains ANSI parsing state only for safe log display, never for the
// live terminal stream. It handles sequences split across follow reads.
type logFilter struct{ state byte }

func (f *logFilter) clean(p []byte) []byte {
	out := make([]byte, 0, len(p))
	for _, b := range p {
		switch f.state {
		case 0:
			if b == 27 {
				f.state = 1
			} else if b >= 32 || b == '\n' || b == '\r' || b == '\t' {
				out = append(out, b)
			}
		case 1:
			switch b {
			case '[':
				f.state = 2
			case ']':
				f.state = 3
			case 'P', 'X', '^', '_':
				f.state = 4
			default:
				if b >= 0x20 && b <= 0x2f {
					f.state = 6
				} else {
					f.state = 0
				}
			}
		case 2:
			if b >= 0x40 && b <= 0x7e {
				f.state = 0
			}
		case 3:
			if b == 7 {
				f.state = 0
			} else if b == 27 {
				f.state = 5
			}
		case 4:
			if b == 27 {
				f.state = 5
			}
		case 5:
			if b == '\\' {
				f.state = 0
			} else if b != 27 {
				f.state = 4
			}
		case 6:
			if b >= 0x30 && b <= 0x7e {
				f.state = 0
			}
		}
	}
	return out
}

func (m *Manager) FollowLogs(ctx context.Context, ref string, raw bool, tail int, w io.Writer) error {
	if tail < 0 {
		return errors.New("tail must be non-negative")
	}
	r, err := m.Resolve(ctx, ref)
	if err != nil {
		return err
	}
	path, err := m.Store.Path(r.Spec.ID, "output.log")
	if err != nil {
		return err
	}
	var file *os.File
	defer func() {
		if file != nil {
			file.Close()
		}
	}()
	var filter logFilter
	write := func(data []byte) error {
		if !raw {
			data = filter.clean(data)
		}
		if len(data) == 0 {
			return nil
		}
		n, err := w.Write(data)
		if err == nil && n != len(data) {
			return io.ErrShortWrite
		}
		return err
	}
	buf := make([]byte, 32*1024)
	drain := func() error {
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			n, err := file.Read(buf)
			if e := write(buf[:n]); e != nil {
				return e
			}
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
		}
	}
	first, finished := true, false
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if file == nil {
			var info os.FileInfo
			file, info, err = openLogSnapshot(path)
			if err != nil {
				return err
			}
			if first {
				data, cut, err := readOpenedLogTail(path, int64(tail), file, info)
				if errors.Is(err, errLogRotated) {
					file.Close()
					file = nil
					continue
				}
				if err != nil {
					return err
				}
				if !raw && cut {
					for len(data) > 0 && !utf8.RuneStart(data[0]) {
						data = data[1:]
					}
				}
				if err := write(data); err != nil {
					return err
				}
				if file != nil {
					if _, err := file.Seek(info.Size(), io.SeekStart); err != nil {
						return err
					}
				}
				first = file == nil && len(data) == 0
			}
		}
		if file != nil {
			if err := drain(); err != nil {
				return err
			}
			current, err := os.Lstat(path)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			opened, err := file.Stat()
			if err != nil {
				return err
			}
			if current != nil && !os.SameFile(current, opened) {
				// The producer closes the old file before renaming it. Pick
				// up anything appended between the preceding EOF and rename.
				if err := drain(); err != nil {
					return err
				}
				file.Close()
				file = nil
				continue
			}
		}
		if finished {
			return nil
		}
		latest, err := m.Store.Load(ctx, r.Spec.ID)
		if err != nil {
			return err
		}
		observed := m.Observe(ctx, latest)
		if observed.Runtime != nil && (observed.Runtime.State == task.Exited || observed.Runtime.State == task.Failed) {
			finished = true
			continue
		}
		if observed.Health == "unreachable" {
			return errors.New("broker unreachable; follow stopped")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
