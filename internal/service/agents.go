package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/raywcm/taskdock/internal/task"
)

func ManagedAgentsUI(argv []string) bool {
	return len(argv) >= 2 && filepath.Base(argv[0]) == "agentctl" && argv[len(argv)-1] == "tui"
}

func (m *Manager) EnsureAgents(ctx context.Context, executable string, size task.Size) (task.Record, error) {
	settings, err := m.Settings()
	if err != nil {
		return task.Record{}, err
	}
	// Existing frontends observe this preference without restarting their PTY.
	locale := exec.CommandContext(ctx, executable, "--language", settings.Language, "locale")
	if output, err := locale.CombinedOutput(); err != nil {
		return task.Record{}, errors.New(string(output))
	}
	name := "taskdock-agents-ui"
	record, err := m.Resolve(ctx, name)
	if err == nil {
		if !ManagedAgentsUI(record.Spec.Command) {
			return task.Record{}, errors.New("taskdock-agents-ui is already used by another program")
		}
		if record.Runtime != nil && (record.Runtime.State == task.Exited || record.Runtime.State == task.Failed) {
			if err = m.Delete(ctx, string(record.Spec.ID)); err != nil {
				return task.Record{}, err
			}
			err = errors.New("new frontend required")
		}
	}
	if err != nil {
		directory, err := os.Getwd()
		if err != nil {
			return task.Record{}, err
		}
		return m.Create(ctx, name, []string{executable, "--tdock-state-dir", m.Store.Root(), "tui"}, directory, size, false)
	}
	return record, nil
}
