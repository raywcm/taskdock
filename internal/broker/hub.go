// Package broker owns task lifetime and client attachment policy, independently of UI lifetime.
package broker

import (
	"errors"
	"sync"

	"github.com/raywcm/taskdock/internal/protocol"
)

const (
	OutputChunkSize  = 32 * 1024
	outputQueueBytes = 2 * 1024 * 1024
)

var (
	ErrBusy   = errors.New("task already has a controlling client")
	ErrClosed = errors.New("broker output is closed")
)

// Subscription is a lease. A stale disconnect must never evict a replacement.
type Subscription struct {
	OutputReady <-chan struct{}
	Notices     <-chan protocol.NoticeMessage
	Done        <-chan struct{}
	outputMu    sync.Mutex
	ready       chan struct{}
	queue       [][]byte
	queuedBytes int
	notices     chan protocol.NoticeMessage
	done        chan struct{}
	reason      protocol.ErrorMessage // Published before Done closes.
	Mode        string
	Initial     bool
	serial      uint64
	modern      bool
}

func (s *Subscription) Reason() protocol.ErrorMessage { <-s.Done; return s.reason }

func (s *Subscription) signalOutput() {
	select {
	case s.ready <- struct{}{}:
	default:
	}
}

// TakeOutput drains the next chunk, if available. Taking ownership of the
// entire chunk lets the producer continue appending without sharing its bytes.
func (s *Subscription) TakeOutput() []byte {
	s.outputMu.Lock()
	defer s.outputMu.Unlock()
	if len(s.queue) == 0 {
		return nil
	}
	data := s.queue[0]
	s.queue[0] = nil
	s.queuedBytes -= len(data)
	if len(s.queue) == 1 {
		s.queue = s.queue[:0]
	} else {
		s.queue = s.queue[1:]
		s.signalOutput()
	}
	return data
}

func (s *Subscription) enqueue(data []byte) bool {
	s.outputMu.Lock()
	defer s.outputMu.Unlock()
	if len(data) > outputQueueBytes-s.queuedBytes {
		return false
	}
	s.queuedBytes += len(data)
	s.queue = appendOutput(s.queue, data)
	s.signalOutput()
	return true
}

func appendOutput(queue [][]byte, data []byte) [][]byte {
	if last := len(queue) - 1; last >= 0 && len(queue[last]) < OutputChunkSize {
		count := min(len(data), OutputChunkSize-len(queue[last]))
		queue[last] = append(queue[last], data[:count]...)
		data = data[count:]
	}
	for len(data) != 0 {
		count := min(len(data), OutputChunkSize)
		queue = append(queue, append([]byte(nil), data[:count]...))
		data = data[count:]
	}
	return queue
}

type Hub struct {
	mu          sync.Mutex
	members     map[*Subscription]struct{}
	owner       *Subscription
	serial      uint64
	closed      bool
	capture     bool
	pending     [][]byte
	pendingSize int
}

func (h *Hub) CaptureInitial()                { h.mu.Lock(); defer h.mu.Unlock(); h.capture = true }
func (h *Hub) Attach() (*Subscription, error) { return h.AttachMode(protocol.AttachExclusive, false) }
func (h *Hub) AttachMode(mode string, modern bool) (*Subscription, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, ErrClosed
	}
	if !protocol.ValidAttachMode(mode) {
		return nil, errors.New("invalid attachment mode")
	}
	if mode == protocol.AttachExclusive && h.owner != nil {
		return nil, ErrBusy
	}
	if mode == protocol.AttachTakeover {
		for s := range h.members {
			if s.Mode != protocol.AttachReadOnly {
				h.disconnect(s, "taken_over", "session control was taken over by another terminal")
			}
		}
	}
	ready, notices, done := make(chan struct{}, 1), make(chan protocol.NoticeMessage, 8), make(chan struct{})
	h.serial++
	s := &Subscription{
		OutputReady: ready, Notices: notices, Done: done,
		ready: ready, notices: notices, done: done,
		queue: h.pending, queuedBytes: h.pendingSize,
		Mode: mode, serial: h.serial, modern: modern, Initial: h.pendingSize > 0,
	}
	if s.Initial {
		s.signalOutput()
	}
	h.pending = nil
	h.pendingSize = 0
	h.capture = false
	if h.members == nil {
		h.members = make(map[*Subscription]struct{})
	}
	h.members[s] = struct{}{}
	if h.owner == nil && mode != protocol.AttachReadOnly {
		h.owner = s
	}
	h.notifyRoles()
	return s, nil
}
func (h *Hub) CanInput(s *Subscription) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.members[s]
	return ok && s.Mode != protocol.AttachReadOnly
}
func (h *Hub) OwnsSize(s *Subscription) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return s != nil && h.owner == s
}
func (h *Hub) RequestRefresh(s *Subscription) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.owner == nil || h.owner == s || !h.owner.modern {
		return false
	}
	h.notice(h.owner, protocol.NoticeMessage{Type: "refresh"})
	return true
}
func (h *Hub) Detach(s *Subscription) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.members[s]; ok {
		h.disconnect(s, "", "")
		h.notifyRoles()
	}
}

// NotifySize sends the existing role notice with the broker's current size.
// Older clients ignore the additional size field.
func (h *Hub) NotifySize() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notifyRoles()
}

// Output stays opaque. A slow subscriber alone is disconnected; other clients
// receive the complete byte stream, never a stream with missing chunks.
func (h *Hub) Publish(p []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || len(p) == 0 {
		return
	}
	if len(h.members) == 0 && h.capture {
		if len(p) > outputQueueBytes-h.pendingSize {
			h.pending = nil
			h.pendingSize = 0
			h.capture = false
			return
		}
		h.pending = appendOutput(h.pending, p)
		h.pendingSize += len(p)
		return
	}
	changed := false
	for s := range h.members {
		if !s.enqueue(p) {
			h.disconnect(s, "slow_client", "output queue overflow")
			changed = true
		}
	}
	if changed {
		h.notifyRoles()
	}
}
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for s := range h.members {
		h.disconnect(s, "", "")
	}
}
func (h *Hub) disconnect(s *Subscription, code, message string) {
	delete(h.members, s)
	s.reason = protocol.ErrorMessage{Code: code, Message: message}
	close(s.done)
	if h.owner == s {
		h.owner = nil
		for next := range h.members {
			if next.Mode != protocol.AttachReadOnly && (h.owner == nil || next.serial < h.owner.serial) {
				h.owner = next
			}
		}
	}
}
func (h *Hub) notifyRoles() {
	for s := range h.members {
		if s.modern {
			h.notice(s, protocol.NoticeMessage{Type: "role", SizeOwner: h.owner == s})
		}
	}
}

// Roles are snapshots, unlike output bytes. Coalesce superseded notices during
// bursts of joins/resizes instead of evicting a healthy controller. Retain one
// refresh request so a native viewer still receives a complete redraw.
func (h *Hub) notice(s *Subscription, notice protocol.NoticeMessage) {
	select {
	case s.notices <- notice:
		return
	default:
	}
	refresh := notice.Type == "refresh"
	for {
		select {
		case previous := <-s.notices:
			refresh = refresh || previous.Type == "refresh"
		default:
			s.notices <- protocol.NoticeMessage{Type: "role", SizeOwner: h.owner == s}
			if refresh {
				s.notices <- protocol.NoticeMessage{Type: "refresh"}
			}
			return
		}
	}
}
