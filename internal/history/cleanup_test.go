package history

import (
	"context"
	"errors"
	"github.com/raywcm/taskdock/internal/store"
	"github.com/raywcm/taskdock/internal/task"
	"os"
	"path/filepath"
	"testing"
	"time"
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
