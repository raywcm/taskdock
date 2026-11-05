package service

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/raywcm/taskdock/internal/task"
)

func TestDeleteUsesBrokerLockInsteadOfStaleStateOrPIDs(t *testing.T) {
	for _, state := range []task.State{task.Starting, task.Running, task.Stopping, task.Exited, task.Failed} {
		t.Run(string(state), func(t *testing.T) {
			ctx := context.Background()
			m, err := New(filepath.Join(t.TempDir(), "state"))
			if err != nil {
				t.Fatal(err)
			}
			id, _ := task.NewID()
			instance, _ := task.NewID()
			spec := task.Spec{SchemaVersion: 1, ID: id, Name: "stale", Command: []string{"/bin/sh"}, Dir: t.TempDir(), CreatedAt: time.Now(), Size: task.Size{Rows: 24, Cols: 80}}
			if err := m.Store.Create(ctx, spec); err != nil {
				t.Fatal(err)
			}
			code := 0
			// A live unrelated PID in old metadata must never be signalled.
			runtime := task.Runtime{SchemaVersion: 1, TaskID: id, InstanceID: instance, State: state, BrokerPID: os.Getpid(), ChildPID: os.Getpid(), ExitCode: &code, UpdatedAt: time.Now()}
			if err := m.Store.SaveRuntime(ctx, runtime); err != nil {
				t.Fatal(err)
			}
			release, err := m.Store.BrokerLock(id)
			if err != nil {
				t.Fatal(err)
			}
			if err := m.Delete(ctx, string(id)); err == nil {
				t.Fatal("deleted a lock-owned record despite an unreachable socket")
			}
			if _, err := m.Store.Load(ctx, id); err != nil {
				t.Fatal("failed deletion removed the live broker record", err)
			}
			release()
			if err := m.Delete(ctx, string(id)); err != nil {
				t.Fatal("required recovery of a record with no broker", err)
			}
			if _, err := m.Store.Load(ctx, id); !os.IsNotExist(err) {
				t.Fatal("stale record remains", err)
			}
			if err := syscall.Kill(os.Getpid(), 0); err != nil {
				t.Fatal("deletion touched the unrelated process", err)
			}
		})
	}
}
