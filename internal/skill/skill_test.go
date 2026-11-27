package skill

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var update = flag.Bool("update", false, "rewrite the skills/ folder of the repository")

// TestRepoSkillsInSync keeps skills/<name>/SKILL.md (installed by "npx skills add
// davidemaggi/mcpsh") identical to what "mcpsh skill install" writes for non-Claude agents.
// Run "go test ./internal/skill -update" after editing a template.
func TestRepoSkillsInSync(t *testing.T) {
	for _, s := range Builtin() {
		path := filepath.Join("..", "..", "skills", s.Name, "SKILL.md")
		want := s.Render(false)
		if *update {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%v (run: go test ./internal/skill -update)", err)
		}
		if strings.ReplaceAll(string(got), "\r\n", "\n") != want {
			t.Errorf("%s is out of date; run: go test ./internal/skill -update", path)
		}
	}
}

func TestBuiltinAreValid(t *testing.T) {
	for _, s := range Builtin() {
		if err := s.Validate(); err != nil {
			t.Error(err)
		}
		out := s.Render(true)
		if !strings.HasPrefix(out, "---\nname: "+s.Name+"\ndescription: \"") ||
			!strings.Contains(out, "allowed-tools: Bash(mcpsh *)\n") || !strings.Contains(out, generatedMarker) {
			t.Errorf("frontmatter of %s:\n%s", s.Name, out[:200])
		}
		if strings.Contains(s.Render(false), "allowed-tools") {
			t.Errorf("%s: allowed-tools must be Claude-only", s.Name)
		}
	}
	if !strings.Contains(AgentsSnippet(), "mcpsh <server> <tool> --help") {
		t.Error("agents snippet")
	}
}

func TestLocations(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	proj := filepath.Join(home, "proj")
	cases := []struct {
		target, scope string
		want          []string
	}{
		{TargetClaude, ScopeUser, []string{filepath.Join(home, ".claude", "skills")}},
		{TargetCodex, ScopeUser, []string{filepath.Join(home, ".agents", "skills")}},
		{TargetCopilot, ScopeUser, []string{filepath.Join(home, ".copilot", "skills")}},
		{TargetCopilot, ScopeProject, []string{filepath.Join(proj, ".github", "skills")}},
		{TargetAll, ScopeProject, []string{filepath.Join(proj, ".claude", "skills"), filepath.Join(proj, ".agents", "skills")}},
	}
	for _, c := range cases {
		locs, err := Locations(c.target, c.scope, proj)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, l := range locs {
			got = append(got, l.Dir)
			if l.AllowTools != (l.Target == TargetClaude) {
				t.Errorf("%s: allowed-tools only for Claude", l.Target)
			}
		}
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s/%s = %v, want %v", c.target, c.scope, got, c.want)
		}
	}
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "cc"))
	locs, _ := Locations(TargetClaude, ScopeUser, proj)
	if locs[0].Dir != filepath.Join(home, "cc", "skills") {
		t.Errorf("CLAUDE_CONFIG_DIR ignored: %s", locs[0].Dir)
	}
	if _, err := Locations("vim", ScopeUser, proj); err == nil {
		t.Error("bad target accepted")
	}
}

func TestInstall(t *testing.T) {
	loc := Location{Target: TargetClaude, Dir: t.TempDir(), AllowTools: true}
	s := Builtin()[0]
	path, res, err := Install(loc, s, false)
	if err != nil || res != Created {
		t.Fatalf("%v %v", res, err)
	}
	if _, res, _ = Install(loc, s, false); res != Unchanged {
		t.Errorf("second install: %v", res)
	}
	s2 := *s
	s2.Body += "\nmore\n"
	if _, res, _ = Install(loc, &s2, false); res != Updated {
		t.Errorf("changed install: %v", res)
	}
	if err := os.WriteFile(path, []byte("---\nname: mcpsh\ndescription: mine\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Install(loc, s, false); !errors.Is(err, ErrForeign) {
		t.Errorf("a foreign SKILL.md must not be overwritten: %v", err)
	}
	if _, res, err := Install(loc, s, true); err != nil || res != Updated {
		t.Errorf("--force: %v %v", res, err)
	}
}

func TestForServer(t *testing.T) {
	yes := true
	tools := []*mcp.Tool{
		{Name: "create_issue", Description: "Create an issue. Long details follow here."},
		{Name: "delete_repo", Description: "Delete a repository.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &yes}},
	}
	infos := FromTools(tools, func(t *mcp.Tool) bool {
		return t.Annotations != nil && t.Annotations.DestructiveHint != nil && *t.Annotations.DestructiveHint
	})
	s := ForServer("My GitHub", "", infos)
	if s.Name != "mcpsh-my-github" {
		t.Errorf("name = %q", s.Name)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.Description, "create_issue (Create an issue.)") || strings.Contains(s.Description, "Long details") {
		t.Errorf("description = %q", s.Description)
	}
	if !strings.Contains(s.Body, "`delete_repo`: Delete a repository. **Destructive") {
		t.Errorf("body:\n%s", s.Body)
	}
	if s := ForServer("x", "Query the sales warehouse", infos); !strings.HasPrefix(s.Description, "Query the sales warehouse. Use it through") {
		t.Errorf("about: %q", s.Description)
	}

	// Many tools: the description stays within the limit and says how many were left out.
	var many []ToolInfo
	for i := range 200 {
		many = append(many, ToolInfo{Name: strings.Repeat("t", 10) + string(rune('a'+i%26)), Description: strings.Repeat("word ", 20)})
	}
	big := ForServer("big", "", many)
	if err := big.Validate(); err != nil || !strings.Contains(big.Description, "more.") {
		t.Errorf("long list: %v %q", err, big.Description[len(big.Description)-40:])
	}
}

func TestSkillName(t *testing.T) {
	for in, want := range map[string]string{"github": "mcpsh-github", "My_Server.v2": "mcpsh-my-server-v2", "--x--": "mcpsh-x"} {
		if got := SkillName(in); got != want {
			t.Errorf("SkillName(%q) = %q, want %q", in, got, want)
		}
	}
}
