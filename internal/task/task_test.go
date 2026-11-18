package task

import (
	"strings"
	"testing"
	"time"
)

func validSpec(t *testing.T) Spec {
	t.Helper()
	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	return Spec{
		SchemaVersion: SchemaVersion,
		ID:            id,
		Name:          "demo",
		Command:       []string{"/bin/sh", "-c", "echo hi"},
		Dir:           "/tmp",
		CreatedAt:     time.Now(),
	}
}

func TestNewIDProducesLowercaseHex32(t *testing.T) {
	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID returned error: %v", err)
	}
	if len(id) != 32 {
		t.Fatalf("expected 32 hex chars, got %d (%q)", len(id), id)
	}
	if err := id.Validate(); err != nil {
		t.Fatalf("freshly generated ID rejected: %v", err)
	}
	// Two IDs must differ.
	other, err := NewID()
	if err != nil {
		t.Fatalf("NewID second call: %v", err)
	}
	if id == other {
		t.Fatal("NewID returned identical IDs")
	}
}

func TestIDValidateRejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"empty":          "",
		"too short":      "abc123",
		"too long":       strings.Repeat("a", 34),
		"uppercase":      "ABCDEF0123456789ABCDEF0123456789",
		"non hex":        "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
		"odd length":     "abcdef0123456789abcdef012345678",
		"mixed case hex": "AbCdEf0123456789abcdef0123456789",
	}
	for name, raw := range cases {
		if err := ID(raw).Validate(); err == nil {
			t.Errorf("%s: expected rejection for %q", name, raw)
		}
	}
}

func TestSpecValidateAcceptsBaseline(t *testing.T) {
	if err := validSpec(t).Validate(); err != nil {
		t.Fatalf("baseline spec rejected: %v", err)
	}
}

func TestSpecValidateNameRules(t *testing.T) {
	cases := []struct {
		name  string
		label string
	}{
		{strings.Repeat("a", 129), "129 bytes"},
		{"", "empty"},
		{"   ", "whitespace only"},
		{"bad\x00name", "NUL"},
		{"bad\nname", "newline control char"},
		{"bad\x7fname", "DEL control char"},
		{"-rf", "leading dash"},
		{"--help", "leading double dash"},
		{"\u202eevil", "U+202E RTL override (Cf)"},
		{"admin\u200b", "U+200B zero-width space (Cf)"},
		{"a\u2060b", "U+2060 word joiner (Cf)"},
		{"\ufeffabc", "U+FEFF BOM (Cf)"},
		{"\u061cabc", "U+061C Arabic letter mark (Cf)"},
		{"a\u200eb", "U+200E left-to-right mark (Cf)"},
	}
	for _, c := range cases {
		s := validSpec(t)
		s.Name = c.name
		if err := s.Validate(); err == nil {
			t.Errorf("%s: expected rejection", c.label)
		}
	}

	// Boundary: exactly 128 bytes must pass.
	s := validSpec(t)
	s.Name = strings.Repeat("a", 128)
	if err := s.Validate(); err != nil {
		t.Fatalf("128-byte name should be accepted: %v", err)
	}

	// Multi-byte names are measured in bytes, not runes.
	s = validSpec(t)
	s.Name = strings.Repeat("中", 43) // 129 bytes in UTF-8
	if err := s.Validate(); err == nil {
		t.Fatal("43 CJK chars (129 bytes) should be rejected")
	}
	s.Name = strings.Repeat("中", 42) // 126 bytes
	if err := s.Validate(); err != nil {
		t.Fatalf("42 CJK chars (126 bytes) should be accepted: %v", err)
	}

	// A dash anywhere but the first position stays legal.
	s = validSpec(t)
	s.Name = "my-task-2"
	if err := s.Validate(); err != nil {
		t.Fatalf("interior dashes should be accepted: %v", err)
	}
}

func TestSpecValidateCommandRules(t *testing.T) {
	s := validSpec(t)
	s.Command = nil
	if err := s.Validate(); err == nil {
		t.Error("nil command should be rejected")
	}

	s = validSpec(t)
	s.Command = []string{}
	if err := s.Validate(); err == nil {
		t.Error("empty command slice should be rejected")
	}

	s = validSpec(t)
	s.Command = []string{"", "arg"}
	if err := s.Validate(); err == nil {
		t.Error("empty argv[0] should be rejected")
	}

	s = validSpec(t)
	s.Command = []string{"/bin/sh", "a\x00b"}
	if err := s.Validate(); err == nil {
		t.Error("NUL inside an argument should be rejected")
	}

	s = validSpec(t)
	s.Command = []string{"/bin/sh", string([]byte{0xff, 0xfe})}
	if err := s.Validate(); err == nil {
		t.Error("invalid UTF-8 argument should be rejected")
	}
}

func TestSpecValidateDirectoryRules(t *testing.T) {
	for _, dir := range []string{"", "relative/path", "./also-relative", "~"} {
		s := validSpec(t)
		s.Dir = dir
		if err := s.Validate(); err == nil {
			t.Errorf("non-absolute dir %q should be rejected", dir)
		}
	}

	s := validSpec(t)
	s.Dir = "/tmp\x00evil"
	if err := s.Validate(); err == nil {
		t.Error("dir containing NUL should be rejected")
	}
}

