// Package skill writes Agent Skills (a folder with a SKILL.md) that teach coding agents to use
// mcpsh, into the folders Claude Code, Codex and GitHub Copilot read.
//
// Locations (checked against each tool's documentation, 2026-11-27):
//
//	Claude Code  ~/.claude/skills (or $CLAUDE_CONFIG_DIR/skills)  <project>/.claude/skills
//	Codex        ~/.agents/skills                                 <project>/.agents/skills
//	Copilot      ~/.copilot/skills (also ~/.agents/skills)        <project>/.github/skills (also .claude/skills, .agents/skills)
//
// "all" writes the Claude Code and the shared .agents locations, which Copilot reads too.
package skill

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

//go:embed templates/*.md
var templates embed.FS

// Targets.
const (
	TargetClaude  = "claude"
	TargetCodex   = "codex"
	TargetCopilot = "copilot"
	TargetAll     = "all"
)

// Scopes.
const (
	ScopeUser    = "user"
	ScopeProject = "project"
)

// MaxDescription is the Agent Skills limit for "description".
const MaxDescription = 1024

// generatedMarker identifies files mcpsh may overwrite.
const generatedMarker = "generated-by: mcpsh"

// Skill is one SKILL.md.
type Skill struct {
	Name        string
	Description string
	Body        string
}

// Render returns the SKILL.md content. allowTools adds Claude Code's allowed-tools field,
// which pre-approves mcpsh commands while the skill is active.
func (s *Skill) Render(allowTools bool) string {
	var b strings.Builder
	desc, _ := json.Marshal(s.Description) // a JSON string is a valid YAML double-quoted scalar
	b.WriteString("---\n")
	b.WriteString("name: " + s.Name + "\n")
	b.WriteString("description: " + string(desc) + "\n")
	if allowTools {
		b.WriteString("allowed-tools: Bash(mcpsh *)\n")
	}
	b.WriteString("metadata:\n  " + generatedMarker + "\n")
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimLeft(s.Body, "\n"))
	if !strings.HasSuffix(s.Body, "\n") {
		b.WriteString("\n")
	}
	return b.String()
}

var nameRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Validate checks the Agent Skills rules: name of 1-64 lowercase letters, digits and single
// hyphens; description of 1-1024 characters.
func (s *Skill) Validate() error {
	if len(s.Name) == 0 || len(s.Name) > 64 || !nameRE.MatchString(s.Name) {
		return fmt.Errorf("invalid skill name %q: use 1-64 lowercase letters, digits and single hyphens", s.Name)
	}
	if n := len([]rune(s.Description)); n == 0 || n > MaxDescription {
		return fmt.Errorf("skill %s: description must be 1-%d characters, got %d", s.Name, MaxDescription, n)
	}
	return nil
}

func body(name string) string {
	b, err := templates.ReadFile("templates/" + name + ".md")
	if err != nil {
		panic(err) // embedded at build time
	}
	return string(b)
}

// Builtin returns the generic skills: using mcpsh, and setting up servers for it.
func Builtin() []*Skill {
	return []*Skill{
		{
			Name: "mcpsh",
			Description: "Use MCP (Model Context Protocol) servers through the mcpsh CLI: discover the " +
				"configured servers and their tools, read a tool's parameters and call it from the shell. " +
				"Use when a task needs an external tool or data source that may be available as an MCP " +
				"server (issue trackers, databases, browsers, cloud and SaaS APIs, internal services), " +
				"or when the user mentions MCP, an MCP server or mcpsh.",
			Body: body("mcpsh"),
		},
		{
			Name: "mcpsh-setup",
			Description: "Configure an MCP server so agents can use it through the mcpsh CLI. Use when the " +
				"user wants to add, install, connect or set up an MCP server from an npm or PyPI package, " +
				"a Docker image, a URL, a README, or a .mcp.json / Claude Desktop config snippet, or asks " +
				"why an mcpsh server does not start or connect.",
			Body: body("mcpsh-setup"),
		},
	}
}

// AgentsSnippet is the text for AGENTS.md, CLAUDE.md or .github/copilot-instructions.md.
func AgentsSnippet() string { return body("agents") }

// Location is a skills folder of one agent.
type Location struct {
	Target     string
	Dir        string // the folder that contains <skill-name>/SKILL.md
	AllowTools bool   // write Claude Code's allowed-tools
}

// Locations returns the folders for a target and scope. projectRoot is used for the project scope.
func Locations(target, scope, projectRoot string) ([]Location, error) {
	if scope != ScopeUser && scope != ScopeProject {
		return nil, fmt.Errorf("--scope must be user or project, not %q", scope)
	}
	home, err := os.UserHomeDir()
	if err != nil && scope == ScopeUser {
		return nil, err
	}
	pick := func(user, project string) string {
		if scope == ScopeUser {
			return user
		}
		return filepath.Join(projectRoot, project)
	}
	claudeUser := filepath.Join(home, ".claude", "skills")
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		claudeUser = filepath.Join(d, "skills")
	}
	claude := Location{Target: TargetClaude, Dir: pick(claudeUser, filepath.Join(".claude", "skills")), AllowTools: true}
	codex := Location{Target: TargetCodex, Dir: pick(filepath.Join(home, ".agents", "skills"), filepath.Join(".agents", "skills"))}
	copilot := Location{Target: TargetCopilot, Dir: pick(filepath.Join(home, ".copilot", "skills"), filepath.Join(".github", "skills"))}
	switch target {
	case TargetClaude:
		return []Location{claude}, nil
	case TargetCodex:
		return []Location{codex}, nil
	case TargetCopilot:
		return []Location{copilot}, nil
	case TargetAll:
		return []Location{claude, codex}, nil
	}
	return nil, fmt.Errorf("--target must be claude, codex, copilot or all, not %q", target)
}

// Result says what Install did.
type Result string

const (
	Created   Result = "created"
	Updated   Result = "updated"
	Unchanged Result = "unchanged"
)

// ErrForeign: a SKILL.md not written by mcpsh is in the way.
var ErrForeign = errors.New("exists and was not generated by mcpsh")

// Install writes s into loc. A SKILL.md that mcpsh did not generate is replaced only with force.
func Install(loc Location, s *Skill, force bool) (string, Result, error) {
	if err := s.Validate(); err != nil {
		return "", "", err
	}
	path := filepath.Join(loc.Dir, s.Name, "SKILL.md")
	content := s.Render(loc.AllowTools)
	old, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return path, "", err
	case string(old) == content:
		return path, Unchanged, nil
	case !strings.Contains(string(old), generatedMarker) && !force:
		return path, "", fmt.Errorf("%s %w (use --force to replace it)", path, ErrForeign)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return path, "", err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return path, "", err
	}
	if old == nil {
		return path, Created, nil
	}
	return path, Updated, nil
}
