package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"syscall"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

var errLogRotated = errors.New("output logs rotated during read")

func (m *Manager) Logs(ctx context.Context, ref string, raw bool, tail int) ([]byte, error) {
	if tail < 0 {
		return nil, errors.New("tail must be non-negative")
	}
	r, err := m.Resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	path, err := m.Store.Path(r.Spec.ID, "output.log")
	if err != nil {
		return nil, err
	}
	var data []byte
	var cut bool
	for range 3 {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		data, cut, err = readLogSnapshot(path, int64(tail))
		if !errors.Is(err, errLogRotated) {
			break
		}
	}
	if err != nil || raw {
		return data, err
	}
	// A byte tail can begin midway through a UTF-8 rune. Raw logs retain
	// those bytes, while the text view omits the partial character.
	for cut && len(data) > 0 && !utf8.RuneStart(data[0]) {
		data = data[1:]
	}
	data = []byte(ansi.Strip(string(data)))
	clean := data[:0]
	for _, b := range data {
		if b >= 32 || b == '\n' || b == '\r' || b == '\t' {
			clean = append(clean, b)
		}
	}
	return clean, nil
}

func readLogSnapshot(path string, tail int64) ([]byte, bool, error) {
	current, currentInfo, err := openLogSnapshot(path)
	if err != nil {
		return nil, false, err
	}
	if current != nil {
		defer current.Close()
	}
	return readOpenedLogTail(path, tail, current, currentInfo)
}

func readOpenedLogTail(path string, tail int64, current *os.File, currentInfo os.FileInfo) ([]byte, bool, error) {
	var currentSize int64
	if current != nil {
		currentSize = currentInfo.Size()
		if tail > 0 && currentSize > tail {
			data, err := io.ReadAll(io.NewSectionReader(current, currentSize-tail, tail))
			return data, true, err
		}
	}

	older, olderInfo, err := openLogSnapshot(path + ".1")
	if err != nil {
		return nil, false, err
	}
	var output bytes.Buffer
	var cut bool
	if older != nil {
		defer older.Close()
		if current != nil {
			// Rotation between the opens can make .1 refer to the same file
			// (or a newer file after another rotation). Retry that snapshot.
			now, err := os.Lstat(path)
			if errors.Is(err, os.ErrNotExist) {
				return nil, false, errLogRotated
			}
			if err != nil {
				return nil, false, err
			}
			if os.SameFile(olderInfo, currentInfo) || !os.SameFile(now, currentInfo) {
				return nil, false, errLogRotated
			}
		}
		size := olderInfo.Size()
		start := int64(0)
		if tail > 0 {
			start = max(0, size-(tail-currentSize))
		}
		cut = start > 0
		if _, err := io.Copy(&output, io.NewSectionReader(older, start, size-start)); err != nil {
			return nil, false, err
		}
	}
	if current != nil {
		if _, err := io.Copy(&output, io.NewSectionReader(current, 0, currentSize)); err != nil {
			return nil, false, err
		}
	}
	return output.Bytes(), cut, nil
}

func openLogSnapshot(path string) (*os.File, os.FileInfo, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0) {
		err = errors.New("unsafe output log")
	}
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, info, nil
}
