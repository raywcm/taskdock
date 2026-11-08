// Package terminal owns raw terminal input and attached-session display.
// Native forwarding is available alongside the bounded status-bar viewport.
package terminal

import (
	"errors"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// WithRaw restores termios on normal return, errors and panic unwinding. The
// attach coordinator must handle HUP/TERM and cancel I/O before returning here.
// SIGKILL cannot run deferred cleanup. ANSI mode cleanup is a separate concern.
func WithRaw(tty *os.File, run func() error) (err error) {
	fd := int(tty.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, term.Restore(fd, state)) }()
	return run()
}

// EscapeDecoder handles detach keys without decoding application output.
// Unknown input is returned unchanged; incomplete escape sequences are bounded.
type EscapeDecoder struct {
	Disable bool
	// Command recognizes additional manager keys outside bracketed paste.
	Command func([]byte, uint16) bool
	// Ignore drops reserved keys without ending the current input route.
	Ignore func([]byte, uint16) bool
	// Transform changes application keys after manager commands, outside paste.
	Transform func([]byte) []byte
	// Report consumes terminal replies owned by an embedded manager.
	Report func([]byte) bool
	// Remaining contains bytes after a handled command, for pane switching.
	Remaining []byte
	escape    []byte
	pasting   bool
}

const inputEscapeLimit = 64

func (d *EscapeDecoder) Feed(input []byte) (output []byte, detach bool) {
	d.Remaining = nil
	if d.Disable {
		return append([]byte(nil), input...), false
	}
	for index, b := range input {
		// A control key after a bare Esc is a new key, not a CSI sequence.
		// This also handles Vim Esc + detach arriving in a single TTY read
		// after startup/repaint delayed input processing.
		if len(d.escape) == 1 && b < 0x20 {
			part, done := d.consume(d.escape, 0)
			d.escape = nil
			output = append(output, part...)
			if done {
				d.Remaining = append([]byte(nil), input[index:]...)
				return output, true
			}
		}
		if len(d.escape) > 0 {
			d.escape = append(d.escape, b)
			if d.Report != nil && len(d.escape) >= 2 && d.escape[1] == ']' {
				end := b == 7 || (len(d.escape) > 2 && d.escape[len(d.escape)-2] == 0x1b && b == '\\')
				if !end && len(d.escape) < 512 {
					continue
				}
				part, done := d.consume(d.escape, 0)
				d.escape = nil
				output = append(output, part...)
				if done {
					d.Remaining = append([]byte(nil), input[index+1:]...)
					return output, true
				}
				continue
			}
			if len(d.escape) == 2 && (b == '[' || b == 'O') {
				continue
			}
			if len(d.escape) > 2 && b < 0x40 && len(d.escape) < inputEscapeLimit {
				continue
			}
			part, done := d.consume(d.escape, escapeKey(d.escape))
			d.escape = nil
			output = append(output, part...)
			if done {
				d.Remaining = append([]byte(nil), input[index+1:]...)
				return output, true
			}
			continue
		}
		if b == 0x1b {
			d.escape = []byte{b}
			continue
		}
		part, done := d.consume(input[index:index+1], uint16(b))
		output = append(output, part...)
		if done {
			d.Remaining = append([]byte(nil), input[index+1:]...)
			return output, true
		}
	}
	return output, false
}

const (
	keyF12 uint16 = 256 + iota
	keyPasteStart
	keyPasteEnd
	keyRelease
	// KeyCtrlEnter is distinct only in enhanced terminal keyboard encodings.
	KeyCtrlEnter
)

func (d *EscapeDecoder) consume(raw []byte, key uint16) ([]byte, bool) {
	if d.pasting {
		if key == keyPasteEnd {
			d.pasting = false
		}
		return raw, false
	}
	if d.Report != nil && d.Report(raw) {
		return nil, false
	}
	if key == 0x11 || key == keyF12 {
		return nil, true
	}
	if key == keyPasteStart {
		d.pasting = true
	}
	if !d.pasting && d.Ignore != nil && d.Ignore(raw, key) {
		return nil, false
	}
	if !d.pasting && d.Command != nil && d.Command(raw, key) {
		return nil, true
	}
	if !d.pasting && d.Transform != nil {
		return d.Transform(raw), false
	}
	return raw, false
}

// escapeKey recognizes manager keys in CSI-u and modifyOtherKeys encodings.
// Modified F12 and release events pass through.
func escapeKey(raw []byte) uint16 {
	s := string(raw)
	if s == "\x1b[200~" {
		return keyPasteStart
	}
	if s == "\x1b[201~" {
		return keyPasteEnd
	}
	if len(s) < 4 || !strings.HasPrefix(s, "\x1b[") {
		return 0
	}
	final := s[len(s)-1]
	if final != '~' && final != 'u' {
		return 0
	}
	parts := strings.Split(s[2:len(s)-1], ";")
	code, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0
	}
	modifiers := 1
	if len(parts) > 1 {
		fields := strings.Split(parts[1], ":")
		if len(fields) > 2 {
			return 0
		}
		modifiers, err = strconv.Atoi(fields[0])
		if err != nil || modifiers < 1 {
			return 0
		}
		if len(fields) == 2 {
			if fields[1] == "3" {
				return keyRelease
			}
			if fields[1] != "1" && fields[1] != "2" {
				return 0
			}
		}
	}
	// Caps/Num lock do not change these shortcuts.
	mods := (modifiers - 1) &^ (64 | 128)
	if final == '~' && code == 24 && len(parts) <= 2 && mods == 0 {
		return keyF12
	}
	if final == '~' && code == 27 && len(parts) == 3 {
		code, err = strconv.Atoi(parts[2])
		if err != nil {
			return 0
		}
	} else if final != 'u' || len(parts) > 2 {
		return 0
	}
	if mods == 4 {
		switch code {
		case 13:
			return KeyCtrlEnter
		case 113:
			return 0x11
		case 102:
			return 0x06
		}
	}
	return 0
}

// flushEscape releases a standalone Esc or a truncated sequence after a short
// input deadline.
func (d *EscapeDecoder) flushEscape() []byte {
	if len(d.escape) == 0 {
		return nil
	}
	out, _ := d.consume(d.escape, 0)
	d.escape = nil
	return out
}

func (d *EscapeDecoder) Flush() []byte {
	return d.flushEscape()
}

// PendingEscape reports a partial terminal sequence requiring a short timeout.
func (d *EscapeDecoder) PendingEscape() bool { return len(d.escape) != 0 }

func (d *EscapeDecoder) PendingReport() bool {
	return d.Report != nil && len(d.escape) >= 2 && d.escape[1] == ']'
}

// FlushEscape forwards a standalone Esc or truncated escape sequence.
func (d *EscapeDecoder) FlushEscape() []byte { return d.flushEscape() }
