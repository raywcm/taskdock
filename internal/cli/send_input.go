package cli

import (
	"errors"
	"io"
	"slices"

	"github.com/raywcm/taskdock/internal/ipc"
	"github.com/raywcm/taskdock/internal/protocol"
	"github.com/raywcm/taskdock/internal/service"
	"github.com/spf13/cobra"
)

func sendInputCommand(open func() (*service.Manager, error)) *cobra.Command {
	var pid int
	var paste, enter, idle bool
	command := &cobra.Command{Use: "send-input SESSION", Short: "Send input to a verified foreground program", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if pid <= 0 {
			return errors.New("--pid is required")
		}
		manager, err := open()
		if err != nil {
			return err
		}
		record, err := manager.Resolve(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if record.Runtime == nil {
			return errors.New("session is not running")
		}
		data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 4<<20+1))
		if err != nil {
			return err
		}
		if len(data) > 4<<20 {
			return errors.New("input exceeds 4 MiB")
		}
		session, err := ipc.Connect(cmd.Context(), manager.Store.Root(), *record.Runtime, "control", nil)
		if err != nil {
			return err
		}
		defer session.Close()
		if !slices.Contains(session.Welcome.Capabilities, protocol.GuardedInputCapability) {
			return errors.New("this broker requires an update for guarded input")
		}
		if idle && !slices.Contains(session.Welcome.Capabilities, protocol.IdleInputCapability) {
			return errors.New("this broker requires an update for idle input")
		}
		write := func(data []byte) error {
			if err := session.Send(ipc.JSON(protocol.GuardedInput, protocol.GuardedInputMessage{PID: pid, Data: data, Idle: idle})); err != nil {
				return err
			}
			for {
				frame, err := protocol.Read(session.Conn)
				if err != nil {
					return err
				}
				switch frame.Kind {
				case protocol.Pong:
					return nil
				case protocol.Error:
					return ipc.FrameError(frame)
				case protocol.Exit:
					return errors.New("native program exited")
				}
			}
		}
		if paste {
			data = append(append([]byte("\x1b[200~"), data...), []byte("\x1b[201~")...)
		}
		if idle {
			if enter {
				data = append(data, '\r')
			}
			if len(data) > 48000 {
				return errors.New("input exceeds atomic idle input limit of 48000 bytes")
			}
			return write(data)
		}
		for len(data) > 0 {
			count := min(4000, len(data))
			if err := write(data[:count]); err != nil {
				return err
			}
			data = data[count:]
		}
		if enter {
			return write([]byte("\r"))
		}
		return nil
	}}
	command.Flags().IntVar(&pid, "pid", 0, "Expected foreground program PID")
	command.Flags().BoolVar(&paste, "paste", false, "Use bracketed paste")
	command.Flags().BoolVar(&enter, "enter", false, "Submit after pasting")
	command.Flags().BoolVar(&idle, "idle", false, "Submit atomically only when no input draft is pending")
	return command
}
