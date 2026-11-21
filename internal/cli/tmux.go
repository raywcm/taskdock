package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/raywcm/taskdock/internal/tmux"
	"github.com/spf13/cobra"
)

func tmuxCommand() *cobra.Command {
	var server tmux.Server
	group := &cobra.Command{Use: "tmux", Short: "Browse sessions managed by tmux"}
	group.PersistentFlags().StringVarP(&server.Label, "server", "L", "", "tmux server name")
	group.PersistentFlags().StringVarP(&server.Socket, "socket", "S", "", "tmux socket path")
	group.MarkFlagsMutuallyExclusive("server", "socket")
	var asJSON bool
	list := &cobra.Command{Use: "ls", Aliases: []string{"list-sessions"}, Short: "List existing tmux sessions", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		sessions, err := server.List(cmd.Context())
		if err != nil {
			return err
		}
		if asJSON {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(sessions)
		}
		for _, s := range sessions {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s  %s\n", s.ID, s.Name); err != nil {
				return err
			}
		}
		return nil
	}}
	list.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	group.AddCommand(list)
	for _, name := range []string{"capture", "view"} {
		var target string
		c := &cobra.Command{Use: name + " [SESSION]", Args: targetArgs(&target, 0), RunE: func(cmd *cobra.Command, args []string) error {
			session, err := server.Resolve(cmd.Context(), targetValue(target, args))
			if err != nil {
				return err
			}
			if name == "capture" {
				text, err := server.Capture(cmd.Context(), session)
				if err != nil {
					return err
				}
				_, err = fmt.Fprint(cmd.OutOrStdout(), text)
				return err
			}
			if os.Getenv("TMUX") != "" {
				return errors.New("use the TaskDock tmux browser or tdock tmux capture inside tmux")
			}
			view := server.Viewer(cmd.Context(), session)
			view.Stdin, view.Stdout, view.Stderr = cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()
			return view.Run()
		}}
		if name == "capture" {
			c.Short = "Print the visible contents of the active tmux pane"
		} else {
			c.Short = "Attach a read-only tmux client"
		}
		c.Flags().StringVarP(&target, "target", "t", "", "exact tmux session name or ID")
		group.AddCommand(c)
	}
	return group
}
