package broker

import (
	"bytes"
	"errors"
	"sync"
	"testing"

	"github.com/raywcm/taskdock/internal/protocol"
)

func TestDisconnectAndReattach(t *testing.T) {
	var hub Hub
	first, err := hub.Attach()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hub.Attach(); !errors.Is(err, ErrBusy) {
		t.Fatalf("second controller: %v", err)
	}
	hub.Detach(first)
	hub.Publish([]byte("discarded while disconnected"))
	second, err := hub.Attach()
	if err != nil {
		t.Fatal(err)
	}
	hub.Detach(first) // old connection cleanup must not evict the replacement
	payload := []byte("\x1b[31mhello\x00\xff")
	want := bytes.Clone(payload)
	hub.Publish(payload)
	payload[0] = 'X'
	if got := second.TakeOutput(); !bytes.Equal(got, want) {
		t.Fatalf("output changed: %q", got)
	}
	hub.Close()
	if _, err := hub.Attach(); !errors.Is(err, ErrClosed) {
		t.Fatalf("attach after close: %v", err)
	}
}

func TestSlowClientDoesNotBlockPublish(t *testing.T) {
	var hub Hub
	sub, _ := hub.Attach()
	data := bytes.Repeat([]byte{'x'}, outputQueueBytes+1)
	hub.Publish(data)
	select {
	case <-sub.Done:
	default:
		t.Fatal("slow client not disconnected")
	}
	if _, err := hub.Attach(); err != nil {
		t.Fatalf("reattach: %v", err)
	}
}

func TestConcurrentPublishDetachAndClose(t *testing.T) {
	var hub Hub
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			for range 100 {
				sub, err := hub.Attach()
				hub.Publish([]byte("output"))
				if err == nil {
					hub.Detach(sub)
				}
			}
		})
	}
	wg.Go(hub.Close)
	wg.Wait()
}

func TestInitialCaptureCoalescesAndNeverReplaysOnReconnect(t *testing.T) {
	var hub Hub
	hub.CaptureInitial()
	for range 10000 {
		hub.Publish([]byte("p"))
	}
	first, err := hub.Attach()
	if err != nil {
		t.Fatal(err)
	}
	if !first.Initial {
		t.Fatal("missing startup buffer")
	}
	if got := first.TakeOutput(); len(got) != 10000 {
		t.Fatalf("startup bytes: %d", len(got))
	}
	hub.Detach(first)
	hub.Publish([]byte("offline"))
	second, err := hub.Attach()
	if err != nil {
		t.Fatal(err)
	}
	if second.Initial {
		t.Fatal("replayed after reconnect")
	}
	select {
	case <-second.OutputReady:
		t.Fatal("unexpected history")
	default:
	}
	hub.Close()
}

