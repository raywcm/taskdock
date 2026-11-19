package service

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLogTailKeepsExactRawBytesAcrossRotationBoundary(t *testing.T) {
	ctx := context.Background()
	m, err := New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	r := nameRecord("logs")
	if err := m.Store.Create(ctx, r.Spec); err != nil {
		t.Fatal(err)
	}
	data := []byte("old世\x1b[31mnew\x1b[0m")
	path, _ := m.Store.Path(r.Spec.ID, "output.log")
	const boundary = 4 // The log rotated midway through a UTF-8 character.
	if err := os.WriteFile(path+".1", data[:boundary], 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data[boundary:], 0600); err != nil {
		t.Fatal(err)
	}
	for tail := 0; tail <= len(data)+1; tail++ {
		want := data
		if tail > 0 {
			want = data[max(0, len(data)-tail):]
		}
		got, err := m.Logs(ctx, r.Spec.Name, true, tail)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("raw tail %d: got %q, want %q (%v)", tail, got, want, err)
		}
	}
	got, err := m.Logs(ctx, r.Spec.Name, false, len(data)-boundary)
	if err != nil || string(got) != "new" {
		t.Fatalf("plain text tail: got %q (%v)", got, err)
	}
}
