package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmptyJSONCatalog(t *testing.T) {
	root := NewRoot()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetArgs([]string{"--state-dir", filepath.Join(t.TempDir(), "state"), "ls", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if output.String() != "[]\n" {
		t.Fatalf("JSON output: %q", output.String())
	}
}

func TestMissingTaskArgument(t *testing.T) {
	root := NewRoot()
	root.SetArgs([]string{"--state-dir", filepath.Join(t.TempDir(), "state"), "attach"})
	if err := root.Execute(); err == nil {
		t.Fatal("missing task argument accepted")
	}
}

func TestConnectionFlagsAreMutuallyExclusive(t *testing.T) {
	root := NewRoot()
	root.SetArgs([]string{"--state-dir", filepath.Join(t.TempDir(), "state"), "attach", "test", "--read-only", "--shared"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "were all set") {
		t.Fatalf("conflicting modes accepted: %v", err)
	}
}

func TestNestedInteractiveCommandsAreRejectedButManagementIsAllowed(t *testing.T) {
	t.Setenv("TDOCK_SESSION", "77f1540e63fbc1cf04cc0d1eaf90040c")
	for _, args := range [][]string{nil, {"new", "-d"}, {"attach", "test"}} {
		root := NewRoot()
		root.SetArgs(append([]string{"--state-dir", filepath.Join(t.TempDir(), "state")}, args...))
		if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "already inside") {
			t.Fatalf("nested %v accepted: %v", args, err)
		}
	}
	root := NewRoot()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetArgs([]string{"--state-dir", filepath.Join(t.TempDir(), "state"), "ls", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatal("nested management rejected", err)
	}
}