func TestSpecValidateSchemaAndTimestamp(t *testing.T) {
	s := validSpec(t)
	s.SchemaVersion = SchemaVersion + 1
	if err := s.Validate(); err == nil {
		t.Error("future schema version should be rejected")
	}

	s = validSpec(t)
	s.CreatedAt = time.Time{}
	if err := s.Validate(); err == nil {
		t.Error("zero creation time should be rejected")
	}
}

func TestCanTransitionCoversStateMachine(t *testing.T) {
	states := []State{Starting, Running, Stopping, Exited, Failed}
	allowed := map[State]map[State]bool{
		Starting: {Running: true, Failed: true},
		Running:  {Stopping: true, Exited: true, Failed: true},
		Stopping: {Exited: true, Failed: true},
		Exited:   {},
		Failed:   {},
	}
	for _, from := range states {
		for _, to := range states {
			got := CanTransition(from, to)
			want := allowed[from][to]
			if got != want {
				t.Errorf("CanTransition(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}

	// Completed states must never restart, even into themselves.
	for _, terminal := range []State{Exited, Failed} {
		if CanTransition(terminal, terminal) {
			t.Errorf("%s must not transition to itself", terminal)
		}
	}
	// Unknown state is never a valid source.
	if CanTransition(State("bogus"), Running) {
		t.Error("unknown source state should not transition")
	}
}

func TestRuntimeValidate(t *testing.T) {
	taskID, _ := NewID()
	instanceID, _ := NewID()
	code := 0
	base := Runtime{
		SchemaVersion: SchemaVersion,
		TaskID:        taskID,
		InstanceID:    instanceID,
		State:         Running,
		UpdatedAt:     time.Now(),
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("baseline runtime rejected: %v", err)
	}

	// Exited requires an exit code.
	exited := base
	exited.State = Exited
	if err := exited.Validate(); err == nil {
		t.Error("exited runtime without exit code should be rejected")
	}
	exited.ExitCode = &code
	if err := exited.Validate(); err != nil {
		t.Errorf("exited runtime with exit code should pass: %v", err)
	}

	bad := base
	bad.State = State("unknown")
	if err := bad.Validate(); err == nil {
		t.Error("unknown runtime state should be rejected")
	}

	bad = base
	bad.UpdatedAt = time.Time{}
	if err := bad.Validate(); err == nil {
		t.Error("zero update time should be rejected")
	}

	bad = base
	bad.TaskID = "not-a-valid-id"
	if err := bad.Validate(); err == nil {
		t.Error("invalid task ID should be rejected")
	}

	bad = base
	bad.InstanceID = "not-a-valid-id"
	if err := bad.Validate(); err == nil {
		t.Error("invalid instance ID should be rejected")
	}

	bad = base
	bad.SchemaVersion = 99
	if err := bad.Validate(); err == nil {
		t.Error("unsupported schema version should be rejected")
	}
}

func TestSizeValidate(t *testing.T) {
	if err := (Size{Rows: 24, Cols: 80}).Validate(); err != nil {
		t.Fatalf("normal size rejected: %v", err)
	}
	if err := (Size{Rows: 0, Cols: 80}).Validate(); err == nil {
		t.Error("zero rows should be rejected")
	}
	if err := (Size{Rows: 24, Cols: 0}).Validate(); err == nil {
		t.Error("zero cols should be rejected")
	}
	// uint16 extremes are structurally valid; clamping happens in the viewer.
	if err := (Size{Rows: 65535, Cols: 65535}).Validate(); err != nil {
		t.Errorf("max uint16 size should pass validation: %v", err)
	}
}

func TestElapsedUsesRecordedBounds(t *testing.T) {
	created := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	started := created.Add(5 * time.Second)
	finished := started.Add(90 * time.Second)

	spec := Spec{CreatedAt: created}

	// No runtime: measure from creation to now.
	if got := (Record{Spec: spec}).Elapsed(created.Add(time.Minute)); got != time.Minute {
		t.Errorf("expected 1m from creation, got %v", got)
	}

	// Running: measure from StartedAt to now, ignoring CreatedAt.
	running := &Runtime{State: Running, StartedAt: &started}
	if got := (Record{Spec: spec, Runtime: running}).Elapsed(started.Add(30 * time.Second)); got != 30*time.Second {
		t.Errorf("expected 30s from start, got %v", got)
	}

	// Exited with FinishedAt: freeze at finish, ignore now.
	exited := &Runtime{State: Exited, StartedAt: &started, FinishedAt: &finished}
	code := 0
	exited.ExitCode = &code
	if got := (Record{Spec: spec, Runtime: exited}).Elapsed(started.Add(24 * time.Hour)); got != 90*time.Second {
		t.Errorf("expected frozen 90s, got %v", got)
	}

	// Failed without FinishedAt: fall back to UpdatedAt.
	updated := started.Add(45 * time.Second)
	failed := &Runtime{State: Failed, StartedAt: &started, UpdatedAt: updated}
	if got := (Record{Spec: spec, Runtime: failed}).Elapsed(updated.Add(time.Hour)); got != 45*time.Second {
		t.Errorf("expected 45s from UpdatedAt, got %v", got)
	}

	// Clock skew: end before start must clamp to zero, never negative.
	if got := (Record{Spec: spec}).Elapsed(created.Add(-time.Hour)); got != 0 {
		t.Errorf("expected 0 for negative span, got %v", got)
	}

	// Zero CreatedAt with no runtime also clamps to zero.
	if got := (Record{}).Elapsed(time.Now()); got != 0 {
		t.Errorf("expected 0 for zero start, got %v", got)
	}
}
