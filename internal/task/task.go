// Package task defines persisted data and domain rules, without terminal or UI dependencies.
package task

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const SchemaVersion = 1

type ID string

func NewID() (ID, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return ID(hex.EncodeToString(b[:])), nil
}

func (id ID) Validate() error {
	decoded, err := hex.DecodeString(string(id))
	if err != nil || len(decoded) != 16 || strings.ToLower(string(id)) != string(id) {
		return errors.New("task ID must be 32 lowercase hexadecimal characters")
	}
	return nil
}

// Spec is written by the service layer. Command is argv, never an implicit shell expression.
// Environment is inherited at launch; secrets are deliberately not persisted here.
type Spec struct {
	SchemaVersion  int       `json:"schema_version"`
	ID             ID        `json:"id"`
	Name           string    `json:"name"`
	WindowName     string    `json:"window_name,omitempty"`
	Command        []string  `json:"command"`
	Dir            string    `json:"dir"`
	CreatedAt      time.Time `json:"created_at"`
	Size           Size      `json:"size"`
	CaptureInitial bool      `json:"capture_initial,omitempty"`
}

// Validate checks a spec before creating a task or changing its name.
func (s Spec) Validate() error {
	if err := s.ValidateStored(); err != nil {
		return err
	}
	if strings.ContainsFunc(s.Name, func(r rune) bool { return unicode.Is(unicode.Cf, r) }) {
		return errors.New("name must not contain Unicode format characters")
	}
	if strings.HasPrefix(s.Name, "-") {
		return errors.New("name must not start with a dash")
	}
	return nil
}

// ValidateStored accepts names saved by older versions so their tasks can
// still be listed, stopped, and renamed after an upgrade.
func (s Spec) ValidateStored() error {
	if err := s.ID.Validate(); err != nil {
		return err
	}
	if s.SchemaVersion != SchemaVersion {
		return errors.New("unsupported spec schema")
	}
	if !utf8.ValidString(s.Name) || strings.TrimSpace(s.Name) == "" || len(s.Name) > 128 || strings.ContainsFunc(s.Name, unicode.IsControl) {
		return errors.New("name must contain 1–128 bytes without control characters")
	}
	if s.WindowName != "" && (!utf8.ValidString(s.WindowName) || strings.TrimSpace(s.WindowName) == "" || len(s.WindowName) > 128 ||
		strings.ContainsFunc(s.WindowName, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) })) {
		return errors.New("window name must contain 1–128 bytes of printable text")
	}
	if len(s.Command) == 0 || s.Command[0] == "" {
		return errors.New("command is required")
	}
	for _, arg := range s.Command {
		if !utf8.ValidString(arg) || strings.ContainsRune(arg, 0) {
			return errors.New("command contains NUL")
		}
	}
	if !filepath.IsAbs(s.Dir) || strings.ContainsRune(s.Dir, 0) {
		return errors.New("working directory must be absolute")
	}
	if s.CreatedAt.IsZero() {
		return errors.New("creation time is required")
	}
	return nil
}

type State string

const (
	Starting State = "starting"
	Running  State = "running"
	Stopping State = "stopping"
	Exited   State = "exited"
	Failed   State = "failed"
)

func (s State) Finished() bool { return s == Exited || s == Failed }

// CanTransition does not allow restarting a completed ID. A restart will create a new task.
func CanTransition(from, to State) bool {
	switch from {
	case Starting:
		return to == Running || to == Failed
	case Running:
		return to == Stopping || to == Exited || to == Failed
	case Stopping:
		return to == Exited || to == Failed
	default:
		return false
	}
}

const DefaultStopGrace = 5 * time.Second

// StartupTimeout is how long the creator waits for broker readiness.
const StartupTimeout = 5 * time.Second

// Runtime is written under the broker lock. PIDs are diagnostic, not identity.
type Runtime struct {
	SchemaVersion int           `json:"schema_version"`
	TaskID        ID            `json:"task_id"`
	InstanceID    ID            `json:"instance_id"`
	State         State         `json:"state"`
	BrokerPID     int           `json:"broker_pid"`
	ChildPID      int           `json:"child_pid"`
	UpdatedAt     time.Time     `json:"updated_at"`
	ExitCode      *int          `json:"exit_code,omitempty"`
	Signal        string        `json:"signal,omitempty"`
	Error         string        `json:"error,omitempty"`
	StartedAt     *time.Time    `json:"started_at,omitempty"`
	FinishedAt    *time.Time    `json:"finished_at,omitempty"`
	StopGrace     time.Duration `json:"stop_grace_ns,omitempty"`
}

func (r Runtime) Validate() error {
	if r.SchemaVersion != SchemaVersion {
		return errors.New("unsupported runtime schema")
	}
	if err := r.TaskID.Validate(); err != nil {
		return err
	}
	if err := r.InstanceID.Validate(); err != nil {
		return err
	}
	switch r.State {
	case Starting, Running, Stopping, Exited, Failed:
	default:
		return errors.New("invalid runtime state")
	}
	if r.UpdatedAt.IsZero() {
		return errors.New("runtime update time is required")
	}
	if r.StopGrace < 0 {
		return errors.New("invalid stop grace period")
	}
	if r.State == Exited && r.ExitCode == nil {
		return errors.New("exited runtime requires an exit code")
	}
	return nil
}

// Record contains a disk snapshot. It is not proof that the broker is currently alive.
type Record struct {
	Spec      Spec       `json:"spec"`
	Runtime   *Runtime   `json:"runtime,omitempty"`
	Health    string     `json:"health,omitempty"`
	Resources *Resources `json:"resources,omitempty"`
}

// AwaitingBroker gives a newly spawned broker time to take its lock if the
// creator stopped waiting before the broker got scheduled.
func (r Record) AwaitingBroker(now time.Time) bool {
	return r.Runtime == nil && now.Before(r.Spec.CreatedAt.Add(StartupTimeout))
}

type Resources struct {
	CPUPercent        float64   `json:"cpu_percent"`
	CPUAvailable      bool      `json:"cpu_available"`
	MemoryBytes       uint64    `json:"memory_bytes"`
	ForegroundCommand string    `json:"foreground_command,omitempty"`
	ProcessCount      int       `json:"process_count"`
	SampledAt         time.Time `json:"sampled_at"`
}

type Size struct {
	Rows uint16 `json:"rows"`
	Cols uint16 `json:"cols"`
}

func (s Size) Validate() error {
	if s.Rows == 0 || s.Cols == 0 {
		return errors.New("terminal size must be non-zero")
	}
	return nil
}

// Elapsed freezes completed tasks at their recorded finish time. Older brokers
// without explicit timestamps use creation / last terminal-state update.
func (r Record) Elapsed(now time.Time) time.Duration {
	start := r.Spec.CreatedAt
	if r.Runtime != nil && r.Runtime.StartedAt != nil {
		start = *r.Runtime.StartedAt
	}
	end := now
	if r.Runtime != nil && (r.Runtime.State == Exited || r.Runtime.State == Failed) {
		if r.Runtime.FinishedAt != nil {
			end = *r.Runtime.FinishedAt
		} else {
			end = r.Runtime.UpdatedAt
		}
	}
	if start.IsZero() || end.Before(start) {
		return 0
	}
	return end.Sub(start)
}
