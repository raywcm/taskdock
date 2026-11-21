package store

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/raywcm/taskdock/internal/task"
)

func TestOrderPersistsAcrossStoresAndConcurrentMoves(t *testing.T) {
	s, spec := fixture(t)
	ctx := context.Background()
	ids := []task.ID{spec.ID}
	for i := 1; i < 12; i++ {
		spec.ID, _ = task.NewID()
		spec.CreatedAt = spec.CreatedAt.Add(time.Second)
		if err := s.Create(ctx, spec); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, spec.ID)
	}
	other, err := Open(s.Root())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			if err := other.Move(ctx, ids[10], -1); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	rows, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rows[2].Spec.ID != ids[10] {
		t.Fatal("concurrent moves lost updates or changed the selected ID")
	}
	info, err := os.Stat(filepath.Join(s.Root(), "order.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("order must be private", err)
	}
	// New sessions append; changing a name does not change position.
	if err := s.Rename(ctx, ids[10], "renamed"); err != nil {
		t.Fatal(err)
	}
	spec.ID, _ = task.NewID()
	spec.CreatedAt = spec.CreatedAt.Add(time.Second)
	if err := s.Create(ctx, spec); err != nil {
		t.Fatal(err)
	}
	rows, err = other.List(ctx)
	if err != nil || rows[2].Spec.ID != ids[10] || rows[len(rows)-1].Spec.ID != spec.ID {
		t.Fatal("rename/new changed order", err)
	}
}

func TestMoveStaysWithinRunningOrStoppedTab(t *testing.T) {
	s, spec := fixture(t)
	ctx := context.Background()
	stopped := spec.ID
	first, _ := task.NewID()
	last, _ := task.NewID()
	for _, id := range []task.ID{first, last} {
		spec.ID = id
		spec.CreatedAt = spec.CreatedAt.Add(time.Second)
		if err := s.Create(ctx, spec); err != nil {
			t.Fatal(err)
		}
		instance, _ := task.NewID()
		if err := s.SaveRuntime(ctx, task.Runtime{SchemaVersion: 1, TaskID: id, InstanceID: instance, State: task.Running, UpdatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Move(ctx, first, -1); err != nil {
		t.Fatal(err)
	}
	if err := s.Move(ctx, last, -1); err != nil {
		t.Fatal(err)
	}
	rows, err := s.List(ctx)
	if err != nil || rows[0].Spec.ID != stopped || rows[1].Spec.ID != last || rows[2].Spec.ID != first {
		t.Fatal("move crossed tabs", err)
	}
	if err := s.Move(ctx, first, 3); err == nil {
		t.Fatal("invalid direction accepted")
	}
}

func TestUnreadableOrderKeepsCatalogAvailable(t *testing.T) {
	for _, contents := range []string{
		"{",
		`{"schema_version":2}`,
		`{"schema_version":1,"ids":["bad"]}`,
		`{"schema_version":1,"ids":["0123456789abcdef0123456789abcdef","0123456789abcdef0123456789abcdef"]}`,
	} {
		s, first := fixture(t)
		ctx := context.Background()
		second := first
		second.ID, _ = task.NewID()
		second.CreatedAt = first.CreatedAt.Add(time.Second)
		if err := s.Create(ctx, second); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(s.Root(), "order.json")
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		rows, warnings, err := s.ListWithWarnings(ctx)
		if err != nil || len(warnings) != 1 || len(rows) != 2 || rows[0].Spec.ID != first.ID || rows[1].Spec.ID != second.ID {
			t.Fatalf("order %q: got %d rows, warnings %v, err %v", contents, len(rows), warnings, err)
		}
		if err := s.Move(ctx, second.ID, -1); err == nil {
			t.Fatal("move overwrote unreadable order")
		}
		after, err := os.ReadFile(path)
		if err != nil || string(after) != contents {
			t.Fatalf("saved order changed: %q, %v", after, err)
		}
	}
}
