package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/davidemaggi/mcpsh/internal/config"
	"github.com/davidemaggi/mcpsh/internal/daemon"
	"github.com/davidemaggi/mcpsh/internal/ipc"
	"github.com/davidemaggi/mcpsh/internal/toolcache"
)

// ---- list ----

type serverInfo struct {
	Name      string `json:"name"`
	Scope     string `json:"scope"`
	File      string `json:"file"`
	Transport string `json:"transport"`
	Target    string `json:"target"`
	Tools     *int   `json:"tools,omitempty"`
	Pid       int    `json:"pid,omitempty"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
}

func (a *App) listCmd() *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List configured servers and their status",
		Long: `List the servers from the user and project config files.

Without --check no server is started: the tool count comes from the discovery cache.
With --check every server is contacted and its tool list refreshed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			ts, err := a.trustStore()
			if err != nil {
				return err
			}
			names := cfg.Names()
			infos := make([]serverInfo, len(names))
			ids := make([]string, len(names)) // resolved IDs, to match daemon sessions
			var wg sync.WaitGroup
			for i, name := range names {
				e := cfg.Servers[name]
				info := serverInfo{
					Name: name, Scope: string(e.Scope), File: e.File,
					Transport: e.Server.TransportType(), Target: rawTarget(&e.Server), Status: "ready",
				}
				rs, rerr := cfg.Resolve(name, a.Lookup)
				switch {
				case e.Server.Disabled:
					info.Status = "disabled"
				case !ts.Trusted(e):
					info.Status = "untrusted"
					info.Error = "run: mcpsh trust " + name
				case rerr != nil:
					info.Status = "error"
					info.Error = rerr.Error()
				default:
					ids[i] = rs.ID
					if c := toolcache.Get(rs.ID); c != nil {
						n := len(filterTools(c.Tools, rs))
						info.Tools = &n
					}
					if check {
						wg.Add(1)
						go func(i int, rs *config.Resolved) {
							defer wg.Done()
							d, err := a.tools(cmd.Context(), rs, true, "")
							if err != nil {
								infos[i].Status = "error"
								infos[i].Error = firstLine(err.Error())
								return
							}
							n := len(d.tools)
							infos[i].Tools = &n
							infos[i].Status = "ok"
						}(i, rs)
					}
				}
				infos[i] = info
			}
			wg.Wait()
			a.addSessionStates(cmd.Context(), infos, ids)
			if a.jsonOutput() {
				return writeJSON(a.Stdout, infos)
			}
			if len(infos) == 0 {
				fmt.Fprintln(a.Stdout, "No servers configured. Add one with:\n  mcpsh add <name> -- <command> [args...]\n  mcpsh add <name> --url https://...")
				return nil
			}
			tw := tabwriter.NewWriter(a.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tSCOPE\tTRANSPORT\tTOOLS\tSTATUS\tTARGET")
			for _, in := range infos {
				tools := "-"
				if in.Tools != nil {
					tools = fmt.Sprint(*in.Tools)
				}
				status := in.Status
				if in.Pid > 0 {
					status += fmt.Sprintf(" (pid %d)", in.Pid)
				}
				if in.Error != "" {
					status += " (" + truncate(in.Error, 80) + ")"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", in.Name, in.Scope, in.Transport, tools, status, truncate(in.Target, 60))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "connect to every server and refresh its tool list")
	return cmd
}

// addSessionStates shows what the daemon is doing with each server, without starting it.
func (a *App) addSessionStates(ctx context.Context, infos []serverInfo, ids []string) {
	if a.noDaemon {
		return
	}
	sctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	st, err := daemon.NewClient(0).Status(sctx)
	if err != nil {
		return
	}
	byID := map[string]ipc.SessionStatus{}
	for _, s := range st.Sessions {
		byID[s.ID] = s
	}
	for i := range infos {
		s, ok := byID[ids[i]]
		if !ok || ids[i] == "" || infos[i].Status != "ready" && infos[i].Status != "ok" {
			continue
		}
		infos[i].Pid = s.Pid
		switch s.State {
		case "running":
			infos[i].Status = "running"
		case "crashed":
			infos[i].Status = "crashed"
			infos[i].Error = s.LastError
		default:
			infos[i].Status = s.State
		}
	}
}

// rawTarget describes a server from its uninterpolated definition (never shows secret values).
func rawTarget(s *config.Server) string {
	if s.TransportType() == config.TransportStdio {
		return strings.TrimSpace(s.Command + " " + strings.Join(s.Args, " "))
	}
	return s.URL
}

// ---- add ----

func (a *App) addCmd() *cobra.Command {
	var (
		url, transport, scope, cwd string
		envs, headers, allow, deny []string
		force, allowLiteral        bool
	)
	cmd := &cobra.Command{
		Use:   "add <name> [flags] -- <command> [args...] | add <name> --url <url>",
		Short: "Add a server to the user or project config",
		Example: `  mcpsh add fs -- npx -y @modelcontextprotocol/server-filesystem .
  mcpsh add github --url https://api.githubcopilot.com/mcp/ --header "Authorization: Bearer ${GITHUB_TOKEN}"
  mcpsh add tracker --scope project --env API_KEY='${TRACKER_KEY}' -- uvx tracker-mcp`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			dash := cmd.ArgsLenAtDash()
			if dash > 1 {
				return usagef("see mcpsh add --help", "put the server command after \"--\"")
			}
			var command []string
			if dash == 1 {
				command = args[1:]
			}
			s := config.Server{Cwd: cwd, AllowTools: allow, DenyTools: deny}
			switch {
			case url != "" && len(command) > 0:
				return usagef("see mcpsh add --help", "use either --url or a command after \"--\", not both")
			case url != "":
				s.URL = url
				s.Type = config.TransportHTTP
				if transport != "" {
					s.Type = transport
				}
			case len(command) > 0:
				s.Command, s.Args = command[0], command[1:]
				if transport != "" && transport != config.TransportStdio {
					return usagef("see mcpsh add --help", "--transport "+transport+" needs --url")
				}
			default:
				return usagef("see mcpsh add --help", "give a command after \"--\" or a --url")
			}
			var err error
			if s.Env, err = parsePairs(envs, "=", "--env"); err != nil {
				return err
			}
			if s.Headers, err = parsePairs(headers, ":", "--header"); err != nil {
				return err
			}
			path, sc, err := a.scopeFile(scope)
			if err != nil {
				return err
			}
			if lit := config.LiteralSecrets(&s); sc == config.ScopeProject && len(lit) > 0 && !allowLiteral {
				return usagef("project files are usually committed",
					fmt.Sprintf("%s look like secrets written in clear text; reference an environment variable instead, e.g. --env API_KEY='${API_KEY}' (or pass --allow-literal-secrets)",
						strings.Join(lit, ", ")))
			}
			if err := config.AddServer(path, name, s, force); err != nil {
				return usagef("", err.Error())
			}
			if sc == config.ScopeProject {
				if err := a.trustEntries(&config.Entry{Name: name, Scope: sc, File: path, Server: s}); err != nil {
					return err
				}
			}
			fmt.Fprintf(a.Stdout, "Added %s server %q to %s (%s).\nNext: mcpsh %s --help\n", s.TransportType(), name, path, sc, name)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&url, "url", "", "URL of a remote server (Streamable HTTP)")
	f.StringVar(&transport, "transport", "", "http or sse for --url (default http)")
	f.StringVar(&scope, "scope", "user", "user or project")
	f.StringVar(&cwd, "cwd", "", "working directory of a stdio server (default: the project root)")
	f.StringArrayVar(&envs, "env", nil, "environment variable KEY=VALUE for the server; repeatable; use '${VAR}' for secrets")
	f.StringArrayVar(&headers, "header", nil, "HTTP header 'Name: value'; repeatable; use '${VAR}' for secrets")
	f.StringArrayVar(&allow, "allow-tool", nil, "only expose tools matching this glob; repeatable")
	f.StringArrayVar(&deny, "deny-tool", nil, "hide tools matching this glob; repeatable")
	f.BoolVar(&force, "force", false, "replace an existing server with the same name")
	f.BoolVar(&allowLiteral, "allow-literal-secrets", false, "allow secret-looking values in clear text in a project file")
	return cmd
}

func parsePairs(items []string, sep, flag string) (map[string]string, error) {
	if len(items) == 0 {
		return nil, nil
	}
	m := map[string]string{}
	for _, it := range items {
		k, v, ok := strings.Cut(it, sep)
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, usagef("see mcpsh add --help", fmt.Sprintf("%s %q: expected NAME%sVALUE", flag, it, sep))
		}
		if sep == ":" {
			v = strings.TrimSpace(v)
		}
		m[k] = v
	}
	return m, nil
}

// scopeFile returns the file to edit for a scope. The project file is the nearest
// .mcpsh.json, else a new one at the workspace root.
func (a *App) scopeFile(scope string) (string, config.Scope, error) {
	cfg, err := a.config()
	if err != nil {
		return "", "", err
	}
	switch config.Scope(scope) {
	case config.ScopeUser:
		return cfg.UserFile, config.ScopeUser, nil
	case config.ScopeProject:
		if cfg.ProjectFile != "" {
			return cfg.ProjectFile, config.ScopeProject, nil
		}
		return filepath.Join(cfg.WorkspaceRoot, config.ProjectFileName), config.ScopeProject, nil
	}
	return "", "", usagef("", fmt.Sprintf("--scope must be user or project, not %q", scope))
}

// ---- remove ----

func (a *App) removeCmd() *cobra.Command {
	var scope string
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a server from the config",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := a.config()
			if err != nil {
				return err
			}
			var files []string
			if scope != "" {
				p, _, err := a.scopeFile(scope)
				if err != nil {
					return err
				}
				files = []string{p}
			} else {
				for _, p := range []string{cfg.UserFile, cfg.ProjectFile} {
					if p == "" {
						continue
					}
					if servers, err := config.ReadServers(p); err == nil {
						if _, ok := servers[name]; ok {
							files = append(files, p)
						}
					}
				}
				if len(files) > 1 {
					return usagef("", fmt.Sprintf("server %q is defined in both %s and %s; choose one with --scope user|project", name, files[0], files[1]))
				}
			}
			if len(files) == 0 {
				return &config.UnknownServerError{Name: name}
			}
			ok, err := config.RemoveServer(files[0], name)
			if err != nil {
				return err
			}
			if !ok {
				return usagef("", fmt.Sprintf("server %q is not in %s", name, files[0]))
			}
			if ts, err := a.trustStore(); err == nil {
				ts.Revoke(files[0], name)
				_ = ts.Save()
			}
			fmt.Fprintf(a.Stdout, "Removed server %q from %s.\n", name, files[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "", "user or project (needed when both define the server)")
	return cmd
}

// ---- import ----

func (a *App) importCmd() *cobra.Command {
	var (
		scope               string
		force, allowLiteral bool
	)
	cmd := &cobra.Command{
		Use:   "import [file]",
		Short: "Copy servers from a Claude-style .mcp.json (or any mcpServers file)",
		Long: `Copy the "mcpServers" of another file into an mcpsh config file.
Without a file, .mcp.json at the project root is used. Servers imported into the project
file are trusted, because you chose to import them.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			src := filepath.Join(cfg.WorkspaceRoot, ".mcp.json")
			if len(args) == 1 {
				src = args[0]
			}
			servers, err := config.ReadServers(src)
			if err != nil {
				return err
			}
			path, sc, err := a.scopeFile(scope)
			if err != nil {
				return err
			}
			existing, _ := config.ReadServers(path)
			names := make([]string, 0, len(servers))
			for n := range servers {
				names = append(names, n)
			}
			sort.Strings(names)
			var imported, skipped []string
			var entries []*config.Entry
			for _, n := range names {
				s := servers[n]
				if _, ok := existing[n]; ok && !force {
					skipped = append(skipped, n+" (exists; --force replaces it)")
					continue
				}
				if err := config.ValidateName(n); err != nil {
					skipped = append(skipped, n+" ("+err.Error()+")")
					continue
				}
				if lit := config.LiteralSecrets(&s); sc == config.ScopeProject && len(lit) > 0 && !allowLiteral {
					skipped = append(skipped, fmt.Sprintf("%s (clear-text secrets in %s; use ${VAR} or --allow-literal-secrets)", n, strings.Join(lit, ", ")))
					continue
				}
				if err := config.AddServer(path, n, s, true); err != nil {
					skipped = append(skipped, n+" ("+err.Error()+")")
					continue
				}
				imported = append(imported, n)
				entries = append(entries, &config.Entry{Name: n, Scope: sc, File: path, Server: s})
			}
			if sc == config.ScopeProject {
				if err := a.trustEntries(entries...); err != nil {
					return err
				}
			}
			fmt.Fprintf(a.Stdout, "Imported %d server(s) from %s into %s.\n", len(imported), src, path)
			for _, n := range imported {
				fmt.Fprintln(a.Stdout, "  + "+n)
			}
			for _, n := range skipped {
				fmt.Fprintln(a.Stdout, "  - skipped "+n)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "project", "user or project")
	cmd.Flags().BoolVar(&force, "force", false, "replace servers that already exist")
	cmd.Flags().BoolVar(&allowLiteral, "allow-literal-secrets", false, "allow secret-looking values in clear text in a project file")
	return cmd
}

// ---- trust ----

func (a *App) trustCmd() *cobra.Command {
	var all, revoke bool
	cmd := &cobra.Command{
		Use:   "trust [server...]",
		Short: "Review and approve servers defined in the project file",
		Long: `Servers from a project .mcpsh.json run commands on your machine, so mcpsh runs them
only after you approve them. An approval covers the exact definition: if the file changes,
the server needs approval again.

Without arguments, the untrusted project servers are shown with their definitions.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			ts, err := a.trustStore()
			if err != nil {
				return err
			}
			var targets []*config.Entry
			for _, n := range cfg.Names() {
				e := cfg.Servers[n]
				if e.Scope != config.ScopeProject {
					continue
				}
				if all || contains(args, n) {
					targets = append(targets, e)
				}
			}
			for _, n := range args {
				if e, ok := cfg.Servers[n]; !ok {
					return &config.UnknownServerError{Name: n}
				} else if e.Scope != config.ScopeProject {
					return usagef("", fmt.Sprintf("server %q comes from the user config and needs no approval", n))
				}
			}
			if len(args) == 0 && !all {
				return a.showUntrusted(cfg, ts)
			}
			for _, e := range targets {
				if revoke {
					ts.Revoke(e.File, e.Name)
					fmt.Fprintf(a.Stdout, "Revoked %q.\n", e.Name)
				} else {
					ts.Trust(e)
					fmt.Fprintf(a.Stdout, "Trusted %q from %s.\n", e.Name, e.File)
				}
			}
			return ts.Save()
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "approve every server of the project file")
	cmd.Flags().BoolVar(&revoke, "revoke", false, "withdraw the approval instead")
	return cmd
}

func (a *App) showUntrusted(cfg *config.Config, ts *config.TrustStore) error {
	n := 0
	for _, name := range cfg.Names() {
		e := cfg.Servers[name]
		if e.Scope != config.ScopeProject || ts.Trusted(e) {
			continue
		}
		n++
		b, _ := json.MarshalIndent(e.Server, "  ", "  ")
		fmt.Fprintf(a.Stdout, "%s (from %s):\n  %s\n\n", name, e.File, b)
	}
	if n == 0 {
		fmt.Fprintln(a.Stdout, "Every project server is trusted.")
		return nil
	}
	fmt.Fprintln(a.Stdout, "Approve with: mcpsh trust <server> (or mcpsh trust --all)")
	return nil
}

func (a *App) trustEntries(entries ...*config.Entry) error {
	ts, err := a.trustStore()
	if err != nil {
		return err
	}
	for _, e := range entries {
		ts.Trust(e)
	}
	return ts.Save()
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ---- refresh ----

func (a *App) refreshCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "refresh [server...]",
		Short: "Fetch the tool lists again and update the discovery cache",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			names := args
			explicit := len(args) > 0
			if !explicit {
				names = cfg.Names()
			}
			var failed error
			for _, n := range names {
				e, ok := cfg.Servers[n]
				if !ok {
					return &config.UnknownServerError{Name: n}
				}
				if !explicit && e.Server.Disabled {
					continue
				}
				rs, err := a.resolve(n)
				if err == nil {
					var d *discovery
					d, err = a.refreshOne(cmd.Context(), rs)
					if err == nil {
						fmt.Fprintf(a.Stdout, "%s: %d tools\n", n, len(d.tools))
						continue
					}
				}
				var unt *config.UntrustedError
				if !explicit && errors.As(err, &unt) {
					fmt.Fprintf(a.Stdout, "%s: skipped (not trusted)\n", n)
					continue
				}
				fmt.Fprintf(a.Stderr, "%s: %v\n", n, err)
				if failed == nil || ExitCode(err) > ExitCode(failed) {
					failed = err
				}
			}
			if failed != nil {
				return &silentError{code: ExitCode(failed)}
			}
			return nil
		},
	}
}

func (a *App) refreshOne(ctx context.Context, rs *config.Resolved) (*discovery, error) {
	return a.tools(ctx, rs, true, "")
}
