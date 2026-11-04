package task

// TerminalModes is a client-side boundary checkpoint, not a screen emulator.
// It belongs to one broker instance and is updated only at attach/detach.
type TerminalModes struct {
	SchemaVersion int          `json:"schema_version"`
	TaskID        ID           `json:"task_id"`
	InstanceID    ID           `json:"instance_id"`
	Private       map[int]bool `json:"private"`
	Insert        *bool        `json:"insert,omitempty"`
	Keypad        *bool        `json:"keypad,omitempty"`
	CursorStyle   *int         `json:"cursor_style,omitempty"`
	Rendition     string       `json:"rendition,omitempty"`
}
