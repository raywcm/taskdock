// Package protocol defines v1 framing. It never interprets ANSI or terminal input bytes.
package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/raywcm/taskdock/internal/task"
)

const (
	Version           = 1
	MaxPayload        = 64 * 1024
	MaxControlPayload = 8 * 1024
)

type Kind byte

const (
	Hello Kind = iota + 1
	Welcome
	Input
	Output
	Resize
	Detach
	Exit
	Error
	Ping
	Pong
	Stop
	Notice
)

type Frame struct {
	Kind    Kind
	Payload []byte
}

// Header: "MT" (2) | version (1) | kind (1) | payload length, big endian (4).
// Validate length before allocating, including when reading from an untrusted socket.
func Read(r io.Reader) (Frame, error) {
	var h [8]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return Frame{}, err
	}
	if h[0] != 'M' || h[1] != 'T' || h[2] != Version {
		return Frame{}, errors.New("unsupported protocol header")
	}
	kind, size := Kind(h[3]), binary.BigEndian.Uint32(h[4:])
	if err := validate(kind, uint64(size)); err != nil {
		return Frame{}, err
	}
	f := Frame{Kind: kind, Payload: make([]byte, int(size))}
	_, err := io.ReadFull(r, f.Payload)
	return f, err
}

// Encoder serializes output and lifecycle frames onto one connection. After any
// write error, the caller must close the connection; a partial frame cannot be retried.
type Encoder struct {
	mu sync.Mutex
	w  io.Writer
}

func NewEncoder(w io.Writer) *Encoder { return &Encoder{w: w} }

func (e *Encoder) Write(f Frame) error {
	if err := validate(f.Kind, uint64(len(f.Payload))); err != nil {
		return err
	}
	var h [8]byte
	h[0], h[1], h[2], h[3] = 'M', 'T', Version, byte(f.Kind)
	binary.BigEndian.PutUint32(h[4:], uint32(len(f.Payload)))
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := writeAll(e.w, h[:]); err != nil {
		return err
	}
	return writeAll(e.w, f.Payload)
}

func validate(kind Kind, size uint64) error {
	if kind < Hello || kind > Notice {
		return fmt.Errorf("unknown frame kind: %d", kind)
	}
	limit := uint64(MaxControlPayload)
	if kind == Input || kind == Output {
		limit = MaxPayload
	}
	if size > limit {
		return fmt.Errorf("frame exceeds payload limit %d", limit)
	}
	return nil
}

func writeAll(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if n < 0 || n > len(p) {
			return io.ErrShortWrite
		}
		p = p[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

// Control payloads are UTF-8 JSON. Input and Output payloads are opaque bytes.
type HelloMessage struct {
	Preview      bool       `json:"preview,omitempty"`
	TaskID       task.ID    `json:"task_id"`
	InstanceID   task.ID    `json:"instance_id"`
	Mode         string     `json:"mode"` // "attach" or "control"
	Size         *task.Size `json:"size,omitempty"`
	AttachMode   string     `json:"attach_mode,omitempty"`
	Capabilities []string   `json:"capabilities,omitempty"`
}

type WelcomeMessage struct {
	TaskID       task.ID    `json:"task_id"`
	InstanceID   task.ID    `json:"instance_id"`
	Replay       string     `json:"replay"` // "none", or "initial" for a new foreground task
	AttachMode   string     `json:"attach_mode,omitempty"`
	SizeOwner    bool       `json:"size_owner,omitempty"`
	Size         *task.Size `json:"size,omitempty"`
	Capabilities []string   `json:"capabilities,omitempty"`
}

const (
	AttachModesCapability     = "attach-modes-v1"
	OutputByteQueueCapability = "output-byte-queue-v1"
	AttachExclusive           = "exclusive"
	AttachTakeover            = "takeover"
	AttachReadOnly            = "readonly"
	AttachShared              = "shared"
)

func ValidAttachMode(mode string) bool {
	return mode == AttachExclusive || mode == AttachTakeover || mode == AttachReadOnly || mode == AttachShared
}

type NoticeMessage struct {
	Type      string     `json:"type"` // "role" or "refresh"
	SizeOwner bool       `json:"size_owner,omitempty"`
	Size      *task.Size `json:"size,omitempty"`
}

type ErrorMessage struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ExitMessage struct {
	Code   int    `json:"code"`
	Signal string `json:"signal,omitempty"`
}
