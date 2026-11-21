package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/raywcm/taskdock/internal/config"
	"github.com/raywcm/taskdock/internal/daemon"
	"github.com/raywcm/taskdock/internal/history"
	"github.com/raywcm/taskdock/internal/ipc"
	"github.com/raywcm/taskdock/internal/metrics"
	"github.com/raywcm/taskdock/internal/store"
	"github.com/raywcm/taskdock/internal/task"
)

type Manager struct {
	Store       *store.JSON
	sampler     *metrics.Sampler
	maintenance bool
}

func New(state string) (*Manager, error) {
	s, err := store.Open(state)
	if err != nil {
		return nil, err
	}
	return &Manager{Store: s, sampler: metrics.New()}, nil
}

func (m *Manager) Settings() (config.Settings, error) { return m.Store.Settings() }
func (m *Manager) Move(ctx context.Context, id task.ID, direction int) error {
	return m.Store.Move(ctx, id, direction)
}
func (m *Manager) UpdateSettings(ctx context.Context, edit config.SettingsEdit) (config.Settings, error) {
	settings, err := m.Store.UpdateSettings(ctx, edit)
	if err != nil {
		return config.Settings{}, err
	}
	if err := history.Ensure(m.Store); err != nil {
		return settings, fmt.Errorf("settings saved but history cleanup could not start: %w", err)
	}
	return settings, nil
}
func (m *Manager) EnableHistory() error { m.maintenance = true; return history.Ensure(m.Store) }
func (m *Manager) Resources(ctx context.Context, records []task.Record) map[task.ID]task.Resources {
	return m.sampler.Sample(ctx, records)
}

func (m *Manager) List(ctx context.Context) ([]task.Record, error) {
	records, _, err := m.ListWithWarnings(ctx)
	return records, err
}

func (m *Manager) ListWithWarnings(ctx context.Context) ([]task.Record, []error, error) {
	if m.maintenance {
		_ = history.Ensure(m.Store)
	}
	records, warnings, err := m.Store.ListWithWarnings(ctx)
	if err != nil {
		return nil, nil, err
	}
	var wg sync.WaitGroup
	slots := make(chan struct{}, 16)
	for i := range records {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-slots }()
			records[i] = m.Observe(ctx, records[i])
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return records, warnings, nil
}
func (m *Manager) Observe(ctx context.Context, r task.Record) task.Record {
	if r.Runtime == nil {
		if !r.AwaitingBroker(time.Now()) {
			latest, err := m.retireOrphan(ctx, r.Spec.ID)
			if err != nil && !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
				r.Health = "unreachable"
				return r
			}
			r = latest
		}
		if r.Runtime == nil {
			r.Health = "not-started"
			return r
		}
	}
	if r.Runtime.State.Finished() {
		r.Health = "finished"
		return r
	}
	probe, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	live, err := ipc.Query(probe, m.Store.Root(), *r.Runtime, false)
	if err != nil {
		if latest, err := m.retireOrphan(ctx, r.Spec.ID); err == nil && latest.Runtime != nil {
			latest.Health = "finished"
			return latest
		}
		r.Health = "unreachable"
		return r
	}
	r.Runtime = &live
	r.Health = "online"
	if live.State.Finished() {
		r.Health = "finished"
	}
	return r
}
func (m *Manager) Resolve(ctx context.Context, ref string) (task.Record, error) {
	if task.ID(ref).Validate() == nil {
		if r, err := m.Store.Load(ctx, task.ID(ref)); !errors.Is(err, os.ErrNotExist) {
			return r, err
		}
	}
	records, err := m.Store.List(ctx)
	if err != nil {
		return task.Record{}, err
	}
	var byName *task.Record
	for i := range records {
		if records[i].Spec.Name == ref {
			byName = &records[i]
			break
		}
	}
	var byPrefix []task.Record
	if len(ref) >= 4 {
		for _, r := range records {
			if strings.HasPrefix(string(r.Spec.ID), ref) {
				byPrefix = append(byPrefix, r)
			}
		}
	}
	// A task named exactly like another task's ID prefix must not silently
	// shadow it. Previously the name won, so a prefix lookup could resolve to
	// the wrong task with no warning.
	if byName != nil && (len(byPrefix) > 1 || len(byPrefix) == 1 && byPrefix[0].Spec.ID != byName.Spec.ID) {
		return task.Record{}, fmt.Errorf("reference %q matches a task name and another task's ID prefix; use the full ID", ref)
	}
	if byName != nil {
		return *byName, nil
	}
	if len(byPrefix) == 1 {
		return byPrefix[0], nil
	}
	if len(byPrefix) > 1 {
		return task.Record{}, errors.New("ambiguous task ID prefix")
	}
	return task.Record{}, fmt.Errorf("task %q not found", ref)
}

