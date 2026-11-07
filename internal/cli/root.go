package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/raywcm/taskdock/internal/config"
	"github.com/raywcm/taskdock/internal/daemon"
	historycleanup "github.com/raywcm/taskdock/internal/history"
	"github.com/raywcm/taskdock/internal/i18n"
	"github.com/raywcm/taskdock/internal/ipc"
	"github.com/raywcm/taskdock/internal/metrics"
	"github.com/raywcm/taskdock/internal/protocol"
	"github.com/raywcm/taskdock/internal/service"
	"github.com/raywcm/taskdock/internal/task"
	"github.com/raywcm/taskdock/internal/terminal"
	"github.com/raywcm/taskdock/internal/tui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var Version = "0.8.1-dev"

type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("task exited with code %d", e.Code) }

func NewRoot() *cobra.Command {
	var stateDir string
	var noStatus bool
	openStore := func() (*service.Manager, error) {
		dir, err := config.StateDir(stateDir)
		if err != nil {
			return nil, err
		}
		return service.New(dir)
	}
	open := func() (*service.Manager, error) {
		manager, err := openStore()
		if err != nil {
			return nil, err
		}
		if err := manager.EnableHistory(); err != nil {
			return nil, fmt.Errorf("history cleanup: %w", err)
		}
		return manager, nil
	}
	var root *cobra.Command
	root = &cobra.Command{Use: "tdock", Short: "Independent terminal task manager", Version: Version, Args: cobra.NoArgs, SilenceUsage: true, SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			input, ok := cmd.InOrStdin().(*os.File)
			if !ok || !term.IsTerminal(int(input.Fd())) {
				return errors.New("TUI requires a terminal; use tdock ls --json")
			}
			output, ok := cmd.OutOrStdout().(*os.File)
			if !ok || !term.IsTerminal(int(output.Fd())) {
				return errors.New("TUI requires a terminal; use tdock ls --json")
			}
			manager, err := open()
			if err != nil {
				return err
			}
			var notice error
			var viewState tui.State
			var conflict *task.Spec
			var nameConflict *service.NameConflictError
			for {
				action, err := tui.Run(cmd.Context(), manager, input, output, notice, tui.Options{Version: Version, State: viewState, Conflict: conflict, NameConflict: nameConflict})
				notice = nil
				conflict = nil
				nameConflict = nil
				if err != nil {
					return err
				}
				if action.Kind == "" {
					return nil
				}
				viewState = action.State
				switch action.Kind {
				case "attach":
					r, err := manager.Resolve(cmd.Context(), string(action.ID))
					if err != nil {
						notice = err
						continue
					}
					if r.Runtime == nil {
						notice = errors.New("task has not started")
						continue
					}
					_, err = terminal.AttachWithOptions(cmd.Context(), manager.Store.Root(), r, input, output, terminal.AttachOptions{Mode: action.Mode, NoStatus: noStatus})
					if err != nil {
						notice = fmt.Errorf("attach: %w", err)
						var remote *ipc.RemoteError
						if errors.As(err, &remote) && remote.Code == "busy" {
							spec := r.Spec
							conflict = &spec
						}
					}
				case "new":
					shell := defaultShell()
					name := action.Name
					r, err := manager.Create(cmd.Context(), name, []string{shell}, workingDir(), terminal.CurrentSize(input), true)
					if err != nil {
						notice = err
						var collision *service.NameConflictError
						if errors.As(err, &collision) {
							nameConflict = collision
						}
						continue
					}
					if r.Runtime != nil {
						viewState = tui.State{Tab: "running", Selected: r.Spec.ID, Monitor: true}
					}
				}
				if cmd.Context().Err() != nil {
					return cmd.Context().Err()
				}
			}
		}}
	root.SetIn(os.Stdin)
	root.SetOut(os.Stdout)
	root.SetErr(os.Stderr)
	root.SetVersionTemplate("TaskDock v{{.Version}}\n")
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		switch cmd.Name() {
		case "tdock", "new-session", "attach-session":
		default:
			return nil
		}
		id := task.ID(os.Getenv("TDOCK_SESSION"))
		if id.Validate() != nil {
			// Old sessions lack the marker. Check the current process ancestry
			// against this catalog without connecting to or resizing any task.
			manager, err := openStore()
			if err != nil {
				return err
			}
			records, err := manager.Store.List(cmd.Context())
			if err != nil {
				return err
			}
			id = metrics.SessionForProcess(cmd.Context(), records, os.Getpid())
		}
		if id != "" {
			return errors.New("already inside a TaskDock session; press F12 or Ctrl+B, d to return to the list")
		}
		return nil
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.PersistentFlags().BoolVar(&noStatus, "no-status", false, "use native terminal forwarding without a status row")
	root.PersistentFlags().StringVar(&stateDir, "state-dir", "", "absolute state directory (or TDOCK_STATE_DIR)")
	var listJSON, history bool
	list := &cobra.Command{Use: "list-sessions", Aliases: []string{"ls"}, Short: "List tasks and live broker health", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		m, err := open()
		if err != nil {
			return err
		}
		records, err := m.List(cmd.Context())
		if err != nil {
			return err
		}
		if history {
			filtered := make([]task.Record, 0)
			for _, r := range records {
				if r.Health == "finished" {
					filtered = append(filtered, r)
				}
			}
			records = filtered
		}
		if listJSON {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(records)
		}
		preferences, settingsErr := m.Settings()
		if settingsErr != nil {
			preferences = config.Defaults()
		}
		locale := i18n.New(preferences.Language)
		for _, r := range records {
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), service.LabelLocale(r, locale)); err != nil {
				return err
			}
		}
		return nil
	}}
	list.Flags().BoolVar(&listJSON, "json", false, "print JSON")
	list.Flags().BoolVar(&history, "history", false, "only completed tasks")
	root.AddCommand(list)
	var name, dir string
	var detached, newJSON, reuse bool
	create := &cobra.Command{Use: "new-session [flags] [command [args...]]", Aliases: []string{"new"}, Short: "Start a persistent task (default: your shell)", Args: cobra.ArbitraryArgs, RunE: func(cmd *cobra.Command, args []string) error {
		m, err := open()
		if err != nil {
			return err
		}
		if reuse && name == "" {
			return errors.New("-A requires -s or --name")
		}
		if len(args) == 0 {
			args = []string{defaultShell()}
		} else if len(args) == 1 && cmd.ArgsLenAtDash() < 0 {
			args = []string{defaultShell(), "-c", args[0]}
		}
		cwd := dir
		if cwd == "" {
			cwd = workingDir()
		}
		cwd, err = filepath.Abs(cwd)
		if err != nil {
			return err
		}
		size := task.Size{Rows: 24, Cols: 80}
		input, inOK := cmd.InOrStdin().(*os.File)
		output, outOK := cmd.OutOrStdout().(*os.File)
		interactive := inOK && outOK && term.IsTerminal(int(input.Fd())) && term.IsTerminal(int(output.Fd()))
		if interactive {
			size = terminal.CurrentSize(input)
		}
		r, err := m.Create(cmd.Context(), name, args, cwd, size, interactive && !detached && !newJSON)
		if err != nil {
			var collision *service.NameConflictError
			if !reuse || !errors.As(err, &collision) {
				return err
			}
			r, err = m.Resolve(cmd.Context(), name)
			if err != nil {
				return err
			}
			r = m.Observe(cmd.Context(), r)
			if r.Health != "online" || r.Runtime == nil || r.Runtime.State != task.Running {
				return errors.New("session is not running; use another name or inspect its history")
			}
		}
		if newJSON {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(m.Observe(cmd.Context(), r))
		}
		if detached || !interactive {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), r.Spec.ID)
			return err
		}
		if r.Runtime == nil {
			return errors.New("broker did not write runtime state")
		}
		result, err := attachCreated(cmd.Context(), m, r, input, output, noStatus)
		if err == nil && result.ExitCode != 0 {
			return &ExitError{result.ExitCode}
		}
		return err
	}}
	create.Flags().StringVarP(&name, "name", "s", "", "unique task name")
	create.Flags().StringVarP(&dir, "cwd", "c", "", "working directory")
	create.Flags().BoolVarP(&detached, "detach", "d", false, "start in background")
	create.Flags().BoolVar(&newJSON, "json", false, "start in background and print JSON")
	create.Flags().BoolVarP(&reuse, "attach", "A", false, "attach to the named session if it already exists")
	create.Flags().SetInterspersed(false)
	root.AddCommand(create)
	var noEscape, takeover, readOnly, shared bool
	var attachTarget string
	attach := &cobra.Command{Use: "attach-session [SESSION]", Aliases: []string{"attach", "a"}, Short: "Enter a task; F12 detaches", Args: targetArgs(&attachTarget, 0), RunE: func(cmd *cobra.Command, args []string) error {
		m, err := open()
		if err != nil {
			return err
		}
		r, err := m.Resolve(cmd.Context(), targetValue(attachTarget, args))
		if err != nil {
			return err
		}
		if r.Runtime == nil {
			return errors.New("task never started")
		}
		if r.Runtime.State == task.Exited || r.Runtime.State == task.Failed {
			return errors.New("task has ended; use tdock logs")
		}
		input, ok := cmd.InOrStdin().(*os.File)
		if !ok {
			return errors.New("attach requires a terminal")
		}
		output, ok := cmd.OutOrStdout().(*os.File)
		if !ok {
			return errors.New("attach requires a terminal")
		}
		mode := "" // Negotiate shared control, with exclusive fallback for old brokers.
		if takeover {
			mode = protocol.AttachTakeover
		} else if readOnly {
			mode = protocol.AttachReadOnly
		} else if shared {
			mode = protocol.AttachShared
		}
		result, err := terminal.AttachWithOptions(cmd.Context(), m.Store.Root(), r, input, output, terminal.AttachOptions{NoEscape: noEscape, Mode: mode, NoStatus: noStatus})
		if err == nil && result.ExitCode != 0 {
			return &ExitError{result.ExitCode}
		}
		return err
	}}
	attach.Flags().BoolVar(&noEscape, "no-escape", false, "forward every input byte, including detach keys")
	attach.Flags().BoolVar(&takeover, "takeover", false, "disconnect existing controllers and take control")
	attach.Flags().BoolVarP(&readOnly, "read-only", "r", false, "watch output without sending input or changing terminal size")
	attach.Flags().BoolVar(&shared, "shared", false, "join the session with shared input control")
	attach.MarkFlagsMutuallyExclusive("takeover", "read-only", "shared")
	attach.Flags().StringVarP(&attachTarget, "target", "t", "", "target session name or ID")
	root.AddCommand(attach)
	show := &cobra.Command{Use: "show TASK", Short: "Inspect task, process identity, exit status and health", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		m, err := open()
		if err != nil {
			return err
		}
		r, err := m.Resolve(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(m.Observe(cmd.Context(), r))
	}}
	root.AddCommand(show)
	stop := &cobra.Command{Use: "stop TASK", Short: "Stop the task session and wait for its exit", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		m, err := open()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
		defer cancel()
		return m.Stop(ctx, args[0])
	}}
	root.AddCommand(stop)
	var renameTarget string
	rename := &cobra.Command{Use: "rename-session [SESSION] NAME", Aliases: []string{"rename"}, Short: "Rename a task", Args: targetArgs(&renameTarget, 1), RunE: func(cmd *cobra.Command, args []string) error {
		m, err := open()
		if err != nil {
			return err
		}
		return m.Rename(cmd.Context(), targetValue(renameTarget, args), args[len(args)-1])
	}}
	rename.Flags().StringVarP(&renameTarget, "target", "t", "", "target session name or ID")
	root.AddCommand(rename)
	remove := &cobra.Command{Use: "delete TASK", Short: "Delete a stopped or dead task and its history", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		m, err := open()
		if err != nil {
			return err
		}
		return m.Delete(cmd.Context(), args[0])
	}}
	root.AddCommand(remove)
	var hasTarget string
	has := &cobra.Command{Use: "has-session [SESSION]", Aliases: []string{"has"}, Short: "Check whether a session is running", Args: targetArgs(&hasTarget, 0), RunE: func(cmd *cobra.Command, args []string) error {
		m, err := openStore()
		if err != nil {
			return err
		}
		r, err := m.Resolve(cmd.Context(), targetValue(hasTarget, args))
		if err != nil {
			return err
		}
		r = m.Observe(cmd.Context(), r)
		if r.Health != "online" || r.Runtime == nil || r.Runtime.State != task.Running {
			return &ExitError{Code: 1}
		}
		return nil
	}}
	has.Flags().StringVarP(&hasTarget, "target", "t", "", "target session name or ID")
	root.AddCommand(has)
	var killTarget string
	kill := &cobra.Command{Use: "kill-session [SESSION]", Short: "Stop a session and delete its history", Args: targetArgs(&killTarget, 0), RunE: func(cmd *cobra.Command, args []string) error {
		m, err := open()
		if err != nil {
			return err
		}
		r, err := m.Resolve(cmd.Context(), targetValue(killTarget, args))
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
		defer cancel()
		if err := m.Stop(ctx, string(r.Spec.ID)); err != nil {
			return err
		}
		return m.Delete(ctx, string(r.Spec.ID))
	}}
	kill.Flags().StringVarP(&killTarget, "target", "t", "", "target session name or ID")
	root.AddCommand(kill)

	recoverCmd := &cobra.Command{Use: "recover TASK", Short: "Mark a crashed broker's task failed without signalling stale PIDs", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		m, err := open()
		if err != nil {
			return err
		}
		return m.Recover(cmd.Context(), args[0])
	}}
	root.AddCommand(recoverCmd)
	var raw, follow bool
	var tail int
	logs := &cobra.Command{Use: "logs TASK", Short: "Read bounded task output logs (terminal controls stripped by default)", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		m, err := open()
		if err != nil {
			return err
		}
		if !follow {
			data, err := m.Logs(cmd.Context(), args[0], raw, tail)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		}
		// Follow uses stable byte offsets per generation; no duplicate output when rotated.
		return m.FollowLogs(cmd.Context(), args[0], raw, tail, cmd.OutOrStdout())
	}}
	logs.Flags().BoolVar(&raw, "raw", false, "include original terminal control bytes")
	logs.Flags().BoolVarP(&follow, "follow", "f", false, "follow output until task exit")
	logs.Flags().IntVar(&tail, "tail", 64*1024, "last N bytes (0 means all retained output)")
	root.AddCommand(logs)
	internal := &cobra.Command{Use: "__broker ID", Hidden: true, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		state, err := config.StateDir(stateDir)
		if err != nil {
			return err
		}
		ready := os.NewFile(3, "readiness")
		if ready == nil {
			return errors.New("missing readiness pipe")
		}
		defer ready.Close()
		return daemon.Run(cmd.Context(), state, task.ID(args[0]), ready)
	}}
	root.AddCommand(internal)
	root.AddCommand(&cobra.Command{Use: "__history", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		state, err := config.StateDir(stateDir)
		if err != nil {
			return err
		}
		return historycleanup.Run(cmd.Context(), state)
	}})
	preferences := &cobra.Command{Use: "settings", Short: "View or change interface preferences", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		m, err := open()
		if err != nil {
			return err
		}
		settings, err := m.Settings()
		if err != nil {
			return err
		}
		asJSON, _ := cmd.Flags().GetBool("json")
		if asJSON {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(settings)
		}
		l := i18n.New(settings.Language)
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "TaskDock · %s\n%s: %s (%s)\n%s: %s\n%s: %ds\n%s: %t\n%s: %s\n", l.Text("settings"), l.Text("language"), settings.Language, l.Language, l.Text("theme"), settings.Theme, l.Text("interval"), settings.RefreshSeconds, l.Text("resources"), settings.ShowResources, l.Text("default_tab"), settings.DefaultTab)
		if err != nil {
			return err
		}
		duration, _ := settings.Retention()
		value := duration.String()
		if duration == 0 {
			value = l.Text("never")
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", l.Text("auto_delete"), value)
		return err
	}}
	preferences.Flags().Bool("json", false, "print JSON")
	preferences.AddCommand(&cobra.Command{Use: "set KEY VALUE", Short: "Save one interface preference", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		m, err := openStore()
		if err != nil {
			return err
		}
		settings, err := m.Settings()
		if err != nil {
			return err
		}
		settings, err = config.With(settings, args[0], args[1])
		if err != nil {
			return err
		}
		if err := m.SaveSettings(cmd.Context(), settings); err != nil {
			return err
		}
		return m.EnableHistory()
	}})
	preferences.AddCommand(&cobra.Command{Use: "reset", Short: "Reset interface preferences", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		m, err := openStore()
		if err != nil {
			return err
		}
		if err := m.SaveSettings(cmd.Context(), config.Defaults()); err != nil {
			return err
		}
		return m.EnableHistory()
	}})
	root.AddCommand(preferences)
	configureLocalization(root, &stateDir)
	return root
}
func defaultShell() string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	return shell
}
func workingDir() string {
	dir, err := os.Getwd()
	if err != nil {
		return "/"
	}
	return dir
}

