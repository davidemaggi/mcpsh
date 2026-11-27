package cli

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/davidemaggi/mcpsh/internal/config"
	"github.com/davidemaggi/mcpsh/internal/schema"
	"github.com/davidemaggi/mcpsh/internal/toolcache"
)

// discovery is the tool list of one server and where it came from.
type discovery struct {
	tools     []*mcp.Tool // filtered by allowTools / denyTools
	fetchedAt time.Time
	cached    bool
}

// tools returns the server's tools from the cache when fresh, else from the server.
// A stale cache is used, with a warning, when the server cannot be reached.
// When want is not empty and missing from a cached list, the list is refreshed once.
func (a *App) tools(ctx context.Context, rs *config.Resolved, refresh bool, want string) (*discovery, error) {
	cfg, err := a.config()
	if err != nil {
		return nil, err
	}
	ttl := cfg.Settings.CacheTTL.Or(config.DefaultCacheTTL)
	entry := toolcache.Get(rs.ID)
	if !refresh && entry != nil && entry.Fresh(ttl) {
		d := &discovery{tools: filterTools(entry.Tools, rs), fetchedAt: entry.FetchedAt, cached: true}
		// A tool missing from the cached list may be new on the server: fetch again.
		// A tool that is there but filtered out stays hidden without a refetch.
		if want == "" || hasTool(entry.Tools, want) {
			return d, nil
		}
	}
	fctx, cancel := context.WithTimeout(ctx, rs.StartupTimeout+a.callTimeout(rs))
	defer cancel()
	tools, err := a.engine().ListTools(fctx, rs)
	if err != nil {
		// Implicit discovery may fall back to a stale cache; an explicit refresh may not.
		if entry != nil && !refresh {
			fmt.Fprintf(a.Stderr, "mcpsh: warning: using the tool list cached %s ago: %v\n",
				time.Since(entry.FetchedAt).Round(time.Second), err)
			return &discovery{tools: filterTools(entry.Tools, rs), fetchedAt: entry.FetchedAt, cached: true}, nil
		}
		return nil, err
	}
	return &discovery{tools: filterTools(tools, rs), fetchedAt: time.Now()}, nil
}

func hasTool(tools []*mcp.Tool, name string) bool { return findTool(tools, name) != nil }

func (a *App) callTimeout(rs *config.Resolved) time.Duration {
	if a.timeout > 0 {
		return a.timeout
	}
	return rs.CallTimeout
}

// filterTools applies allowTools (when set) then denyTools, both glob patterns.
func filterTools(tools []*mcp.Tool, rs *config.Resolved) []*mcp.Tool {
	var out []*mcp.Tool
	for _, t := range tools {
		if len(rs.AllowTools) > 0 && !matchAny(rs.AllowTools, t.Name) {
			continue
		}
		if matchAny(rs.DenyTools, t.Name) {
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func matchAny(patterns []string, name string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

// findTool matches the original name or its kebab-case alias.
func findTool(tools []*mcp.Tool, name string) *mcp.Tool {
	for _, t := range tools {
		if t.Name == name {
			return t
		}
	}
	for _, t := range tools {
		if schema.Kebab(t.Name) == name {
			return t
		}
	}
	return nil
}

// serverCmd builds "mcpsh <server>" with one subcommand per tool.
func (a *App) serverCmd(ctx context.Context, name string, rest []string) (*cobra.Command, error) {
	rs, err := a.resolve(name)
	if err != nil {
		return nil, err
	}
	want := ""
	if i := firstPositional(rest); i >= 0 {
		want = rest[i]
	}
	disc, err := a.tools(ctx, rs, false, want)
	if err != nil {
		return nil, err
	}
	cmd := &cobra.Command{
		Use:   name + " [tool]",
		Short: "Tools of the " + name + " server",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return a.unknownTool(cmd, rs, disc, args[0])
			}
			return a.printTools(rs, disc)
		},
	}
	cmd.SetHelpFunc(func(*cobra.Command, []string) { _ = a.printTools(rs, disc) })
	for _, t := range disc.tools {
		tc, err := a.toolCmd(rs, t)
		if err != nil {
			fmt.Fprintf(a.Stderr, "mcpsh: warning: skipping tool %q: %v\n", t.Name, err)
			continue
		}
		cmd.AddCommand(tc)
	}
	return cmd, nil
}

func (a *App) unknownTool(cmd *cobra.Command, rs *config.Resolved, disc *discovery, name string) error {
	msg := fmt.Sprintf("server %q has no tool %q", rs.Name, name)
	if s := cmd.SuggestionsFor(name); len(s) > 0 {
		msg += "; did you mean " + strings.Join(s, " or ") + "?"
	}
	return usagef("run \"mcpsh "+rs.Name+"\" to list its tools", msg)
}

// toolInfo is the JSON form of a tool in listings.
type toolInfo struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description"`
	Destructive bool   `json:"destructive,omitempty"`
	ReadOnly    bool   `json:"readOnly,omitempty"`
}

func (a *App) printTools(rs *config.Resolved, disc *discovery) error {
	if a.jsonOutput() {
		infos := make([]toolInfo, 0, len(disc.tools))
		for _, t := range disc.tools {
			infos = append(infos, toolInfo{
				Name: t.Name, Title: t.Title, Description: t.Description,
				Destructive: a.isDestructive(rs, t), ReadOnly: t.Annotations != nil && t.Annotations.ReadOnlyHint,
			})
		}
		return writeJSON(a.Stdout, infos)
	}
	w := a.Stdout
	fmt.Fprintf(w, "Tools of %q (%d", rs.Name, len(disc.tools))
	if disc.cached {
		fmt.Fprintf(w, ", cached %s ago", time.Since(disc.fetchedAt).Round(time.Second))
	}
	fmt.Fprintln(w, "):")
	width := 0
	for _, t := range disc.tools {
		width = max(width, len(t.Name))
	}
	for _, t := range disc.tools {
		desc := firstLine(t.Description)
		if desc == "" {
			desc = t.Title
		}
		if a.isDestructive(rs, t) {
			desc = "[destructive] " + desc
		}
		fmt.Fprintf(w, "  %-*s  %s\n", width, t.Name, truncate(desc, 100))
	}
	fmt.Fprintf(w, "\nRun \"mcpsh %s <tool> --help\" for a tool's parameters.\n", rs.Name)
	return nil
}

// isDestructive: noConfirmTools wins, then confirmTools, then an explicit destructiveHint=true.
func (a *App) isDestructive(rs *config.Resolved, t *mcp.Tool) bool {
	if matchAny(rs.NoConfirmTools, t.Name) {
		return false
	}
	if matchAny(rs.ConfirmTools, t.Name) {
		return true
	}
	return t.Annotations != nil && t.Annotations.DestructiveHint != nil && *t.Annotations.DestructiveHint
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
