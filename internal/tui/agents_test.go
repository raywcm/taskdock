package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestOptionalAgentsTab(t *testing.T) {
	backend := &fakeBackend{}
	plain := newModel(context.Background(), backend, nil, Options{})
	if plain.tabTotal() != 3 || strings.Contains(plain.tabs(120), "Agents") {
		t.Fatal("component appeared without agentctl")
	}
	enabled := newModel(context.Background(), backend, nil, Options{AgentctlPath: "/test/agentctl"})
	if enabled.tabTotal() != 4 || !strings.Contains(enabled.tabs(120), "Agents") {
		t.Fatal("missing optional component")
	}
	enabled.selectTab(tabSettings)
	next, _ := enabled.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	result := next.(model)
	if result.tab != tabAgents || result.selected.Kind != "" || result.quitting {
		t.Fatal("tab did not remain in the TaskDock preview")
	}
}

func TestAgentsResponsiveGeometry(t *testing.T) {
	for _, size := range [][2]int{{160, 40}, {80, 18}, {40, 10}, {26, 9}, {20, 6}, {12, 5}} {
		m := newModel(context.Background(), &fakeBackend{}, nil, Options{AgentctlPath: "/test/agentctl"})
		m.tab = tabAgents
		m.width, m.height = size[0], size[1]
		lines := strings.Split(m.View().Content, "\n")
		if len(lines) > m.height {
			t.Fatalf("%v: height %d", size, len(lines))
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > m.width {
				t.Fatalf("%v: overflowing line %q", size, line)
			}
		}
		if got := int(m.agentsSize().Cols); got != m.width-4 {
			t.Fatalf("%v: PTY width %d", size, got)
		}
		if got := int(m.agentsSize().Rows); got != m.height-m.agentsTop()-1 {
			t.Fatalf("%v: PTY height %d", size, got)
		}
	}
}
