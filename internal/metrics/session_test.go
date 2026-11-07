package metrics

import (
	"testing"

	"github.com/raywcm/taskdock/internal/task"
)

func TestSessionAncestryRequiresMatchingBrokerAndChild(t *testing.T) {
	records := []task.Record{{Spec: task.Spec{ID: "session"}, Runtime: &task.Runtime{State: task.Running, BrokerPID: 100, ChildPID: 101}}}
	snapshot := map[int]process{101: {pid: 101, parent: 100}, 102: {pid: 102, parent: 101}, 103: {pid: 103, parent: 102}}
	if got := sessionForProcess(snapshot, records, 103); got != "session" {
		t.Fatal(got)
	}
	snapshot[101] = process{pid: 101, parent: 99}
	if got := sessionForProcess(snapshot, records, 103); got != "" {
		t.Fatal("reused PID matched", got)
	}
}