func (m *Manager) Create(ctx context.Context, name string, argv []string, dir string, size task.Size, capture bool) (task.Record, error) {
	id, err := task.NewID()
	if err != nil {
		return task.Record{}, err
	}
	if name == "" {
		name = "task-" + string(id[:8])
	}
	spec := task.Spec{SchemaVersion: 1, ID: id, Name: name, Command: argv, Dir: dir, Size: size, CaptureInitial: capture, CreatedAt: time.Now().UTC()}
	if err := spec.Validate(); err != nil {
		return task.Record{}, err
	}
	if err := size.Validate(); err != nil {
		return task.Record{}, err
	}
	unlock, err := m.Store.Lock(ctx)
	if err != nil {
		return task.Record{}, err
	}
	defer unlock()
	records, err := m.Store.List(ctx)
	if err != nil {
		return task.Record{}, err
	}
	for _, r := range records {
		if r.Spec.Name == name {
			// Preserve the exact name match for new -A, even when the name
			// is also the full ID or ID prefix of another task.
			return r, &NameConflictError{Name: name, Suggestion: SuggestedName(records, name, "")}
		}
	}
	if m.maintenance {
		// Let the worker survive an interrupted first launch, even if this
		// process exits before it gets to start the broker.
		_ = history.StartWorker(m.Store)
	}
	if err := m.Store.Create(ctx, spec); err != nil {
		return task.Record{}, err
	}
	if err := m.launch(ctx, id); err != nil {
		return task.Record{Spec: spec}, fmt.Errorf("task %s: %w (inspect with tdock show %s)", id, err, id)
	}
	r, err := m.Store.Load(context.Background(), id)
	return r, err
}

func (m *Manager) launch(ctx context.Context, id task.ID) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	read, write, err := os.Pipe()
	if err != nil {
		return err
	}
	defer read.Close()
	logName, _ := m.Store.Path(id, "broker.log")
	log, err := os.OpenFile(logName, os.O_CREATE|os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		write.Close()
		return err
	}
	defer log.Close()
	null, err := os.Open(os.DevNull)
	if err != nil {
		write.Close()
		return err
	}
	defer null.Close()
	cmd := exec.Command(exe, "--state-dir", m.Store.Root(), "__broker", string(id))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = null, log, log
	cmd.ExtraFiles = []*os.File{write}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		write.Close()
		return err
	}
	write.Close()
	go func() { _ = cmd.Wait() }() // Reap while a long-running TUI remains alive.
	cancel := context.AfterFunc(ctx, func() { read.Close() })
	defer cancel()
	_ = read.SetReadDeadline(time.Now().Add(task.StartupTimeout))
	var ready daemon.Ready
	if err := json.NewDecoder(io.LimitReader(read, 16*1024)).Decode(&ready); err != nil {
		return fmt.Errorf("broker readiness is uncertain: %w", err)
	}
	if ready.Error != "" {
		return errors.New(ready.Error)
	}
	if ready.Runtime == nil || ready.Runtime.TaskID != id {
		return errors.New("invalid broker readiness identity")
	}
	return ready.Runtime.Validate()
}

