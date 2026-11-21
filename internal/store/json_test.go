package store

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raywcm/taskdock/internal/task"
)

func fixture(t *testing.T) (*JSON, task.Spec) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	id, err := task.NewID()
	if err != nil {
		t.Fatal(err)
	}
	spec := task.Spec{SchemaVersion: 1, ID: id, Name: "中文 task", Command: []string{"/bin/sh", "-c", "true"}, Dir: "/tmp", CreatedAt: time.Now().UTC()}
	if err := s.Create(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	return s, spec
}

func TestOversizedSpecDoesNotPoisonCatalog(t *testing.T) {
	s, spec := fixture(t)
	id, err := task.NewID()
	if err != nil {
		t.Fatal(err)
	}
	spec.ID = id
	spec.Command = []string{"/bin/echo", strings.Repeat("x", 1<<20)}
	if err := s.Create(context.Background(), spec); err == nil {
		t.Fatal("accepted oversized metadata")
	}
	records, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("valid tasks lost or invalid task exposed: %d", len(records))
	}
	spec.Command = []string{"/bin/echo", "ok"}
	if err := s.Create(context.Background(), spec); err != nil {
		t.Fatalf("failed create left its ID reserved: %v", err)
	}
}

func TestRecordRoundTripAndPermissions(t *testing.T) {
	s, spec := fixture(t)
	ctx := context.Background()
	instance, _ := task.NewID()
	state := task.Runtime{SchemaVersion: 1, TaskID: spec.ID, InstanceID: instance, State: task.Running, UpdatedAt: time.Now().UTC()}
	if err := s.SaveRuntime(ctx, state); err != nil {
		t.Fatal(err)
	}
	record, err := s.Load(ctx, spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Spec.Name != spec.Name || record.Runtime.State != task.Running {
		t.Fatalf("bad record: %+v", record)
	}
	if err := s.Create(ctx, spec); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("duplicate ID: %v", err)
	}
	dir, _ := s.taskDir(spec.ID)
	for _, name := range []string{"spec.json", "runtime.json"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("permissions for %s: %v", name, err)
		}
	}
}

func TestCatalogLoadsLegacyNamesAndEnforcesRulesOnWrites(t *testing.T) {
	for _, oldName := range []string{"-legacy", "old\u200btask", "👩\u200d💻"} {
		t.Run(oldName, func(t *testing.T) {
			ctx := context.Background()
			s, ordinary := fixture(t)
			legacy := ordinary
			id, err := task.NewID()
			if err != nil {
				t.Fatal(err)
			}
			legacy.ID, legacy.Name = id, "legacy"
			if err := s.Create(ctx, legacy); err != nil {
				t.Fatal(err)
			}
			// Simulate a spec persisted before the new name restrictions.
			legacy.Name = oldName
			path, err := s.Path(id, "spec.json")
			if err != nil {
				t.Fatal(err)
			}
			if err := writeJSON(path, legacy); err != nil {
				t.Fatal(err)
			}

			records, err := s.List(ctx)
			if err != nil || len(records) != 2 {
				t.Fatalf("list with a legacy name: %d records, %v", len(records), err)
			}
			record, err := s.Load(ctx, id)
			if err != nil || record.Spec.Name != oldName {
				t.Fatalf("load legacy name: %q, %v", record.Spec.Name, err)
			}
			if err := s.Rename(ctx, ordinary.ID, oldName); err == nil {
				t.Fatal("rename accepted a name prohibited for new writes")
			}
			if err := s.Rename(ctx, id, "renamed"); err != nil {
				t.Fatal(err)
			}
			record, err = s.Load(ctx, id)
			if err != nil || record.Spec.Name != "renamed" {
				t.Fatalf("load after rename: %q, %v", record.Spec.Name, err)
			}
		})
	}
}

func TestAtomicRuntimeSnapshots(t *testing.T) {
	s, spec := fixture(t)
	instance, _ := task.NewID()
	state := task.Runtime{SchemaVersion: 1, TaskID: spec.ID, InstanceID: instance, State: task.Running, UpdatedAt: time.Now().UTC()}
	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 25 {
			state.ChildPID = i
			if err := s.SaveRuntime(ctx, state); err != nil {
				t.Error(err)
				return
			}
		}
	})
	for range 50 {
		if _, err := s.Load(ctx, spec.ID); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}

func TestRejectTraversalPublicRootAndSymlinkMetadata(t *testing.T) {
	s, spec := fixture(t)
	if _, err := s.Load(context.Background(), task.ID("../../elsewhere")); err == nil {
		t.Fatal("accepted traversal")
	}
	dir := filepath.Join(t.TempDir(), "public")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("accepted public root")
	}
	taskDir, _ := s.taskDir(spec.ID)
	if err := os.Symlink(filepath.Join(taskDir, "spec.json"), filepath.Join(taskDir, "runtime.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(context.Background(), spec.ID); err == nil {
		t.Fatal("accepted symlink metadata")
	}
}
