package service

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/raywcm/taskdock/internal/task"
)

func TestStopAndListRetireOrphansWithoutTouchingRecordedPIDs(t *testing.T) {
	for _, action := range []string{"stop", "list"} {
		t.Run(action, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			m, err := New(filepath.Join(t.TempDir(), "state"))
			if err != nil {
				t.Fatal(err)
			}
			id, _ := task.NewID()
			instance, _ := task.NewID()
			started := time.Now().Add(-time.Hour)
			spec := task.Spec{SchemaVersion: 1, ID: id, Name: "orphan", Command: []string{"/bin/sh"}, Dir: t.TempDir(), CreatedAt: started}
			if err := m.Store.Create(ctx, spec); err != nil {
				t.Fatal(err)
			}
			runtime := task.Runtime{SchemaVersion: 1, TaskID: id, InstanceID: instance, State: task.Running,
				BrokerPID: os.Getpid(), ChildPID: os.Getpid(), StartedAt: &started, UpdatedAt: started}
			if err := m.Store.SaveRuntime(ctx, runtime); err != nil {
				t.Fatal(err)
			}
			release, err := m.Store.BrokerLock(id)
			if err != nil {
				t.Fatal(err)
			}
			release = sync.OnceFunc(release)
			t.Cleanup(release)
			if err := m.Stop(ctx, spec.Name); err == nil {
				t.Fatal("Stop succeeded while the broker still owned its lock")
			}
			records, err := m.List(ctx)
			if err != nil || len(records) != 1 || records[0].Health != "unreachable" || records[0].Runtime.State != task.Running {
				t.Fatalf("failed health probe retired a lock-owned session: %+v %v", records, err)
			}
			release()
			if action == "stop" {
				if err := m.Stop(ctx, spec.Name); err != nil {
					t.Fatal(err)
				}
			} else {
				records, err := m.List(ctx)
				if err != nil || len(records) != 1 || records[0].Health != "finished" {
					t.Fatalf("listing did not retire the session: %+v %v", records, err)
				}
			}
			r, err := m.Store.Load(ctx, id)
			if err != nil || r.Runtime.State != task.Failed || r.Runtime.FinishedAt == nil || r.Runtime.Error == "" {
				t.Fatalf("stopped state was not persisted: %+v %v", r.Runtime, err)
			}
			if err := m.Stop(ctx, spec.Name); err != nil {
				t.Fatal(err)
			}
			latest, err := m.Store.Load(ctx, id)
			if err != nil || !latest.Runtime.UpdatedAt.Equal(r.Runtime.UpdatedAt) {
				t.Fatalf("repeated Stop changed the finish time: %+v %v", latest.Runtime, err)
			}
		})
	}
}
