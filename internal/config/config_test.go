package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/davidemaggi/mcpsh/internal/platform"
)

func lookupFrom(m map[string]string) Lookup {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestExpand(t *testing.T) {
	env := lookupFrom(map[string]string{"TOKEN": "abc", "EMPTY": ""})
	cases := []struct {
		in, want string
		missing  string
	}{
		{in: "plain", want: "plain"},
		{in: "Bearer ${TOKEN}", want: "Bearer abc"},
		{in: "${TOKEN}${TOKEN}", want: "abcabc"},
		{in: "${NOPE:-fallback}", want: "fallback"},
		{in: "${TOKEN:-fallback}", want: "abc"},
		{in: "${EMPTY:-fallback}", want: "fallback"},
		{in: "${EMPTY}", want: ""},
		{in: "$$HOME and $HOME", want: "$HOME and $HOME"},
		{in: "${NOPE:-}", want: ""},
		{in: "${NOPE}", missing: "NOPE"},
	}
	for _, c := range cases {
		got, err := Expand(c.in, env, "f")
		if c.missing != "" {
			var mv *MissingVarError
			if !errors.As(err, &mv) || mv.Var != c.missing {
				t.Errorf("Expand(%q): want MissingVarError for %s, got %v", c.in, c.missing, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("Expand(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	if _, err := Expand("${BAD-NAME}", env, "f"); err == nil {
		t.Error("invalid name: want error")
	}
	if _, err := Expand("${OPEN", env, "f"); err == nil {
		t.Error("unterminated: want error")
	}
}

// setup isolates the user config dir and creates a project tree with a .git marker.
func setup(t *testing.T) (userDir, project string) {
	t.Helper()
	root := t.TempDir()
	userDir = filepath.Join(root, "user")
	t.Setenv(platform.EnvConfigDir, userDir)
	t.Setenv(platform.EnvStateDir, filepath.Join(root, "state"))
	project = filepath.Join(root, "proj")
	must(t, os.MkdirAll(filepath.Join(project, ".git"), 0o755))
	must(t, os.MkdirAll(filepath.Join(project, "sub", "dir"), 0o755))
	return userDir, project
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(path), 0o755))
	must(t, os.WriteFile(path, []byte(content), 0o644))
}

func TestLoadMerge(t *testing.T) {
	userDir, project := setup(t)
	write(t, filepath.Join(userDir, UserFileName), `{
	  "mcpServers": {
	    "github": {"type": "http", "url": "https://user.example/mcp", "headers": {"Authorization": "Bearer ${GH}"}},
	    "fs": {"command": "npx", "args": ["-y", "fs"]},
	    "gone": {"command": "old"}
	  },
	  "settings": {"callTimeout": "30s", "cacheTTL": 3600}
	}`)
	write(t, filepath.Join(project, ProjectFileName), `{
	  "mcpServers": {
	    "fs": {"command": "./local-fs", "allowTools": ["read_*"]},
	    "gone": {"disabled": true},
	    "extra": {"command": "x", "somethingUnknown": 1}
	  },
	  "settings": {"cacheTTL": "2h"},
	  "otherTool": {"keep": true}
	}`)

	cfg, err := Load(filepath.Join(project, "sub", "dir"))
	must(t, err)
	if cfg.ProjectFile != filepath.Join(project, ProjectFileName) {
		t.Fatalf("project file = %q", cfg.ProjectFile)
	}
	if cfg.WorkspaceRoot != project {
		t.Errorf("workspace root = %q, want %q", cfg.WorkspaceRoot, project)
	}
	if got := strings.Join(cfg.Names(), ","); got != "extra,fs,github,gone" {
		t.Errorf("names = %s", got)
	}
	fs := cfg.Servers["fs"]
	if fs.Scope != ScopeProject || fs.Server.Command != "./local-fs" || len(fs.Server.Args) != 0 {
		t.Errorf("project must replace the user entry entirely, got %+v", fs)
	}
	if cfg.Servers["github"].Scope != ScopeUser {
		t.Error("github should come from the user file")
	}
	if !cfg.Servers["gone"].Server.Disabled {
		t.Error("gone should be disabled by the project")
	}
	if cfg.Settings.CallTimeout.D() != 30*time.Second || cfg.Settings.CacheTTL.D() != 2*time.Hour {
		t.Errorf("settings merge: %+v", cfg.Settings)
	}

	_, err = cfg.Resolve("gone", lookupFrom(nil))
	var dis *DisabledServerError
	if !errors.As(err, &dis) {
		t.Errorf("want DisabledServerError, got %v", err)
	}
	_, err = cfg.Resolve("nope", lookupFrom(nil))
	var unk *UnknownServerError
	if !errors.As(err, &unk) {
		t.Errorf("want UnknownServerError, got %v", err)
	}
}

func TestResolve(t *testing.T) {
	userDir, project := setup(t)
	write(t, filepath.Join(userDir, UserFileName), `{"mcpServers": {
	  "gh": {"url": "https://x/${ORG}", "headers": {"Authorization": "Bearer ${GH}"}, "timeout": "5s"},
	  "st": {"command": "${BIN:-node}", "args": ["--root", "${HOME_DIR}"], "env": {"K": "${V}"}, "cwd": "rel", "roots": false}
	}}`)
	cfg, err := Load(project)
	must(t, err)

	gh, err := cfg.Resolve("gh", lookupFrom(map[string]string{"ORG": "acme", "GH": "t0k"}))
	must(t, err)
	if gh.Transport != TransportHTTP || gh.URL != "https://x/acme" || gh.Headers["Authorization"] != "Bearer t0k" {
		t.Errorf("gh = %+v", gh)
	}
	if gh.CallTimeout != 5*time.Second || gh.StartupTimeout != DefaultStartupTimeout {
		t.Errorf("timeouts = %v %v", gh.CallTimeout, gh.StartupTimeout)
	}
	if len(gh.Roots) != 1 || gh.Roots[0] != project {
		t.Errorf("roots = %v", gh.Roots)
	}
	if gh.Describe() != "https://x/acme" || strings.Contains(gh.Describe(), "t0k") {
		t.Errorf("describe leaks or is wrong: %q", gh.Describe())
	}

	_, err = cfg.Resolve("gh", lookupFrom(map[string]string{"ORG": "acme"}))
	var mv *MissingVarError
	if !errors.As(err, &mv) || mv.Var != "GH" {
		t.Errorf("want missing GH, got %v", err)
	}

	st, err := cfg.Resolve("st", lookupFrom(map[string]string{"HOME_DIR": "/h", "V": "v"}))
	must(t, err)
	if st.Command != "node" || st.Args[1] != "/h" || st.Env["K"] != "v" || st.Roots != nil {
		t.Errorf("st = %+v", st)
	}
	if st.Cwd != filepath.Join(userDir, "rel") {
		t.Errorf("relative cwd should resolve against the config file dir, got %q", st.Cwd)
	}

	// The ID changes with secrets and stays stable otherwise.
	a, _ := cfg.Resolve("gh", lookupFrom(map[string]string{"ORG": "acme", "GH": "1"}))
	b, _ := cfg.Resolve("gh", lookupFrom(map[string]string{"ORG": "acme", "GH": "1"}))
	c, _ := cfg.Resolve("gh", lookupFrom(map[string]string{"ORG": "acme", "GH": "2"}))
	if a.ID != b.ID || a.ID == c.ID {
		t.Errorf("IDs: %s %s %s", a.ID, b.ID, c.ID)
	}
}

func TestFindProjectFileStopsAtGitRoot(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ProjectFileName), `{}`)
	repo := filepath.Join(root, "repo")
	must(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
	must(t, os.MkdirAll(filepath.Join(repo, "a"), 0o755))
	if got := FindProjectFile(filepath.Join(repo, "a")); got != "" {
		t.Errorf("search crossed the git root: %q", got)
	}
	if got := FindProjectFile(root); got != filepath.Join(root, ProjectFileName) {
		t.Errorf("got %q", got)
	}
}

func TestLoadBadJSON(t *testing.T) {
	userDir, project := setup(t)
	write(t, filepath.Join(userDir, UserFileName), `{"mcpServers": {`)
	_, err := Load(project)
	var fe *FileError
	if !errors.As(err, &fe) {
		t.Fatalf("want FileError, got %v", err)
	}
}

func TestLiteralSecretsWarning(t *testing.T) {
	_, project := setup(t)
	write(t, filepath.Join(project, ProjectFileName), `{"mcpServers": {
	  "a": {"command": "x", "env": {"API_KEY": "plain", "OTHER": "ok", "GH_TOKEN": "${GH_TOKEN}"}},
	  "b": {"url": "https://x", "headers": {"Authorization": "Bearer abc"}}
	}}`)
	cfg, err := Load(project)
	must(t, err)
	if len(cfg.Warnings) != 2 {
		t.Fatalf("warnings = %v", cfg.Warnings)
	}
	if got := LiteralSecrets(&cfg.Servers["a"].Server); len(got) != 1 || got[0] != "env.API_KEY" {
		t.Errorf("literal secrets = %v", got)
	}
}

func TestAddRemovePreservesUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".mcp.json")
	write(t, path, `{"mcpServers": {"keep": {"command": "k", "custom": 1}}, "other": [1, 2]}`)

	must(t, AddServer(path, "new", Server{Command: "npx", Args: []string{"-y", "pkg"}}, false))
	if err := AddServer(path, "new", Server{Command: "x"}, false); err == nil {
		t.Error("duplicate add without replace should fail")
	}
	must(t, AddServer(path, "new", Server{Command: "y"}, true))
	if err := AddServer(path, "list", Server{Command: "x"}, false); err == nil {
		t.Error("reserved name should be refused")
	}
	b, _ := os.ReadFile(path)
	s := string(b)
	for _, want := range []string{`"other"`, `"custom": 1`, `"command": "y"`} {
		if !strings.Contains(s, want) {
			t.Errorf("file lost %s:\n%s", want, s)
		}
	}
	ok, err := RemoveServer(path, "new")
	must(t, err)
	if !ok {
		t.Error("remove should report the server was there")
	}
	servers, err := ReadServers(path)
	must(t, err)
	if _, ok := servers["new"]; ok || len(servers) != 1 {
		t.Errorf("servers after remove = %v", servers)
	}
}

