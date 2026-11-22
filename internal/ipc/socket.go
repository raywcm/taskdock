// Package ipc implements local authenticated control and attachment connections.
package ipc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"github.com/raywcm/taskdock/internal/protocol"
	"github.com/raywcm/taskdock/internal/task"
)

const (
	Timeout      = 2 * time.Second
	writeTimeout = 10 * time.Second
)

type RemoteError struct{ Code, Message string }

func (e *RemoteError) Error() string { return e.Code + ": " + e.Message }

func PrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return checkPrivateDir(dir)
}

func checkPrivateDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm()&0077 != 0 || int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("unsafe private directory: %s", dir)
	}
	return nil
}

func SocketPath(state string, id task.ID) (string, error) {
	preferred, err := socketPath(state, id, "tdock", true)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(preferred); err == nil {
		return preferred, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	// Only look in existing private legacy directories. Never create the old
	// namespace for a fresh installation, and never follow a legacy symlink.
	legacy, err := socketPath(state, id, "mtask", false)
	if err == nil {
		if info, err := os.Lstat(legacy); err == nil {
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0077 != 0 || int(stat.Uid) != os.Getuid() {
				return "", errors.New("unsafe legacy socket")
			}
			return legacy, nil
		}
	}
	return preferred, nil
}

func socketPath(state string, id task.ID, namespace string, create bool) (string, error) {
	if err := id.Validate(); err != nil {
		return "", err
	}
	private := checkPrivateDir
	if create {
		private = PrivateDir
	}
	parent := os.Getenv("XDG_RUNTIME_DIR")
	if parent != "" {
		if !filepath.IsAbs(parent) {
			return "", errors.New("XDG_RUNTIME_DIR must be absolute")
		}
		if err := private(parent); err != nil {
			return "", err
		}
		parent = filepath.Join(parent, namespace)
	} else {
		parent = fmt.Sprintf("/tmp/%s-%d", namespace, os.Getuid())
	}
	if err := private(parent); err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(filepath.Clean(state)))
	dir := filepath.Join(parent, fmt.Sprintf("%x", hash[:6]))
	if err := private(dir); err != nil {
		return "", err
	}
	name := filepath.Join(dir, string(id)+".sock")
	if len(name) > 100 {
		return "", errors.New("Unix socket path exceeds portable 100-byte limit; shorten XDG_RUNTIME_DIR")
	}
	return name, nil
}

func Authenticate(conn *net.UnixConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var uid uint32
	var peerErr error
	err = raw.Control(func(fd uintptr) { uid, peerErr = peerUID(int(fd)) })
	if err != nil {
		return err
	}
	if peerErr != nil {
		return peerErr
	}
	if int(uid) != os.Getuid() {
		return errors.New("Unix peer belongs to another user")
	}
	return nil
}

func JSON(kind protocol.Kind, value any) protocol.Frame {
	b, _ := json.Marshal(value)
	return protocol.Frame{Kind: kind, Payload: b}
}
func Decode(data []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	if err := d.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("expected one JSON value")
	}
	return nil
}

type Session struct {
	Conn    *net.UnixConn
	Encoder *protocol.Encoder
	Welcome protocol.WelcomeMessage
}

func (s *Session) Send(frame protocol.Frame) error {
	_ = s.Conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	return s.Encoder.Write(frame)
}
func (s *Session) Close() error { return s.Conn.Close() }

func Connect(ctx context.Context, state string, r task.Runtime, mode string, size *task.Size) (*Session, error) {
	return connect(ctx, state, r, mode, size, "")
}

// ConnectAttachment probes capabilities before requesting a non-default mode.
// Older v1 brokers ignore unknown JSON fields; sending readonly blindly would
// therefore accidentally grant input control, and takeover could be ignored.
func ConnectAttachment(ctx context.Context, state string, r task.Runtime, size *task.Size, mode string) (*Session, error) {
	return connectAttachment(ctx, state, r, size, mode, false)
}

func ConnectMonitor(ctx context.Context, state string, r task.Runtime, size *task.Size) (*Session, error) {
	return connectAttachment(ctx, state, r, size, "", true)
}

