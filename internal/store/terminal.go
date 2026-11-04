package store

import (
	"errors"
	"github.com/raywcm/taskdock/internal/task"
)

func (s *JSON) LoadTerminalModes(id task.ID) (task.TerminalModes, error) {
	name, err := s.Path(id, "terminal.json")
	if err != nil {
		return task.TerminalModes{}, err
	}
	var modes task.TerminalModes
	if err := readJSON(name, &modes); err != nil {
		return modes, err
	}
	if modes.TaskID != id || modes.SchemaVersion != 1 || len(modes.Private) > 32 || len(modes.Rendition) > 512 {
		return modes, errors.New("invalid terminal checkpoint")
	}
	if err := modes.InstanceID.Validate(); err != nil {
		return modes, err
	}
	return modes, nil
}
func (s *JSON) SaveTerminalModes(modes task.TerminalModes) error {
	if err := modes.TaskID.Validate(); err != nil {
		return err
	}
	if err := modes.InstanceID.Validate(); err != nil {
		return err
	}
	if modes.SchemaVersion != 1 || len(modes.Private) > 32 || len(modes.Rendition) > 512 {
		return errors.New("invalid terminal checkpoint")
	}
	name, err := s.Path(modes.TaskID, "terminal.json")
	if err != nil {
		return err
	}
	return writeJSON(name, modes)
}