func TestTrust(t *testing.T) {
	_, project := setup(t)
	write(t, filepath.Join(project, ProjectFileName), `{"mcpServers": {"p": {"command": "x"}}}`)
	cfg, err := Load(project)
	must(t, err)
	ts, err := LoadTrust()
	must(t, err)
	e := cfg.Servers["p"]
	var ue *UntrustedError
	if !errors.As(ts.Check(e), &ue) {
		t.Fatal("project server should start untrusted")
	}
	ts.Trust(e)
	must(t, ts.Save())

	ts2, err := LoadTrust()
	must(t, err)
	if ts2.Check(e) != nil {
		t.Error("trust was not persisted")
	}
	changed := *e
	changed.Server.Args = []string{"--evil"}
	if ts2.Check(&changed) == nil {
		t.Error("a changed definition must need trust again")
	}
	user := &Entry{Name: "u", Scope: ScopeUser, Server: Server{Command: "x"}}
	if ts2.Check(user) != nil {
		t.Error("user servers are always trusted")
	}
}

func TestDurationJSON(t *testing.T) {
	var s Settings
	must(t, jsonUnmarshal(`{"callTimeout": "1m30s", "cacheTTL": 2.5}`, &s))
	if s.CallTimeout.D() != 90*time.Second || s.CacheTTL.D() != 2500*time.Millisecond {
		t.Errorf("%+v", s)
	}
	if err := jsonUnmarshal(`{"callTimeout": "soon"}`, &s); err == nil {
		t.Error("want error for a bad duration")
	}
}
