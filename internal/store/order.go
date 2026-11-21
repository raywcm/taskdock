package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/raywcm/taskdock/internal/task"
)

type sessionOrder struct {
	Version int       `json:"schema_version"`
	IDs     []task.ID `json:"ids"`
}

func (s *JSON) applyOrder(records []task.Record) error {
	var order sessionOrder
	err := readJSON(filepath.Join(s.root, "order.json"), &order)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if order.Version != 1 {
		return errors.New("unsupported session order schema")
	}
	rank := make(map[task.ID]int, len(order.IDs))
	for i, id := range order.IDs {
		if id.Validate() != nil {
			return errors.New("invalid session order ID")
		}
		if _, exists := rank[id]; exists {
			return errors.New("duplicate session order ID")
		}
		rank[id] = i
	}
	sort.SliceStable(records, func(i, j int) bool {
		a, aok := rank[records[i].Spec.ID]
		b, bok := rank[records[j].Spec.ID]
		if aok != bok {
			return aok
		}
		return aok && a < b
	})
	return nil
}

// Move re-reads under the catalog lock so simultaneous frontends cannot
// overwrite one another's order with a stale list. Deleted IDs are pruned.
func (s *JSON) Move(ctx context.Context, id task.ID, direction int) error {
	if direction != -1 && direction != 1 {
		return errors.New("move direction must be -1 or 1")
	}
	release, err := s.Lock(ctx)
	if err != nil {
		return err
	}
	defer release()
	records, _, err := s.listByCreation(ctx)
	if err != nil {
		return err
	}
	// Reordering writes the file, so preserve unreadable or newer formats.
	if err := s.applyOrder(records); err != nil {
		return fmt.Errorf("cannot update order.json: %w", err)
	}
	index := -1
	for i, r := range records {
		if r.Spec.ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return os.ErrNotExist
	}
	stopped := func(r task.Record) bool {
		return r.Runtime == nil || r.Runtime.State == task.Exited || r.Runtime.State == task.Failed
	}
	next := index + direction
	for next >= 0 && next < len(records) && stopped(records[next]) != stopped(records[index]) {
		next += direction
	}
	if next < 0 || next >= len(records) {
		return nil
	}
	records[index], records[next] = records[next], records[index]
	order := sessionOrder{Version: 1, IDs: make([]task.ID, len(records))}
	for i, r := range records {
		order.IDs[i] = r.Spec.ID
	}
	return writeJSON(filepath.Join(s.root, "order.json"), order)
}
