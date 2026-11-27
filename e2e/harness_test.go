// Package e2e runs the real mcpsh binary against the real test server.
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

var (
	mcpshBin  string
	serverBin string
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mcpsh-e2e-*")
	if err != nil {
		panic(err)
	}
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	mcpshBin = filepath.Join(dir, "mcpsh"+exe)
	serverBin = filepath.Join(dir, "mcpsh-testserver"+exe)
	for bin, pkg := range map[string]string{mcpshBin: "../cmd/mcpsh", serverBin: "../cmd/mcpsh-testserver"} {
		out, err := exec.Command("go", "build", "-o", bin, pkg).CombinedOutput()
		if err != nil {
			fmt.Fprintf(os.Stderr, "building %s: %v\n%s", pkg, err, out)
			os.Exit(1)
		}
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// env is an isolated mcpsh installation: its own config and state dirs and a project dir.
type env struct {
	t       *testing.T
	root    string
	project string
	vars    map[string]string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	e := &env{t: t, root: root, project: filepath.Join(root, "project"), vars: map[string]string{}}
	mustMkdir(t, filepath.Join(e.project, ".git"))
	e.vars["MCPSH_CONFIG_DIR"] = filepath.Join(root, "config")
	e.vars["MCPSH_STATE_DIR"] = filepath.Join(root, "state")
	e.vars["MCPSH_NO_DAEMON"] = "1"
	return e
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

// userConfig writes the user config file with the given mcpServers.
func (e *env) userConfig(servers map[string]any) {
	e.writeJSON(filepath.Join(e.vars["MCPSH_CONFIG_DIR"], "config.json"), map[string]any{"mcpServers": servers})
}

func (e *env) projectConfig(servers map[string]any) string {
	p := filepath.Join(e.project, ".mcpsh.json")
	e.writeJSON(p, map[string]any{"mcpServers": servers})
	return p
}

func (e *env) writeJSON(path string, v any) {
	e.t.Helper()
	mustMkdir(e.t, filepath.Dir(path))
	b, _ := json.MarshalIndent(v, "", "  ")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// ts is a stdio test server definition.
func ts(extra map[string]any) map[string]any {
	s := map[string]any{"command": serverBin}
	for k, v := range extra {
		s[k] = v
	}
	return s
}

type result struct {
	stdout, stderr string
	code           int
}

func (r result) String() string {
	return fmt.Sprintf("exit %d\n--- stdout\n%s\n--- stderr\n%s", r.code, r.stdout, r.stderr)
}

// run executes mcpsh in the project dir.
func (e *env) run(args ...string) result { return e.runIn(e.project, "", args...) }

func (e *env) runStdin(stdin string, args ...string) result {
	return e.runIn(e.project, stdin, args...)
}

func (e *env) runIn(dir, stdin string, args ...string) result {
	e.t.Helper()
	cmd := exec.Command(mcpshBin, args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	for k, v := range e.vars {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		e.t.Fatal(err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(90 * time.Second):
		_ = cmd.Process.Kill()
		e.t.Fatalf("mcpsh %v did not finish", args)
	}
	return result{stdout: out.String(), stderr: errb.String(), code: cmd.ProcessState.ExitCode()}
}

// expect checks the exit code and that stdout/stderr contain the given fragments.
func expect(t *testing.T, r result, code int, stdout, stderr string) {
	t.Helper()
	if r.code != code || !strings.Contains(r.stdout, stdout) || !strings.Contains(r.stderr, stderr) {
		t.Errorf("want exit %d, stdout containing %q, stderr containing %q; got %s", code, stdout, stderr, r)
	}
}
