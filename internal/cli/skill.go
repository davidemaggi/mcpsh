package cli

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/davidemaggi/mcpsh/internal/skill"
)

func (a *App) skillCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "Install Agent Skills that teach coding agents to use mcpsh",
	}
	cmd.AddCommand(a.skillInstallCmd(), a.skillGenerateCmd())
	return cmd
}

func (a *App) skillInstallCmd() *cobra.Command {
	var (
		target, scope string
		names         []string
		force         bool
	)
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the mcpsh skills for Claude Code, Codex and/or GitHub Copilot",
		Long: `Install the generic skills:

  mcpsh        use the configured MCP servers (discover, read --help, call)
  mcpsh-setup  configure a new MCP server from a package, URL, README or JSON snippet

Targets and folders (user scope / project scope):

  claude   ~/.claude/skills    .claude/skills   (with allowed-tools: Bash(mcpsh *))
  codex    ~/.agents/skills    .agents/skills
  copilot  ~/.copilot/skills   .github/skills
  all      claude + the shared .agents folders, which Copilot reads too`,
		Example: `  mcpsh skill install
  mcpsh skill install --target claude --scope project`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := a.projectRoot(scope)
			if err != nil {
				return err
			}
			locs, err := skill.Locations(target, scope, root)
			if err != nil {
				return usagef("see mcpsh skill install --help", err.Error())
			}
			var skills []*skill.Skill
			for _, s := range skill.Builtin() {
				if len(names) == 0 || slices.Contains(names, s.Name) {
					skills = append(skills, s)
				}
			}
			if len(skills) == 0 {
				return usagef("", "--skill must be mcpsh or mcpsh-setup")
			}
			if err := a.installSkills(locs, skills, force); err != nil {
				return err
			}
			a.permissionHints(locs)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&target, "target", skill.TargetAll, "claude, codex, copilot or all")
	f.StringVar(&scope, "scope", skill.ScopeUser, "user or project")
	f.StringArrayVar(&names, "skill", nil, "install only this skill (mcpsh or mcpsh-setup); repeatable")
	f.BoolVar(&force, "force", false, "replace skills with the same name that mcpsh did not write")
	return cmd
}

func (a *App) skillGenerateCmd() *cobra.Command {
	var (
		target, scope, about string
		force, stdout        bool
	)
	cmd := &cobra.Command{
		Use:   "generate <server>",
		Short: "Write a skill for one server, so agents know when to use it",
		Long: `Write the skill mcpsh-<server> from the server's tools. Its description, which agents use
to decide when to load it, lists the tools; --description replaces that with your own words
about what the server is for (recommended for servers with many tools).`,
		Example: `  mcpsh skill generate github
  mcpsh skill generate warehouse --description "Query the sales data warehouse" --scope project`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rs, err := a.resolve(args[0])
			if err != nil {
				return err
			}
			disc, err := a.tools(cmd.Context(), rs, false, "")
			if err != nil {
				return err
			}
			infos := skill.FromTools(disc.tools, func(t *mcp.Tool) bool { return a.isDestructive(rs, t) })
			s := skill.ForServer(rs.Name, about, infos)
			if err := s.Validate(); err != nil {
				return usagef("", err.Error())
			}
			if stdout {
				fmt.Fprint(a.Stdout, s.Render(target == skill.TargetClaude))
				return nil
			}
			root, err := a.projectRoot(scope)
			if err != nil {
				return err
			}
			locs, err := skill.Locations(target, scope, root)
			if err != nil {
				return usagef("see mcpsh skill generate --help", err.Error())
			}
			return a.installSkills(locs, []*skill.Skill{s}, force)
		},
	}
	f := cmd.Flags()
	f.StringVar(&target, "target", skill.TargetAll, "claude, codex, copilot or all")
	f.StringVar(&scope, "scope", skill.ScopeUser, "user or project")
	f.StringVar(&about, "description", "", "what the server is for, in your words")
	f.BoolVar(&force, "force", false, "replace a skill with the same name that mcpsh did not write")
	f.BoolVar(&stdout, "stdout", false, "print the SKILL.md instead of writing it")
	return cmd
}

func (a *App) agentsMDCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "agents-md",
		Short: "Print instructions to paste into AGENTS.md, CLAUDE.md or .github/copilot-instructions.md",
		Long: `Print a short section that tells coding agents how to use mcpsh. Where it goes:

  Codex            AGENTS.md
  Claude Code      CLAUDE.md (or AGENTS.md when there is no CLAUDE.md)
  GitHub Copilot   .github/copilot-instructions.md (AGENTS.md also works in most places)`,
		Example: `  mcpsh agents-md >> AGENTS.md`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprint(a.Stdout, skill.AgentsSnippet())
			return nil
		},
	}
}

// projectRoot is the workspace root for the project scope.
func (a *App) projectRoot(scope string) (string, error) {
	if scope != skill.ScopeProject {
		return "", nil
	}
	cfg, err := a.config()
	if err != nil {
		return "", err
	}
	return cfg.WorkspaceRoot, nil
}

func (a *App) installSkills(locs []skill.Location, skills []*skill.Skill, force bool) error {
	var failed []string
	for _, loc := range locs {
		for _, s := range skills {
			path, res, err := skill.Install(loc, s, force)
			if err != nil {
				failed = append(failed, err.Error())
				continue
			}
			fmt.Fprintf(a.Stdout, "%-9s %s\n", res, path)
		}
	}
	if len(failed) > 0 {
		msg := strings.Join(failed, "\n")
		if strings.Contains(msg, skill.ErrForeign.Error()) {
			return usagef("", msg)
		}
		return errors.New(msg)
	}
	return nil
}

// permissionHints tells how to let each agent run mcpsh without asking every time.
func (a *App) permissionHints(locs []skill.Location) {
	for _, l := range locs {
		switch l.Target {
		case skill.TargetClaude:
			fmt.Fprintln(a.Stdout, "\nClaude Code: the skills pre-approve mcpsh commands while they are active (allowed-tools).")
		case skill.TargetCodex:
			fmt.Fprintln(a.Stdout, "\nCodex: to run mcpsh without approval prompts, add to ~/.codex/rules/default.rules:\n"+
				`  prefix_rule(pattern=["mcpsh"], decision="allow")`)
		case skill.TargetCopilot:
			fmt.Fprintln(a.Stdout, "\nCopilot CLI: allow mcpsh with --allow-tool 'shell(mcpsh:*)'.")
		}
	}
}
