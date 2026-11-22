package cli

import (
	"github.com/raywcm/taskdock/internal/service"
	"testing"
)

func TestManagedAgentsHandoff(t *testing.T) {
	for _, test := range []struct {
		argv    []string
		allowed bool
	}{
		{[]string{"/bin/agentctl", "tui"}, true},
		{[]string{"/bin/agentctl", "--tdock-state-dir", "/private/test", "tui"}, true},
		{[]string{"/bin/bash"}, false},
		{[]string{"/bin/agentctl", "status"}, false},
		{[]string{"env", "agentctl", "tui"}, false},
	} {
		if service.ManagedAgentsUI(test.argv) != test.allowed {
			t.Fatalf("handoff mismatch: %v", test.argv)
		}
	}
}
