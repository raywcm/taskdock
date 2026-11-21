package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/raywcm/taskdock/internal/task"
	"golang.org/x/sys/unix"
)

func (s *JSON) Root() string { return s.root }
func (s *JSON) Path(id task.ID, name string) (string, error) {
	dir, err := s.taskDir(id)
	if err != nil {
		return "", err
	}
	if filepath.Base(name) != name {
		return "", errors.New("invalid task filename")
	}
	return filepath.Join(dir, name), nil
}

// Lock serializes catalog mutations across CLI processes. Network calls must not
// run while this lock is held, except the bounded launch readiness transaction.
func (s *JSON) Lock(ctx context.Context) (func(), error) {
	return lock(ctx, filepath.Join(s.root, "catalog.lock"), true)
}
func (s *JSON) BrokerLock(id task.ID) (func(), error) {
	dir, err := s.taskDir(id)
	if err != nil {
		return nil, err
	}
	if err := checkPrivateDir(dir); err != nil {
		return nil, err
	}
	return lock(context.Background(), filepath.Join(dir, "broker.lock"), false)
}

// RetireOrphan requires the catalog lock. It records the loss of a broker
// only after taking its exclusive lock, without relying on stored PIDs.
func (s *JSON) RetireOrphan(ctx context.Context, id task.ID, now time.Time) (task.Record, error) {
	record, err := s.Load(ctx, id)
	if err != nil || (record.Runtime != nil && !task.CanTransition(record.Runtime.State, task.Failed)) {
		return record, err
	}
	release, err := s.BrokerLock(id)
	if err != nil {
		return record, err
	}
	defer release()
	// The broker may have saved a normal exit immediately before releasing its lock.
	record, err = s.Load(ctx, id)
	if err != nil || (record.Runtime != nil && !task.CanTransition(record.Runtime.State, task.Failed)) {
		return record, err
	}
	var runtime task.Runtime
	if record.Runtime == nil {
		instance, err := task.NewID()
		if err != nil {
			return record, err
		}
		runtime = task.Runtime{SchemaVersion: task.SchemaVersion, TaskID: id, InstanceID: instance,
			Error: "session startup was interrupted before the broker recorded its state"}
	} else {
		runtime = *record.Runtime
		runtime.Error = "broker disappeared; child session may need manual inspection"
	}
	finished := now.UTC()
	runtime.State = task.Failed
	runtime.UpdatedAt = finished
	runtime.FinishedAt = &finished
	if err := s.SaveRuntime(ctx, runtime); err != nil {
		return record, err
	}
	record.Runtime = &runtime
	return record, nil
}

func lock(ctx context.Context, name string, wait bool) (func(), error) {
	fd, err := unix.Open(name, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		f.Close()
		return nil, err
	}
	if int(stat.Uid) != os.Getuid() || stat.Mode&0077 != 0 || stat.Mode&unix.S_IFMT != unix.S_IFREG {
		f.Close()
		return nil, errors.New("unsafe lock file")
	}
	for {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = f.Close() }, nil
		}
		if !wait || (!errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN)) {
			f.Close()
			return nil, fmt.Errorf("task or catalog is busy: %w", err)
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (s *JSON) Rename(ctx context.Context, id task.ID, name string) error {
	r, err := s.Load(ctx, id)
	if err != nil {
		return err
	}
	r.Spec.Name = name
	if err := r.Spec.Validate(); err != nil {
		return err
	}
	target, _ := s.Path(id, "spec.json")
	return writeJSON(target, r.Spec)
}

// Remove requires the catalog and broker locks. IDs are never reused.
func (s *JSON) Remove(ctx context.Context, id task.ID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := s.taskDir(id)
	if err != nil {
		return err
	}
	// Removal must succeed for an unreadable record too. Loading first would
	// leave a corrupt spec.json undeletable, which is exactly the state a
	// partial write produces. Existence of the directory is still required so
	// that deleting an unknown ID keeps reporting ErrNotExist.
	if err := checkPrivateDir(dir); err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func (s *JSON) Log(id task.ID) (*os.File, error) {
	name, err := s.Path(id, "output.log")
	if err != nil {
		return nil, err
	}
	return os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
}