func TestTakeoverPreservesReadersAndRevokesAllWriters(t *testing.T) {
	var h Hub
	first, _ := h.AttachMode(protocol.AttachExclusive, false)
	reader, _ := h.AttachMode(protocol.AttachReadOnly, false)
	shared, _ := h.AttachMode(protocol.AttachShared, false)
	if !h.CanInput(first) || !h.CanInput(shared) || h.CanInput(reader) || !h.OwnsSize(first) || h.OwnsSize(shared) {
		t.Fatal("incorrect initial permissions")
	}
	replacement, err := h.AttachMode(protocol.AttachTakeover, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, old := range []*Subscription{first, shared} {
		if h.CanInput(old) || old.Reason().Code != "taken_over" {
			t.Fatal("old controller was not revoked")
		}
		h.Detach(old)
	}
	if !h.CanInput(replacement) || !h.OwnsSize(replacement) {
		t.Fatal("stale cleanup revoked the replacement")
	}
	payload := []byte("\x1b[31m共同查看\x00\xff")
	h.Publish(payload)
	for _, s := range []*Subscription{reader, replacement} {
		if got := s.TakeOutput(); !bytes.Equal(got, payload) {
			t.Fatalf("fanout changed bytes: %q", got)
		}
	}
	h.Close()
}

func TestSizeOwnerPromotesOldestWriterWithoutPromotingReaders(t *testing.T) {
	var h Hub
	reader, _ := h.AttachMode(protocol.AttachReadOnly, false)
	first, _ := h.AttachMode(protocol.AttachShared, false)
	second, _ := h.AttachMode(protocol.AttachShared, false)
	third, _ := h.AttachMode(protocol.AttachShared, false)
	h.Detach(first)
	if !h.OwnsSize(second) || h.OwnsSize(third) || h.OwnsSize(reader) {
		t.Fatal("size owner did not advance in join order")
	}
	h.Detach(second)
	h.Detach(third)
	if h.OwnsSize(reader) || h.CanInput(reader) {
		t.Fatal("reader silently became a controller")
	}
	next, err := h.Attach()
	if err != nil || !h.OwnsSize(next) {
		t.Fatal("reader prevented an exclusive controller from connecting")
	}
	h.Close()
}

func TestSlowReaderDoesNotEvictHealthyWriter(t *testing.T) {
	var h Hub
	writer, _ := h.Attach()
	reader, _ := h.AttachMode(protocol.AttachReadOnly, false)
	data := bytes.Repeat([]byte{'x'}, OutputChunkSize)
	for range outputQueueBytes/OutputChunkSize + 1 {
		h.Publish(data)
		if got := writer.TakeOutput(); !bytes.Equal(got, data) {
			t.Fatal("writer lost output")
		}
	}
	select {
	case <-reader.Done:
		if reader.Reason().Code != "slow_client" || !h.CanInput(writer) {
			t.Fatal("a slow observer affected the writer")
		}
	default:
		t.Fatal("observer exceeded its byte budget without disconnecting")
	}
	h.Close()
}

func TestShortOutputBurstStaysAttachedAndPreservesBytes(t *testing.T) {
	var h Hub
	sub, err := h.Attach()
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	line := []byte("\x1b[34m目录-0123456789012345678901234567890123456789\x1b[0m\r\n")
	for range 700 {
		// Keep the viewer paused while a directory listing arrives as short
		// reads. The previous frame-count cap disconnected it at 64 reads.
		h.Publish(line[:8])
		h.Publish(line[8:])
	}
	if !h.CanInput(sub) {
		t.Fatal("attached viewer was dropped below the byte budget")
	}
	want := bytes.Repeat(line, 700)
	var got []byte
	for len(got) < len(want) {
		select {
		case <-sub.OutputReady:
			chunk := sub.TakeOutput()
			if len(chunk) == 0 || len(chunk) > OutputChunkSize {
				t.Fatalf("invalid output chunk size: %d", len(chunk))
			}
			got = append(got, chunk...)
		default:
			t.Fatal("queued bytes failed to signal readiness")
		}
	}
	if !bytes.Equal(got, want) {
		t.Fatal("short reads were reordered or changed")
	}
}

func TestConcurrentTakeoverPublishAndStaleInputChecks(t *testing.T) {
	var h Hub
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 100 {
				s, err := h.AttachMode(protocol.AttachTakeover, false)
				if err == nil {
					h.Publish([]byte("frame"))
					_ = h.CanInput(s)
					h.Detach(s)
				}
			}
		})
	}
	wg.Wait()
	h.Close()
}

func TestJoinAndResizeBurstsDoNotEvictControllers(t *testing.T) {
	var h Hub
	var members []*Subscription
	for range 24 {
		s, err := h.AttachMode(protocol.AttachShared, true)
		if err != nil {
			t.Fatal(err)
		}
		members = append(members, s)
	}
	for range 100 {
		h.NotifySize()
		h.RequestRefresh(members[1])
	}
	for i, s := range members {
		if !h.CanInput(s) || h.OwnsSize(s) != (i == 0) {
			t.Fatal("notice burst displaced a healthy writer")
		}
	}
	h.Detach(members[0])
	if !h.OwnsSize(members[1]) {
		t.Fatal("burst broke ownership promotion")
	}
	h.Close()
}