func (m *Manager) Rename(ctx context.Context, ref, name string) error {
	unlock, err := m.Store.Lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	r, err := m.Resolve(ctx, ref)
	if err != nil {
		return err
	}
	records, err := m.Store.List(ctx)
	if err != nil {
		return err
	}
	for _, other := range records {
		if other.Spec.ID != r.Spec.ID && other.Spec.Name == name {
			return &NameConflictError{Name: name, Suggestion: SuggestedName(records, name, r.Spec.ID)}
		}
	}
	return m.Store.Rename(ctx, r.Spec.ID, name)
}
func (m *Manager) Stop(ctx context.Context, ref string) error {
	r, err := m.Resolve(ctx, ref)
	if err != nil {
		return err
	}
	if r.Runtime == nil || r.Runtime.State == task.Starting {
		r, err = m.resolveStartupForStop(ctx, r.Spec.ID)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	grace := r.Runtime.StopGrace
	if grace == 0 {
		grace = task.DefaultStopGrace
	}
	// Leave room for broker IPC and PTY teardown. The broker's advertised
	// grace applies even when this client has a different environment.
	const overhead = 3*ipc.Timeout + time.Second
	ctx, cancel := context.WithTimeout(ctx, min(grace, time.Duration(math.MaxInt64)-overhead)+overhead)
	defer cancel()
	if r.Runtime.State.Finished() {
		return m.waitRetired(ctx, r.Spec.ID)
	}
	if _, err := ipc.Query(ctx, m.Store.Root(), *r.Runtime, true); err != nil {
		if _, recoveryErr := m.retireOrphan(ctx, r.Spec.ID); recoveryErr == nil || errors.Is(recoveryErr, os.ErrNotExist) {
			return m.waitRetired(ctx, r.Spec.ID)
		} else if !errors.Is(recoveryErr, syscall.EWOULDBLOCK) && !errors.Is(recoveryErr, syscall.EAGAIN) {
			return fmt.Errorf("cannot recover task after stop request failed: %w", recoveryErr)
		}
		return fmt.Errorf("cannot stop task through its broker: %w", err)
	}
	if err := m.waitRetired(ctx, r.Spec.ID); err != nil {
		return err
	}
	// The broker can disappear after acknowledging Stop but before saving the exit.
	_, err = m.retireOrphan(ctx, r.Spec.ID)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (m *Manager) resolveStartupForStop(ctx context.Context, id task.ID) (task.Record, error) {
	ctx, cancel := context.WithTimeout(ctx, task.StartupTimeout)
	defer cancel()
	for {
		r, err := m.retireOrphan(ctx, id)
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return r, err
		}
		// A live broker may own the lock before its control socket is ready.
		if r.Runtime != nil && r.Runtime.State != task.Starting {
			return r, nil
		}
		select {
		case <-ctx.Done():
			return r, fmt.Errorf("waiting for broker startup: %w", ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (m *Manager) waitRetired(ctx context.Context, id task.ID) error {
	for {
		release, err := m.Store.BrokerLock(id)
		if err == nil {
			release()
			return nil
		}
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}
func (m *Manager) Delete(ctx context.Context, ref string) error {
	unlock, err := m.Store.Lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	// An exact ID can address an unreadable record. Keep its broker lock
	// through both socket cleanup and removal of the task directory.
	id := task.ID(ref)
	var release func()
	exact := id.Validate() == nil
	if exact {
		release, err = m.Store.BrokerLock(id)
	}
	if !exact || errors.Is(err, os.ErrNotExist) {
		r, resolveErr := m.Resolve(ctx, ref)
		if resolveErr != nil {
			return resolveErr
		}
		id = r.Spec.ID
		release, err = m.Store.BrokerLock(id)
	}
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return errors.New("task broker is still alive; stop it before deleting")
	}
	if err != nil {
		return err
	}
	defer release()
	// The lock, rather than stale runtime metadata or a reused PID, proves
	// that no broker owns this record. Delete never signals recorded PIDs.
	socket, err := ipc.SocketPath(m.Store.Root(), id)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("invalid socket path")
		}
		if err := os.Remove(socket); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return m.Store.Remove(ctx, id)
}

func (m *Manager) Recover(ctx context.Context, ref string) error {
	r, err := m.Resolve(ctx, ref)
	if err != nil {
		return err
	}
	_, err = m.retireOrphan(ctx, r.Spec.ID)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return errors.New("broker is still alive")
	}
	return err
}

func (m *Manager) retireOrphan(ctx context.Context, id task.ID) (task.Record, error) {
	unlock, err := m.Store.Lock(ctx)
	if err != nil {
		return task.Record{}, err
	}
	defer unlock()
	return m.Store.RetireOrphan(ctx, id, time.Now())
}
