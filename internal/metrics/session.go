package metrics

import (
	"context"

	"github.com/raywcm/taskdock/internal/task"
)

// SessionForProcess also recognizes shells started by brokers from before the
// TDOCK_SESSION marker existed. Snapshot ancestry is used only for this UI guard.
func SessionForProcess(ctx context.Context, records []task.Record, pid int) task.ID {
	active := false
	for _, record := range records {
		if record.Runtime != nil && record.Runtime.State == task.Running {
			active = true
			break
		}
	}
	if !active {
		return ""
	}
	// Inspect only ancestors, never the host's entire process table. Some
	// container /proc mounts make a full resource snapshot expensive.
	snapshot := make(map[int]process)
	for ancestor := pid; ancestor > 1 && len(snapshot) < 128; {
		if _, seen := snapshot[ancestor]; seen {
			break
		}
		p, ok := processAt(ctx, ancestor)
		if !ok {
			break
		}
		snapshot[ancestor] = p
		ancestor = p.parent
	}
	return sessionForProcess(snapshot, records, pid)
}

func sessionForProcess(snapshot map[int]process, records []task.Record, pid int) task.ID {
	roots := make(map[int]task.ID)
	for _, record := range records {
		if record.Runtime == nil || record.Runtime.State != task.Running {
			continue
		}
		root, ok := snapshot[record.Runtime.ChildPID]
		if ok && root.parent == record.Runtime.BrokerPID {
			roots[root.pid] = record.Spec.ID
		}
	}
	seen := make(map[int]bool)
	for pid > 1 && !seen[pid] {
		if id := roots[pid]; id != "" {
			return id
		}
		seen[pid] = true
		p, ok := snapshot[pid]
		if !ok {
			return ""
		}
		pid = p.parent
	}
	return ""
}