func attachCreated(ctx context.Context, m *service.Manager, r task.Record, input, output *os.File, noStatus bool) (terminal.AttachResult, error) {
	showFinished := func(r task.Record) (terminal.AttachResult, error) {
		data, err := m.Logs(ctx, string(r.Spec.ID), true, 0)
		if err != nil {
			return terminal.AttachResult{}, err
		}
		if err := terminal.WithRaw(input, func() error { _, err := output.Write(data); return err }); err != nil {
			return terminal.AttachResult{}, err
		}
		if err := terminal.ResetModes(output); err != nil {
			return terminal.AttachResult{}, err
		}
		code := 1
		if r.Runtime != nil && r.Runtime.ExitCode != nil {
			code = *r.Runtime.ExitCode
		}
		return terminal.AttachResult{ExitCode: code}, nil
	}
	if r.Runtime != nil && (r.Runtime.State == task.Exited || r.Runtime.State == task.Failed) {
		return showFinished(r)
	}
	result, err := terminal.AttachWithOptions(ctx, m.Store.Root(), r, input, output, terminal.AttachOptions{NoStatus: noStatus})
	if errors.Is(err, terminal.ErrConnect) {
		latest, loadErr := m.Store.Load(ctx, r.Spec.ID)
		if loadErr == nil && latest.Runtime != nil && (latest.Runtime.State == task.Exited || latest.Runtime.State == task.Failed) {
			return showFinished(latest)
		}
	}
	return result, err
}
