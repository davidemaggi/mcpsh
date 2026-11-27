package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/davidemaggi/mcpsh/internal/daemon"
	"github.com/davidemaggi/mcpsh/internal/ipc"
	"github.com/davidemaggi/mcpsh/internal/logging"
)

func (a *App) daemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage the background daemon that keeps server sessions alive",
		Long: `The daemon keeps MCP server sessions alive between commands, so servers started with
npx or uvx start once instead of on every call. The first command that needs it starts it;
it stops servers after their idle timeout and exits after its own (daemonIdleTimeout).`,
	}
	client := func() *daemon.Client { return daemon.NewClient(a.daemonIdle()) }

	start := &cobra.Command{
		Use:   "start",
		Short: "Start the daemon if it is not running",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := client().Status(cmd.Context())
			if err == nil {
				fmt.Fprintf(a.Stdout, "The daemon is already running (pid %d).\n", st.Daemon.Pid)
				return nil
			}
			if err := daemon.Start(); err != nil {
				return err
			}
			st, err = waitStatus(cmd.Context(), client())
			if err != nil {
				return err
			}
			fmt.Fprintf(a.Stdout, "Daemon started (pid %d).\n", st.Daemon.Pid)
			return nil
		},
	}
	stop := &cobra.Command{
		Use:   "stop",
		Short: "Stop the daemon and every server it runs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			was, err := client().Stop(cmd.Context())
			if err != nil {
				return err
			}
			if was {
				fmt.Fprintln(a.Stdout, "Daemon stopped.")
			} else {
				fmt.Fprintln(a.Stdout, "The daemon is not running.")
			}
			return nil
		},
	}
	restart := &cobra.Command{
		Use:   "restart",
		Short: "Restart the daemon (servers start again on their next call)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := client()
			if _, err := c.Stop(cmd.Context()); err != nil {
				return err
			}
			if err := daemon.Start(); err != nil {
				return err
			}
			st, err := waitStatus(cmd.Context(), c)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.Stdout, "Daemon restarted (pid %d).\n", st.Daemon.Pid)
			return nil
		},
	}
	status := &cobra.Command{
		Use:   "status",
		Short: "Show the daemon and its server sessions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := client().Status(cmd.Context())
			if errors.Is(err, daemon.ErrNotRunning) {
				if a.jsonOutput() {
					return writeJSON(a.Stdout, map[string]any{"running": false})
				}
				fmt.Fprintln(a.Stdout, "The daemon is not running; the next command that needs it starts it.")
				return nil
			}
			if err != nil {
				return err
			}
			if a.jsonOutput() {
				return writeJSON(a.Stdout, map[string]any{"running": true, "status": st})
			}
			printStatus(a.Stdout, st)
			return nil
		},
	}
	var lines int
	var follow bool
	logs := &cobra.Command{
		Use:   "logs",
		Short: "Print the daemon log",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := logging.Path()
			if err != nil {
				return err
			}
			return tailFile(cmd.Context(), a.Stdout, p, lines, follow)
		},
	}
	logs.Flags().IntVarP(&lines, "lines", "n", 100, "number of lines to print")
	logs.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new lines")
	run := &cobra.Command{
		Use:    "run",
		Short:  "Run the daemon in the foreground (used by the automatic start)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			err := daemon.Run()
			if errors.Is(err, ipc.ErrLocked) {
				return nil // another daemon won the race: nothing to do
			}
			return err
		},
	}
	cmd.AddCommand(start, stop, restart, status, logs, run)
	return cmd
}

func waitStatus(ctx context.Context, c *daemon.Client) (*ipc.Status, error) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, err := c.Status(ctx)
		if err == nil {
			return st, nil
		}
		if time.Now().After(deadline) {
			return nil, &daemon.Error{Msg: "the daemon did not start: " + err.Error() + " (see \"mcpsh daemon logs\")"}
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func printStatus(w io.Writer, st *ipc.Status) {
	d := st.Daemon
	fmt.Fprintf(w, "Daemon: running, pid %d, %s, up %s, exits after %s idle\n",
		d.Pid, d.Version, time.Since(d.StartedAt).Round(time.Second), time.Duration(st.IdleTimeoutMs)*time.Millisecond)
	if len(st.Sessions) == 0 {
		fmt.Fprintln(w, "No server sessions.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "\nSERVER\tSTATE\tPID\tUPTIME\tCALLS\tIDLE\tRESTARTS\tLAST ERROR")
	for _, s := range st.Sessions {
		pid, up, idle := "-", "-", "-"
		if s.Pid > 0 {
			pid = fmt.Sprint(s.Pid)
		}
		if !s.StartedAt.IsZero() && s.State == "running" {
			up = time.Since(s.StartedAt).Round(time.Second).String()
		}
		if !s.LastUsed.IsZero() {
			idle = time.Since(s.LastUsed).Round(time.Second).String()
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%d\t%s\n", s.Name, s.State, pid, up, s.Calls, idle, s.Restarts, truncate(s.LastError, 70))
	}
	tw.Flush()
}

// tailFile prints the last n lines of path and, with follow, new lines as they arrive.
func tailFile(ctx context.Context, w io.Writer, path string, n int, follow bool) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(w, "No daemon log yet.")
			return nil
		}
		return err
	}
	defer f.Close()
	var ring []string
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadString('\n')
		if line != "" && strings.HasSuffix(line, "\n") {
			ring = append(ring, line)
			if len(ring) > n {
				ring = ring[1:]
			}
		}
		if err != nil {
			break
		}
	}
	for _, l := range ring {
		fmt.Fprint(w, l)
	}
	if !follow {
		return nil
	}
	for {
		line, err := r.ReadString('\n')
		if line != "" {
			fmt.Fprint(w, line)
		}
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(300 * time.Millisecond):
			}
		}
	}
}
