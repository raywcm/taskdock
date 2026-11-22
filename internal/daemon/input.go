package daemon

// inputDraft conservatively tracks unsent user text without changing PTY bytes.
// Backspace leaves a draft pending because application editing rules differ.
type inputDraft struct {
	pending bool
	escape  []byte
	paste   bool
}

func (d *inputDraft) feed(data []byte) {
	for _, b := range data {
		if len(d.escape) > 0 {
			d.escape = append(d.escape, b)
			if len(d.escape) == 2 {
				if b != '[' && b != ']' && b != 'O' {
					d.pending = true
					d.escape = nil
				}
				continue
			}
			if d.escape[1] == ']' {
				if b == 7 || b == '\\' && d.escape[len(d.escape)-2] == 27 {
					d.escape = nil
				}
				if len(d.escape) > 4096 {
					d.escape = nil
				}
				continue
			}
			if b < 0x40 || b > 0x7e {
				if len(d.escape) > 128 {
					d.escape = nil
				}
				continue
			}
			sequence := string(d.escape)
			switch sequence {
			case "\x1b[200~":
				d.paste = true
				d.pending = true
			case "\x1b[201~":
				d.paste = false
			default:
				// Queries, focus notifications and mouse reports contain no user draft.
				if b != 'R' && b != 'c' && b != 'n' && b != 't' && b != 'm' && b != 'M' && b != 'I' && b != 'O' {
					d.pending = true
				}
			}
			d.escape = nil
			continue
		}
		if b == 27 {
			d.escape = []byte{b}
			continue
		}
		if d.paste {
			d.pending = true
			continue
		}
		switch b {
		case '\r', '\n', 3:
			d.pending = false
		case 0, 5, 12, 17, 19: // Non-editing controls.
		default:
			d.pending = true
		}
	}
}
