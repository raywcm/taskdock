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
			opened, err := file.Stat()
			if err != nil {
				return err
			}
			next, err := nextLogGeneration(path, opened)
			if err != nil {
				return err
			}
			if next != nil {
				// The producer closes the old file before renaming it. Drain
				// anything appended since EOF before switching descriptors.
				if err := drain(); err != nil {
					next.Close()
					return err
				}
				file.Close()
				file = next
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

// nextLogGeneration returns the oldest retained successor of opened. A burst
// can rotate twice between polls, leaving unread output in output.log.1.
func nextLogGeneration(path string, opened os.FileInfo) (*os.File, error) {
	currentInfo, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if os.SameFile(opened, currentInfo) {
		return nil, nil
	}

	current, snapshot, err := openLogSnapshot(path)
	if err != nil || current == nil {
		return nil, err
	}
	older, previous, err := openLogSnapshot(path + ".1")
	if err != nil {
		current.Close()
		return nil, err
	}
	if older == nil {
		return current, nil
	}
	if !os.SameFile(opened, previous) && !os.SameFile(snapshot, previous) {
		// Only read .1 first if it still precedes the current descriptor we
		// opened. If the pair rotated again, consume our pinned descriptor
		// first and pick up the new generations on the next pass.
		now, err := os.Lstat(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			older.Close()
			current.Close()
			return nil, err
		}
		if os.SameFile(snapshot, now) {
			current.Close()
			return older, nil
		}
	}
	older.Close()
	return current, nil
}
