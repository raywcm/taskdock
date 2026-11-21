package history

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/raywcm/taskdock/internal/ipc"
	"github.com/raywcm/taskdock/internal/store"
	"github.com/raywcm/taskdock/internal/task"
)

func stopped(t *testing.T, repository *store.JSON, state task.State, ended time.Time) task.ID {
	t.Helper()
	id, _ := task.NewID()
	instance, _ := task.NewID()
	spec := task.Spec{SchemaVersion: 1, ID: id, Name: string(id[:8]), Command: []string{"/bin/sh"}, Dir: "/tmp", CreatedAt: ended.Add(-time.Hour)}
	if err := repository.Create(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	code := 0
	runtime := task.Runtime{SchemaVersion: 1, TaskID: id, InstanceID: instance, State: state, UpdatedAt: ended, FinishedAt: &ended}
	if state == task.Exited {
		runtime.ExitCode = &code
	}
	if err := repository.SaveRuntime(context.Background(), runtime); err != nil {
		t.Fatal(err)
	}
	path, _ := repository.Path(id, "output.log")
	if err := os.WriteFile(path, []byte("old log"), 0600); err != nil {
		t.Fatal(err)
	}
	return id
}
func TestRetentionDeletesOnlyExpiredUnlockedHistory(t *testing.T) {
	ctx := context.Background()
	repository, err := store.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old := now.Add(-2 * time.Hour)
	expired := stopped(t, repository, task.Exited, old)
	failed := stopped(t, repository, task.Failed, old)
	recent := stopped(t, repository, task.Exited, now.Add(-10*time.Minute))
	orphan := stopped(t, repository, task.Running, old)
	live := stopped(t, repository, task.Running, old)
	releaseLive, err := repository.BrokerLock(live)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseLive()
	locked := stopped(t, repository, task.Exited, old)
	release, err := repository.BrokerLock(locked)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := Sweep(ctx, repository, now)
	if err != nil || !pending {
		t.Fatal(pending, err)
	}
	for _, id := range []task.ID{expired, failed} {
		if _, err := repository.Load(ctx, id); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expired record survived: %s %v", id, err)
		}
	}
	for _, id := range []task.ID{recent, orphan, live, locked} {
		if _, err := repository.Load(ctx, id); err != nil {
			t.Fatalf("protected record removed: %s %v", id, err)
		}
	}
	r, err := repository.Load(ctx, orphan)
	if err != nil || r.Runtime.State != task.Failed || r.Runtime.FinishedAt == nil || !r.Runtime.FinishedAt.Equal(now) {
		t.Fatalf("orphan was not retired with a fresh retention window: %+v %v", r.Runtime, err)
	}
	r, err = repository.Load(ctx, live)
	if err != nil || r.Runtime.State != task.Running {
		t.Fatalf("lock-owned session was changed: %+v %v", r.Runtime, err)
	}
	release()
	if _, err := Sweep(ctx, repository, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Load(ctx, locked); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("released expired record survived", err)
	}
}
func TestDisabledAndExtendedRetentionProtectHistory(t *testing.T) {
	ctx := context.Background()
	repository, _ := store.Open(filepath.Join(t.TempDir(), "state"))
	now := time.Now()
	id := stopped(t, repository, task.Exited, now.Add(-2*time.Hour))
	settings, _ := repository.Settings()
	settings.HistoryRetentionSeconds = 0
	if err := repository.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	orphan := stopped(t, repository, task.Stopping, now.Add(-2*time.Hour))
	if pending, err := Sweep(ctx, repository, now); err != nil || pending {
		t.Fatal(pending, err)
	}
	r, err := repository.Load(ctx, orphan)
	if err != nil || r.Runtime.State != task.Failed {
		t.Fatalf("disabled expiry prevented recovery: %+v %v", r.Runtime, err)
	}
	if _, err := repository.Load(ctx, id); err != nil {
		t.Fatal(err)
	}
	settings.HistoryRetentionSeconds = 24 * 3600
	_ = repository.SaveSettings(ctx, settings)
	if _, err := Sweep(ctx, repository, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Load(ctx, id); err != nil {
		t.Fatal("extended retention removed history")
	}
	// Old workers ignore future frontend language codes while enforcing policy.
	path := filepath.Join(repository.Root(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"language":"future-LANG","history_retention_seconds":3600}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Sweep(ctx, repository, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Load(ctx, id); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("future language broke cleanup", err)
	}
}
func TestWorkerDoesNotRecreateRemovedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "removed")
	if err := Run(context.Background(), path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("recreated removed state")
	}
}

