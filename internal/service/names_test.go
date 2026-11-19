package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/raywcm/taskdock/internal/task"
)

func nameRecord(name string) task.Record {
	id, _ := task.NewID()
	return task.Record{Spec: task.Spec{SchemaVersion: 1, ID: id, Name: name, Command: []string{"/bin/sh"}, Dir: "/tmp", CreatedAt: time.Now(), Size: task.Size{Rows: 24, Cols: 80}}}
}
func TestNumberedNamesIncludeHistoryAndRespectUTF8Limit(t *testing.T) {
	records := []task.Record{nameRecord("work"), nameRecord("work-2"), nameRecord("work-3")}
	if got := SuggestedName(records, "work", ""); got != "work-4" {
		t.Fatal(got)
	}
	if got := SuggestedName(records, "work", records[0].Spec.ID); got != "work" {
		t.Fatal("same task name was considered a conflict")
	}
	name := strings.Repeat("中", 42) + "ab"
	got := SuggestedName([]task.Record{nameRecord(name)}, name, "")
	if !utf8.ValidString(got) || len(got) > 128 || !strings.HasSuffix(got, "-2") {
		t.Fatalf("invalid numbered name: %q", got)
	}
}
func TestNameConflictReturnsSuggestionWithoutMutation(t *testing.T) {
	m, err := New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, second := nameRecord("work"), nameRecord("other")
	for _, r := range []task.Record{first, second} {
		if err := m.Store.Create(ctx, r.Spec); err != nil {
			t.Fatal(err)
		}
	}
	_, err = m.Create(ctx, "work", []string{"/bin/sh"}, "/tmp", task.Size{Rows: 24, Cols: 80}, false)
	var conflict *NameConflictError
	if !errors.As(err, &conflict) || conflict.Suggestion != "work-2" {
		t.Fatalf("create: %v", err)
	}
	err = m.Rename(ctx, string(second.Spec.ID), "work")
	if !errors.As(err, &conflict) || conflict.Suggestion != "work-2" {
		t.Fatalf("rename: %v", err)
	}
	records, _ := m.Store.List(ctx)
	if len(records) != 2 {
		t.Fatal("conflicting create reserved a task")
	}
	unchanged, _ := m.Store.Load(ctx, second.Spec.ID)
	if unchanged.Spec.Name != "other" {
		t.Fatal("conflicting rename changed metadata")
	}
	if err := m.Rename(ctx, string(second.Spec.ID), conflict.Suggestion); err != nil {
		t.Fatal(err)
	}
	unchanged, _ = m.Store.Load(ctx, second.Spec.ID)
	if unchanged.Spec.Name != "work-2" {
		t.Fatal("explicit numbered rename failed")
	}
}

func TestResolveRejectsNameThatAlsoMatchesAnotherID(t *testing.T) {
	m, err := New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const prefix = "abcd"
	first, second := nameRecord(prefix), nameRecord("worker")
	first.Spec.ID = task.ID(prefix + strings.Repeat("0", 28))
	second.Spec.ID = task.ID(prefix + strings.Repeat("1", 28))
	for _, r := range []task.Record{first, second} {
		if err := m.Store.Create(ctx, r.Spec); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Resolve(ctx, prefix); err == nil || !strings.Contains(err.Error(), "use the full ID") {
		t.Fatalf("ambiguous reference resolved to a task: %v", err)
	}
	r, err := m.Resolve(ctx, string(second.Spec.ID))
	if err != nil || r.Spec.ID != second.Spec.ID {
		t.Fatalf("full ID did not resolve: %q, %v", r.Spec.ID, err)
	}
}