func connectAttachment(ctx context.Context, state string, r task.Runtime, size *task.Size, mode string, preview bool) (*Session, error) {
	automatic := mode == ""
	if !automatic && !protocol.ValidAttachMode(mode) {
		return nil, errors.New("invalid attachment mode")
	}
	if mode != protocol.AttachExclusive {
		probe, err := Connect(ctx, state, r, "control", nil)
		if err != nil {
			return nil, err
		}
		supported := slices.Contains(probe.Welcome.Capabilities, protocol.AttachModesCapability)
		_ = probe.Close()
		if automatic {
			mode = protocol.AttachExclusive
			if supported {
				mode = protocol.AttachShared
			}
		} else if !supported {
			return nil, &RemoteError{Code: "unsupported_attach_mode", Message: "this broker predates connection modes; existing tasks keep running, new tasks support the new modes"}
		}
	}
	s, err := connect(ctx, state, r, "attach", size, mode, preview)
	if err != nil {
		return nil, err
	}
	if mode != protocol.AttachExclusive && (s.Welcome.AttachMode != mode || !slices.Contains(s.Welcome.Capabilities, protocol.AttachModesCapability)) {
		_ = s.Close()
		return nil, errors.New("broker did not acknowledge attachment mode")
	}
	return s, nil
}

func connect(ctx context.Context, state string, r task.Runtime, mode string, size *task.Size, attachMode string, preview ...bool) (*Session, error) {
	name, err := SocketPath(state, r.TaskID)
	if err != nil {
		return nil, err
	}
	conn, err := (&net.Dialer{Timeout: Timeout}).DialContext(ctx, "unix", name)
	if err != nil {
		return nil, err
	}
	u := conn.(*net.UnixConn)
	fail := func(err error) (*Session, error) { u.Close(); return nil, err }
	cancel := context.AfterFunc(ctx, func() { u.Close() })
	defer cancel()
	if err := Authenticate(u); err != nil {
		return fail(err)
	}
	_ = u.SetDeadline(time.Now().Add(Timeout))
	s := &Session{Conn: u, Encoder: protocol.NewEncoder(u)}
	hello := protocol.HelloMessage{TaskID: r.TaskID, InstanceID: r.InstanceID, Mode: mode, Size: size}
	hello.Preview = len(preview) > 0 && preview[0]
	if mode == "attach" && attachMode != "" {
		hello.AttachMode = attachMode
		hello.Capabilities = []string{protocol.AttachModesCapability}
	}
	if err := s.Send(JSON(protocol.Hello, hello)); err != nil {
		return fail(err)
	}
	frame, err := protocol.Read(u)
	if err != nil {
		return fail(err)
	}
	if frame.Kind == protocol.Error {
		return fail(FrameError(frame))
	}
	if frame.Kind != protocol.Welcome {
		return fail(errors.New("expected broker welcome"))
	}
	var welcome protocol.WelcomeMessage
	if err := Decode(frame.Payload, &welcome); err != nil {
		return fail(err)
	}
	if welcome.TaskID != r.TaskID || welcome.InstanceID != r.InstanceID {
		return fail(errors.New("broker identity mismatch"))
	}
	s.Welcome = welcome
	_ = u.SetDeadline(time.Time{})
	return s, nil
}
func FrameError(frame protocol.Frame) error {
	var message protocol.ErrorMessage
	if err := Decode(frame.Payload, &message); err != nil {
		return err
	}
	return &RemoteError{Code: message.Code, Message: message.Message}
}

func Query(ctx context.Context, state string, r task.Runtime, stop bool) (task.Runtime, error) {
	s, err := Connect(ctx, state, r, "control", nil)
	if err != nil {
		return task.Runtime{}, err
	}
	defer s.Close()
	cancel := context.AfterFunc(ctx, func() { s.Close() })
	defer cancel()
	kind := protocol.Ping
	if stop {
		kind = protocol.Stop
	}
	if err := s.Send(protocol.Frame{Kind: kind}); err != nil {
		return task.Runtime{}, err
	}
	_ = s.Conn.SetReadDeadline(time.Now().Add(Timeout))
	f, err := protocol.Read(s.Conn)
	if err != nil {
		return task.Runtime{}, err
	}
	if f.Kind == protocol.Error {
		return task.Runtime{}, FrameError(f)
	}
	if f.Kind != protocol.Pong {
		return task.Runtime{}, errors.New("expected broker state")
	}
	var result task.Runtime
	if err := Decode(f.Payload, &result); err != nil {
		return result, err
	}
	if err := result.Validate(); err != nil {
		return result, err
	}
	if result.TaskID != r.TaskID || result.InstanceID != r.InstanceID {
		return result, errors.New("invalid broker state identity")
	}
	return result, nil
}
