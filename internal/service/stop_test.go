package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/raywcm/taskdock/internal/task"
)

func TestInterruptedStartupCommands(t *testing.T) {
	for _, action := range []string{"stop", "recover", "list", "follow"} {
		t.Run(action, func(t *testing.T) {
			m, err := New(filepath.Join(t.TempDir(), "state"))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			r := nameRecord("interrupted")
			r.Spec.CreatedAt = time.Now().Add(-time.Hour)
			if err := m.Store.Create(ctx, r.Spec); err != nil {
				t.Fatal(err)
			}
			switch action {
			case "stop":
				err = m.Stop(ctx, r.Spec.Name)
			case "recover":
				err = m.Recover(ctx, r.Spec.Name)
			case "list":
				records, listErr := m.List(ctx)
				if listErr != nil || len(records) != 1 || records[0].Health != "finished" {
					t.Fatalf("list: %+v, %v", records, listErr)
				}
			case "follow":
				path, _ := m.Store.Path(r.Spec.ID, "output.log")
				if err := os.WriteFile(path, []byte("retained\n"), 0600); err != nil {
					t.Fatal(err)
				}
				var out bytes.Buffer
				err = m.FollowLogs(ctx, r.Spec.Name, false, 0, &out)
				if out.String() != "retained\n" {
					t.Fatalf("follow output: %q", out.String())
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			record, err := m.Store.Load(ctx, r.Spec.ID)
			if err != nil {
				t.Fatal(err)
			}
			rt := record.Runtime
			if rt == nil || rt.State != task.Failed || rt.Error == "" || rt.FinishedAt == nil ||
				rt.StartedAt != nil || rt.ChildPID != 0 || rt.BrokerPID != 0 {
				t.Fatalf("interrupted startup: %+v", rt)
			}
			if err := m.Recover(ctx, r.Spec.Name); err != nil {
				t.Fatal(err)
			}
			latest, err := m.Resolve(ctx, r.Spec.Name)
			if err != nil || latest.Runtime == nil || latest.Runtime.InstanceID != rt.InstanceID || !latest.Runtime.UpdatedAt.Equal(rt.UpdatedAt) {
				t.Fatalf("recovery changed a completed record: %+v %v", latest.Runtime, err)
			}
		})
	}
}

func TestInterruptedStartupHonorsOwnerLocks(t *testing.T) {
	m, err := New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	r := nameRecord("launching")
	r.Spec.CreatedAt = time.Now().Add(-time.Hour)
	if err := m.Store.Create(ctx, r.Spec); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{"creator", "broker"} {
		t.Run(owner, func(t *testing.T) {
			var release func()
			if owner == "creator" {
				release, err = m.Store.Lock(ctx)
			} else {
				release, err = m.Store.BrokerLock(r.Spec.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			stopCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
			defer cancel()
			if err := m.Stop(stopCtx, r.Spec.Name); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("stop during startup: %v", err)
			}
			if owner == "broker" && m.Recover(ctx, r.Spec.Name) == nil {
				t.Fatal("recovered a session still owned by its broker")
			}
			record, err := m.Store.Load(ctx, r.Spec.ID)
			if err != nil || record.Runtime != nil {
				t.Fatalf("changed a lock-owned startup: %+v, %v", record.Runtime, err)
			}
		})
	}
}

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
