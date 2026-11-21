// Package metrics samples read-only per-user process snapshots. It never signals
// processes or participates in the PTY data path.
package metrics

import (
	"context"
	"github.com/raywcm/taskdock/internal/task"
	"math"
	"sync"
	"time"
)

type process struct {
	pid, parent, group, foreground int
	command                        string
	identity                       string
	cpu                            float64
	memory                         uint64
}
type Sampler struct {
	mu       sync.Mutex
	previous map[int]process
	at       time.Time
}

func New() *Sampler { return &Sampler{} }

func (s *Sampler) Sample(ctx context.Context, records []task.Record) map[task.ID]task.Resources {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make(map[task.ID]task.Resources)
	active := false
	for _, r := range records {
		if r.Runtime != nil && r.Health == "online" {
			active = true
			break
		}
	}
	if !active {
		s.previous = nil
		s.at = time.Time{}
		return result
	}
	snapshot, err := processes(ctx)
	if err != nil {
		return result
	}
	now := time.Now()
	elapsed := now.Sub(s.at).Seconds()
	valid := !s.at.IsZero() && elapsed >= .1 && elapsed <= 15
	children := map[int][]int{}
	for pid, p := range snapshot {
		children[p.parent] = append(children[p.parent], pid)
	}
	for _, r := range records {
		if r.Runtime == nil || r.Health != "online" {
			continue
		}
		root, ok := snapshot[r.Runtime.ChildPID]
		if !ok || root.parent != r.Runtime.BrokerPID {
			continue
		}
		oldRoot, knownRoot := s.previous[root.pid]
		sample := task.Resources{SampledAt: now, CPUAvailable: valid && knownRoot && oldRoot.identity == root.identity}
		// The pane's controlling terminal names its foreground process group.
		// Prefer the group leader; background jobs and their children never name the window.
		sample.ForegroundCommand = root.command
		seen := map[int]bool{}
		pending := []int{root.pid}
		for len(pending) > 0 {
			pid := pending[len(pending)-1]
			pending = pending[:len(pending)-1]
			if seen[pid] {
				continue
			}
			seen[pid] = true
			p, ok := snapshot[pid]
			if !ok {
				continue
			}
			if p.pid == root.foreground && p.group == root.foreground {
				sample.ForegroundCommand = p.command
			}
			sample.ProcessCount++
			sample.MemoryBytes += p.memory
			if old, ok := s.previous[pid]; valid && ok && old.identity == p.identity && p.cpu >= old.cpu {
				sample.CPUPercent += (p.cpu - old.cpu) / elapsed * 100
			}
			pending = append(pending, children[pid]...)
		}
		if math.IsNaN(sample.CPUPercent) || math.IsInf(sample.CPUPercent, 0) {
			sample.CPUAvailable = false
			sample.CPUPercent = 0
		}
		result[r.Spec.ID] = sample
	}
	s.previous = snapshot
	s.at = now
	return result
}
