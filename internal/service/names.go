package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/raywcm/taskdock/internal/task"
)

// NameConflictError carries a reviewable suggestion. Mutation never silently
// accepts the alternative; the frontend must explicitly submit that exact name.
type NameConflictError struct{ Name, Suggestion string }

func (e *NameConflictError) Error() string {
	return fmt.Sprintf("task name %q already exists; suggested name: %q", e.Name, e.Suggestion)
}

func activeRecord(r task.Record) bool {
	return r.Runtime == nil || r.Runtime.State == task.Running || r.Runtime.State == task.Starting || r.Runtime.State == task.Stopping
}

func SuggestedName(records []task.Record, name string, exclude task.ID) string {
	used := make(map[string]bool, len(records))
	for _, r := range records {
		if activeRecord(r) && r.Spec.ID != exclude {
			used[r.Spec.Name] = true
		}
	}
	if !used[name] {
		return name
	}
	for n := 2; ; n++ {
		suffix := "-" + strconv.Itoa(n)
		prefix := name
		for len(prefix)+len(suffix) > 128 {
			_, size := utf8.DecodeLastRuneInString(prefix)
			prefix = prefix[:len(prefix)-size]
		}
		candidate := strings.TrimSpace(prefix) + suffix
		if !used[candidate] {
			return candidate
		}
	}
}
func (m *Manager) NameSuggestion(ctx context.Context, name string, exclude task.ID) (string, error) {
	records, err := m.Store.List(ctx)
	if err != nil {
		return "", err
	}
	return SuggestedName(records, name, exclude), nil
}
