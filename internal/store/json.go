// Package store provides versioned JSON snapshots. File-level atomic replacement
// prevents torn reads; the service serializes catalog mutations with Lock while
// the owning broker holds its own exclusive lock for runtime updates.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"syscall"

	"github.com/raywcm/taskdock/internal/task"
)

type JSON struct{ root string }

func Open(root string) (*JSON, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("state directory must be absolute")
	}
	if err := privateDir(root); err != nil {
		return nil, err
	}
	if err := privateDir(filepath.Join(root, "tasks")); err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	root = canonical
	return &JSON{root: root}, nil
}

func privateDir(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return checkPrivateDir(dir)
}
func checkPrivateDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("directory must be private (0700) and not a symlink: %s", dir)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("directory belongs to another user: %s", dir)
	}
	return nil
}

func (s *JSON) taskDir(id task.ID) (string, error) {
	if err := id.Validate(); err != nil {
		return "", err
	}
	return filepath.Join(s.root, "tasks", string(id)), nil
}

// Create reserves an immutable ID. Name uniqueness belongs to the service
// catalog transaction, not this storage primitive. Incomplete directories without
// spec.json are ignored by List and reserved for explicit recovery/cleanup.
func (s *JSON) Create(ctx context.Context, spec task.Spec) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := spec.Validate(); err != nil {
		return err
	}
	dir, err := s.taskDir(spec.ID)
	if err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	}
	return writeJSON(filepath.Join(dir, "spec.json"), spec)
}

func (s *JSON) Load(ctx context.Context, id task.ID) (task.Record, error) {
	var record task.Record
	if err := ctx.Err(); err != nil {
		return record, err
	}
	dir, err := s.taskDir(id)
	if err != nil {
		return record, err
	}
	if err := checkPrivateDir(dir); err != nil {
		return record, err
	}
	if err := readJSON(filepath.Join(dir, "spec.json"), &record.Spec); err != nil {
		return record, err
	}
	if err := record.Spec.Validate(); err != nil {
		return record, err
	}
	if record.Spec.ID != id {
		return record, errors.New("spec ID does not match its directory")
	}
	var runtime task.Runtime
	if err := readJSON(filepath.Join(dir, "runtime.json"), &runtime); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return record, nil
		}
		return record, err
	}
	if err := runtime.Validate(); err != nil {
		return record, err
	}
	if runtime.TaskID != id {
		return record, errors.New("runtime task ID mismatch")
	}
	record.Runtime = &runtime
	return record, nil
}

func (s *JSON) List(ctx context.Context) ([]task.Record, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "tasks"))
	if err != nil {
		return nil, err
	}
	records := make([]task.Record, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() {
			continue
		}
		id := task.ID(entry.Name())
		if err := id.Validate(); err != nil {
			continue
		}
		record, err := s.Load(ctx, id)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("load task %s: %w", id, err)
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Spec.CreatedAt.Equal(records[j].Spec.CreatedAt) {
			return records[i].Spec.ID < records[j].Spec.ID
		}
		return records[i].Spec.CreatedAt.Before(records[j].Spec.CreatedAt)
	})
	return records, s.applyOrder(records)
}

// SaveRuntime requires exclusive ownership of the task's broker lock.
func (s *JSON) SaveRuntime(ctx context.Context, runtime task.Runtime) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := runtime.Validate(); err != nil {
		return err
	}
	dir, err := s.taskDir(runtime.TaskID)
	if err != nil {
		return err
	}
	var spec task.Spec
	if err := readJSON(filepath.Join(dir, "spec.json"), &spec); err != nil {
		return err
	}
	if spec.ID != runtime.TaskID {
		return errors.New("runtime task ID mismatch")
	}
	return writeJSON(filepath.Join(dir, "runtime.json"), runtime)
}

func readJSON(name string, value any) error {
	info, err := os.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1<<20 {
		return fmt.Errorf("invalid metadata file: %s", name)
	}
	data, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

// Temp file and destination share a directory: fsync -> rename -> directory
// fsync. If directory sync fails after rename, report the ambiguous durability
// result; do not silently claim success or roll back the already visible file.
func writeJSON(name string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > 1<<20 {
		return errors.New("metadata exceeds 1 MiB")
	}
	f, err := os.CreateTemp(filepath.Dir(name), ".pending-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, name); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(name))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
