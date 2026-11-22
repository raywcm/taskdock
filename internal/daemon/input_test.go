package daemon

import "testing"

func TestInputDraftProtectsTextAndPastes(t *testing.T) {
	var d inputDraft
	d.feed([]byte("claude\r"))
	if d.pending {
		t.Fatal("submitted command")
	}
	for _, part := range []string{"\x1b[", "1;1R", "\x1b]11;rgb:ffff/ffff/ffff\x1b", "\\"} {
		d.feed([]byte(part))
	}
	if d.pending {
		t.Fatal("terminal response became a draft")
	}
	d.feed([]byte("unfinished"))
	if !d.pending {
		t.Fatal("lost draft")
	}
	d.feed([]byte{127})
	if !d.pending {
		t.Fatal("assumed application edit")
	}
	d.feed([]byte{3})
	if d.pending {
		t.Fatal("cancelled draft")
	}
	d.feed([]byte("\x1b[200~first\nsecond\x1b[201~"))
	if !d.pending || d.paste {
		t.Fatal("paste state")
	}
	d.feed([]byte("\r"))
	if d.pending {
		t.Fatal("submitted paste")
	}
}
