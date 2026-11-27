package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/davidemaggi/mcpsh/internal/config"
	"github.com/davidemaggi/mcpsh/internal/version"
)

const rootLong = `mcpsh exposes the tools of MCP servers as shell commands.

Discover, then call:
  mcpsh list                           configured servers
  mcpsh <server>                       tools of a server, one line each
  mcpsh <server> <tool> --help         parameters of a tool
  mcpsh <server> <tool> --param value  call it (or --json '{...}', --json @file.json, --json -)

Exit codes:
  0  success
  1  the tool reported an error (isError) or the server rejected the call
  2  usage, validation, configuration or trust error
  3  connection, transport, timeout or daemon error

The first call starts a background daemon that keeps server sessions warm.
Use --no-daemon (or MCPSH_NO_DAEMON=1) to run everything in-process.`

func (a *App) newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "mcpsh",
		Short:         "Use any MCP server from the shell",
		Long:          rootLong,
		Version:       version.Version,
		SilenceErrors: true,
		SilenceUsage:  true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if a.noDaemonFlag {
				a.noDaemon = true
			}
			if a.output != "text" && a.output != "json" {
				return usagef("", fmt.Sprintf("--output must be text or json, not %q", a.output))
			}
			return nil
		},
	}
	root.SetIn(a.Stdin)
	root.SetOut(a.Stdout)
	root.SetErr(a.Stderr)
	root.SetVersionTemplate(version.String() + "\n")
	root.CompletionOptions.HiddenDefaultCmd = true
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return usagef("see "+cmd.CommandPath()+" --help", err.Error())
	})

	pf := root.PersistentFlags()
	pf.StringVarP(&a.output, "output", "o", "text", "output format: text or json")
	pf.DurationVar(&a.timeout, "timeout", 0, "timeout for the tool call (e.g. 30s, 2m); default from config, else 60s")
	pf.BoolVarP(&a.yes, "yes", "y", false, "confirm tools marked as destructive without prompting")
	pf.BoolVar(&a.noDaemonFlag, "no-daemon", false, "run in-process without the background daemon (also "+EnvNoDaemon+"=1)")

	root.AddCommand(
		a.listCmd(), a.daemonCmd(), a.addCmd(), a.removeCmd(), a.importCmd(), a.trustCmd(), a.refreshCmd(), a.versionCmd(), a.callCmd(),
	)
	return root
}

// globalValueFlags take a value as the next argument when written without "=".
var globalValueFlags = []string{"--output", "-o", "--timeout"}

// firstPositional returns the index of the first non-flag argument, skipping global flags.
func firstPositional(args []string) int {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return -1
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			return i
		}
		if slices.Contains(globalValueFlags, a) {
			i++
		}
	}
	return -1
}

func (a *App) execute(ctx context.Context, args []string) error {
	root := a.newRoot()
	if i := firstPositional(args); i >= 0 {
		name := args[i]
		if name == "call" {
			// "mcpsh call <server> ..." is the explicit form of "mcpsh <server> ...".
			rest := append(slices.Clone(args[:i]), args[i+1:]...)
			j := firstPositional(rest)
			if j < 0 {
				return usagef("see mcpsh call --help", "missing server name")
			}
			args, name = rest, rest[j]
			i = j
		}
		if !a.isBuiltin(root, name) {
			if err := a.preparseGlobals(args); err != nil {
				return err
			}
			cmd, err := a.serverCmd(ctx, name, args[i+1:])
			if err != nil {
				return err
			}
			root.AddCommand(cmd)
		}
	}
	root.SetArgs(args)
	return root.ExecuteContext(ctx)
}

func (a *App) isBuiltin(root *cobra.Command, name string) bool {
	if name == "help" || name == "completion" || slices.Contains(config.ReservedNames, name) {
		return true
	}
	for _, c := range root.Commands() {
		if c.Name() == name || c.HasAlias(name) {
			return true
		}
	}
	return false
}

// preparseGlobals reads the global flags before cobra runs, because building the server
// command (discovery) already depends on --timeout.
func (a *App) preparseGlobals(args []string) error {
	fs := pflag.NewFlagSet("globals", pflag.ContinueOnError)
	fs.ParseErrorsAllowlist.UnknownFlags = true
	fs.SetOutput(nil)
	fs.Usage = func() {}
	var out string
	fs.StringVarP(&out, "output", "o", "text", "")
	fs.DurationVar(&a.timeout, "timeout", 0, "")
	noDaemon := fs.Bool("no-daemon", false, "")
	fs.BoolP("help", "h", false, "")
	if err := fs.Parse(args); err != nil {
		return usagef("see mcpsh --help", err.Error())
	}
	if *noDaemon {
		a.noDaemon = true
	}
	return nil
}

func (a *App) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the mcpsh version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if a.jsonOutput() {
				return writeJSON(a.Stdout, map[string]string{"version": version.Version, "commit": version.Commit, "date": version.Date})
			}
			fmt.Fprintln(a.Stdout, version.String())
			return nil
		},
	}
}

// callCmd only documents the explicit form; execute() rewrites "call" before cobra runs.
func (a *App) callCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "call <server> [tool] [flags]",
		Short: "Call a tool (same as \"mcpsh <server> <tool>\")",
		RunE: func(cmd *cobra.Command, args []string) error {
			return usagef("see mcpsh call --help", "missing server name")
		},
	}
}
