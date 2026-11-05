package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/x/ansi"
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
func (m *Manager) SaveSettings(ctx context.Context, settings config.Settings) error {
	if err := m.Store.SaveSettings(ctx, settings); err != nil {
		return err
	}
	if m.maintenance {
		if err := history.Ensure(m.Store); err != nil {
			return fmt.Errorf("settings saved but history cleanup could not start: %w", err)
		}
	}
	return nil
}
func (m *Manager) EnableHistory() error { m.maintenance = true; return history.Ensure(m.Store) }
func (m *Manager) Resources(ctx context.Context, records []task.Record) map[task.ID]task.Resources {
	return m.sampler.Sample(ctx, records)
}

func (m *Manager) List(ctx context.Context) ([]task.Record, error) {
	if m.maintenance {
		_ = history.Ensure(m.Store)
	}
	records, err := m.Store.List(ctx)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	return records, nil
}
func (m *Manager) Observe(ctx context.Context, r task.Record) task.Record {
	if r.Runtime == nil {
		r.Health = "not-started"
		return r
	}
	if r.Runtime.State == task.Exited || r.Runtime.State == task.Failed {
		r.Health = "finished"
		return r
	}
	probe, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	live, err := ipc.Query(probe, m.Store.Root(), *r.Runtime, false)
	if err != nil {
		// A broker can finish between loading disk and connecting.
		if latest, loadErr := m.Store.Load(ctx, r.Spec.ID); loadErr == nil && latest.Runtime != nil && (latest.Runtime.State == task.Exited || latest.Runtime.State == task.Failed) {
			latest.Health = "finished"
			return latest
		}
		r.Health = "unreachable"
		return r
	}
	r.Runtime = &live
	r.Health = "online"
	return r
}
func (m *Manager) Resolve(ctx context.Context, ref string) (task.Record, error) {
	if task.ID(ref).Validate() == nil {
		if r, err := m.Store.Load(ctx, task.ID(ref)); err == nil {
			return r, nil
		}
	}
	records, err := m.Store.List(ctx)
	if err != nil {
		return task.Record{}, err
	}
	for _, r := range records {
		if r.Spec.Name == ref {
			return r, nil
		}
	}
	var matches []task.Record
	if len(ref) >= 4 {
		for _, r := range records {
			if strings.HasPrefix(string(r.Spec.ID), ref) {
				matches = append(matches, r)
			}
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
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
			return task.Record{}, &NameConflictError{Name: name, Suggestion: SuggestedName(records, name, "")}
		}
	}
	if err := m.Store.Create(ctx, spec); err != nil {
		return task.Record{}, err
	}
	if m.maintenance {
		defer func() { _ = history.Ensure(m.Store) }()
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
	_ = read.SetReadDeadline(time.Now().Add(5 * time.Second))
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
	if r.Runtime == nil {
		return errors.New("task never started")
	}
	if r.Runtime.State == task.Exited || r.Runtime.State == task.Failed {
		return m.waitRetired(ctx, r.Spec.ID)
	}
	if _, err := ipc.Query(ctx, m.Store.Root(), *r.Runtime, true); err != nil {
		latest := m.Observe(ctx, r)
		if latest.Health == "finished" {
			return m.waitRetired(ctx, r.Spec.ID)
		}
		return fmt.Errorf("cannot stop task through its broker: %w", err)
	}
	ticker := time.NewTicker(30 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			r, err = m.Store.Load(ctx, r.Spec.ID)
			if err != nil {
				return err
			}
			if r.Runtime != nil && (r.Runtime.State == task.Exited || r.Runtime.State == task.Failed) {
				return m.waitRetired(ctx, r.Spec.ID)
			}
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
	r, err := m.Resolve(ctx, ref)
	if err != nil {
		return err
	}
	release, err := m.Store.BrokerLock(r.Spec.ID)
	if err != nil {
		return errors.New("task broker is still alive; stop it before deleting")
	}
	defer release()
	// The lock, rather than stale runtime metadata or a reused PID, proves
	// that no broker owns this record. Delete never signals recorded PIDs.
	socket, err := ipc.SocketPath(m.Store.Root(), r.Spec.ID)
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
	}
	return m.Store.Remove(ctx, r.Spec.ID)
}

// Recover acquires the broker's exclusive lock before changing stale metadata.
// It deliberately never signals a potentially reused PID from a file.
func (m *Manager) Recover(ctx context.Context, ref string) error {
	unlock, err := m.Store.Lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	r, err := m.Resolve(ctx, ref)
	if err != nil {
		return err
	}
	release, err := m.Store.BrokerLock(r.Spec.ID)
	if err != nil {
		return errors.New("broker is still alive")
	}
	defer release()
	if r.Runtime == nil {
		return nil
	}
	if r.Runtime.State == task.Exited || r.Runtime.State == task.Failed {
		return nil
	}
	r.Runtime.State = task.Failed
	r.Runtime.Error = "broker disappeared; child session may need manual inspection"
	r.Runtime.UpdatedAt = time.Now().UTC()
	return m.Store.SaveRuntime(ctx, *r.Runtime)
}

func (m *Manager) Logs(ctx context.Context, ref string, raw bool, tail int) ([]byte, error) {
	if tail < 0 {
		return nil, errors.New("tail must be non-negative")
	}
	r, err := m.Resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	var data []byte
	for _, name := range []string{"output.log.1", "output.log"} {
		file, _ := m.Store.Path(r.Spec.ID, name)
		info, err := os.Lstat(file)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return nil, errors.New("unsafe output log")
		}
		part, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		data = append(data, part...)
	}
	if tail > 0 && len(data) > tail {
		data = data[len(data)-tail:]
	}
	if !raw {
		data = []byte(ansi.Strip(string(data)))
		clean := make([]byte, 0, len(data))
		for _, b := range data {
			if b >= 32 || b == '\n' || b == '\r' || b == '\t' {
				clean = append(clean, b)
			}
		}
		data = clean
	}
	return data, nil
}