func TestInvalidSettingsStillRecoverAndKeepHistory(t *testing.T) {
	for _, data := range []string{`{"partial":`, `null`, `{"history_retention_seconds": null}`, `{"schema_version":2}`, `{"history_retention_seconds":-1}`} {
		t.Run(data, func(t *testing.T) {
			ctx := context.Background()
			s, err := store.Open(filepath.Join(t.TempDir(), "state"))
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			ended := now.Add(-2 * time.Hour)
			old := stopped(t, s, task.Exited, ended)
			lost := stopped(t, s, task.Running, ended)
			path := filepath.Join(s.Root(), "settings.json")
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if pending, err := Sweep(ctx, s, now); !pending || err == nil {
				t.Fatalf("invalid retention policy: pending=%t err=%v", pending, err)
			}
			r, err := s.Load(ctx, lost)
			if err != nil || r.Runtime == nil || r.Runtime.State != task.Failed {
				t.Fatalf("orphan was not recovered: %+v %v", r.Runtime, err)
			}
			if _, err := s.Load(ctx, old); err != nil {
				t.Fatalf("expired history while retention was unknown: %v", err)
			}
			if err := os.WriteFile(path, []byte(`{"language":{"future":true},"history_retention_seconds":3600}`), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Sweep(ctx, s, now); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Load(ctx, old); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("known policy did not resume expiry: %v", err)
			}
		})
	}
}

func TestSweepContinuesPastBlockedHistory(t *testing.T) {
	ctx := context.Background()
	repository, err := store.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	blocked := stopped(t, repository, task.Exited, now.Add(-3*time.Hour))
	expired := stopped(t, repository, task.Exited, now.Add(-2*time.Hour))
	path, err := ipc.SocketPath(repository.Root(), blocked)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	pending, err := Sweep(ctx, repository, now)
	if !pending || err == nil || !strings.Contains(err.Error(), string(blocked)) {
		t.Fatalf("missing diagnostic for blocked history: %v %v", pending, err)
	}
	if _, err := repository.Load(ctx, expired); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("other expired history survived: %v", err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "preserve" {
		t.Fatalf("modified unexpected socket entry: %q %v", data, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if pending, err := Sweep(ctx, repository, now); err != nil || pending {
		t.Fatalf("blocked history could not be retried: %v %v", pending, err)
	}
}

func TestSweepHandlesInterruptedStartupWithExpiryOnAndOff(t *testing.T) {
	for _, retention := range []time.Duration{0, time.Hour} {
		t.Run(retention.String(), func(t *testing.T) {
			ctx := context.Background()
			repository, err := store.Open(filepath.Join(t.TempDir(), "state"))
			if err != nil {
				t.Fatal(err)
			}
			settings, err := repository.Settings()
			if err != nil {
				t.Fatal(err)
			}
			settings.HistoryRetentionSeconds = int64(retention / time.Second)
			if err := repository.SaveSettings(ctx, settings); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			id, _ := task.NewID()
			spec := task.Spec{SchemaVersion: 1, ID: id, Name: "interrupted", Command: []string{"/bin/sh"}, Dir: "/tmp", CreatedAt: now}
			if err := repository.Create(ctx, spec); err != nil {
				t.Fatal(err)
			}
			if !needsWorker([]task.Record{{Spec: spec}}, retention) {
				t.Fatal("no worker for a session without runtime state")
			}
			if pending, err := Sweep(ctx, repository, now); err != nil || !pending {
				t.Fatal("worker left before startup finished", pending, err)
			}
			record, err := repository.Load(ctx, id)
			if err != nil || record.Runtime != nil {
				t.Fatal("retired during the startup window", record.Runtime, err)
			}
			later := now.Add(2 * time.Hour)
			release, err := repository.BrokerLock(id)
			if err != nil {
				t.Fatal(err)
			}
			pending, err := Sweep(ctx, repository, later)
			release()
			if err != nil || !pending {
				t.Fatal("lost track of a lock-owned startup", pending, err)
			}
			record, err = repository.Load(ctx, id)
			if err != nil || record.Runtime != nil {
				t.Fatal("changed a lock-owned startup", record.Runtime, err)
			}
			if pending, err := Sweep(ctx, repository, later); err != nil || pending != (retention > 0) {
				t.Fatal("unexpected work after retirement", pending, err)
			}
			record, err = repository.Load(ctx, id)
			if err != nil || record.Runtime == nil || record.Runtime.State != task.Failed || record.Runtime.FinishedAt == nil || !record.Runtime.FinishedAt.Equal(later) {
				t.Fatalf("interrupted session lost its history: %+v %v", record.Runtime, err)
			}
		})
	}
}
