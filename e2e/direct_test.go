package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/davidemaggi/mcpsh/internal/mcpclient"
	"github.com/davidemaggi/mcpsh/internal/proc"
)

func TestDiscoveryAndCache(t *testing.T) {
	eachMode(t, func(t *testing.T, e *env) {
		failFlag := filepath.Join(e.root, "broken")
		e.userConfig(map[string]any{"ts": ts(map[string]any{"env": map[string]any{"MCPSH_TESTSERVER_FAIL_IF_EXISTS": failFlag}})})

		expect(t, e.run("list"), 0, "ts", "")
		r := e.run("list", "-o", "json")
		var infos []map[string]any
		if err := json.Unmarshal([]byte(r.stdout), &infos); err != nil || len(infos) != 1 || infos[0]["tools"] != nil {
			t.Fatalf("list before discovery: %v %s", err, r)
		}

		expect(t, e.run("ts"), 0, "echo", "")
		expect(t, e.run("ts", "--help"), 0, "inspect_args", "")
		r = e.run("ts", "-o", "json")
		var tools []map[string]any
		if err := json.Unmarshal([]byte(r.stdout), &tools); err != nil || len(tools) < 10 {
			t.Fatalf("tools json: %v %s", err, r)
		}

		// From now on the server cannot start: help and listings must come from the cache.
		if err := os.WriteFile(failFlag, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		e.stopDaemon() // a warm session would keep answering
		expect(t, e.run("ts", "--help"), 0, "cached", "")
		expect(t, e.run("ts", "echo", "--help"), 0, "--text string", "")
		expect(t, e.run("list"), 0, strconv.Itoa(len(tools)), "")
		// A call needs the server: connection error, exit 3, with the server's stderr.
		expect(t, e.run("ts", "echo", "--text", "x"), 3, "", "refusing to start")
		// list --check reports it.
		expect(t, e.run("list", "--check"), 0, "error", "")
		// refresh fails with exit 3 and keeps the old cache usable.
		expect(t, e.run("refresh", "ts"), 3, "", "ts:")
		os.Remove(failFlag)
		expect(t, e.run("refresh"), 0, "ts: "+strconv.Itoa(len(tools))+" tools", "")
	})
}

func TestCallsAndExitCodes(t *testing.T) {
	eachMode(t, func(t *testing.T, e *env) {
		e.userConfig(map[string]any{"ts": ts(nil)})
		cases := []struct {
			name           string
			args           []string
			code           int
			stdout, stderr string
		}{
			{"echo", []string{"ts", "echo", "--text", "hello world"}, 0, "hello world", ""},
			{"bool flag", []string{"ts", "echo", "--text", "hi", "--upper"}, 0, "HI", ""},
			{"call form", []string{"call", "ts", "echo", "--text", "explicit"}, 0, "explicit", ""},
			{"kebab tool alias", []string{"ts", "inspect-args", "--repo", "r"}, 0, `"repo":"r"`, ""},
			{"typed flags", []string{"ts", "inspect_args", "--repo", "a/b", "--labels", "x", "--labels", "y",
				"--count", "2", "--ratio", "0.5", "--draft", "--meta", `{"k":1}`, "--page-size", "3", "--param-output", "o"},
				0, `{"count":2,"draft":true,"labels":["x","y"],"meta":{"k":1},"output":"o","pageSize":3,"ratio":0.5,"repo":"a/b"}`, ""},
			{"structured only", []string{"ts", "add", "--a", "2", "--b", "3"}, 0, `"sum":5`, ""},
			{"isError", []string{"ts", "fail", "--message", "boom"}, 1, "", "boom"},
			{"missing required", []string{"ts", "inspect_args"}, 2, "", "missing required parameter --repo (see mcpsh ts inspect_args --help)"},
			{"bad enum", []string{"ts", "inspect_args", "--repo", "r", "--state", "merged"}, 2, "", "must be one of: open, closed"},
			{"bad integer", []string{"ts", "inspect_args", "--repo", "r", "--count", "two"}, 2, "", "expected an integer"},
			{"schema validation", []string{"ts", "inspect_args", "--repo", "r", "--count", "0"}, 2, "", "--count: minimum: 0 is less than 1"},
			{"unknown flag", []string{"ts", "echo", "--nope", "1"}, 2, "", "unknown flag: --nope"},
			{"positional", []string{"ts", "echo", "stray"}, 2, "", `unexpected argument "stray"`},
			{"unknown tool", []string{"ts", "ech"}, 2, "", `has no tool "ech"; did you mean echo?`},
			{"unknown server", []string{"nope"}, 2, "", `unknown server "nope"`},
			{"bad output", []string{"-o", "yaml", "ts", "echo", "--text", "x"}, 2, "", "--output must be text or json"},
			{"timeout", []string{"ts", "slow", "--ms", "5000", "--timeout", "500ms"}, 3, "", "did not answer in time"},
			{"crash", []string{"ts", "crash"}, 3, "", "server process exited"},
			{"destructive refused", []string{"ts", "destroy", "--target", "db"}, 2, "", "re-run with --yes"},
			{"destructive confirmed", []string{"ts", "destroy", "--target", "db", "--yes"}, 0, "destroyed db", ""},
			{"schema flag", []string{"ts", "add", "--schema"}, 0, `"outputSchema"`, ""},
			{"version", []string{"version"}, 0, "mcpsh", ""},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				expect(t, e.run(c.args...), c.code, c.stdout, c.stderr)
			})
		}
	})
}

func TestJSONInputAndOutput(t *testing.T) {
	eachMode(t, func(t *testing.T, e *env) {
		e.userConfig(map[string]any{"ts": ts(nil)})
		expect(t, e.run("ts", "echo", "--json", `{"text":"inline"}`), 0, "inline", "")
		f := filepath.Join(e.root, "args.json")
		if err := os.WriteFile(f, []byte(`{"text":"from file"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		expect(t, e.run("ts", "echo", "--json", "@"+f), 0, "from file", "")
		expect(t, e.runStdin(`{"text":"from stdin"}`, "ts", "echo", "--json", "-"), 0, "from stdin", "")
		expect(t, e.run("ts", "echo", "--json", `{"text":"json"}`, "--text", "flag wins"), 0, "flag wins", "")
		expect(t, e.run("ts", "echo", "--json", `[1]`), 2, "", "must be a JSON object")

		r := e.run("ts", "add", "--a", "1", "--b", "2", "-o", "json")
		var res struct {
			Content           []map[string]any `json:"content"`
			StructuredContent map[string]any   `json:"structuredContent"`
			IsError           bool             `json:"isError"`
		}
		if err := json.Unmarshal([]byte(r.stdout), &res); err != nil || res.StructuredContent["sum"] != 3.0 || r.code != 0 {
			t.Fatalf("json output: %v %s", err, r)
		}
		r = e.run("ts", "fail", "-o", "json")
		if r.code != 1 || !strings.Contains(r.stdout, `"isError": true`) {
			t.Errorf("isError in json mode: %s", r)
		}
	})
}

func TestImageSavedToFile(t *testing.T) {
	eachMode(t, func(t *testing.T, e *env) {
		e.userConfig(map[string]any{"ts": ts(nil)})
		r := e.run("ts", "image")
		expect(t, r, 0, "here is an image", "")
		i := strings.Index(r.stdout, "saved to ")
		if i < 0 {
			t.Fatalf("no file path: %s", r)
		}
		p := strings.TrimSuffix(strings.TrimSpace(r.stdout[i+len("saved to "):]), "]")
		b, err := os.ReadFile(p)
		if err != nil || !strings.HasPrefix(string(b), "\x89PNG") || !strings.HasSuffix(p, ".png") {
			t.Errorf("image file %q: %v", p, err)
		}
		os.Remove(p)
	})
}

func TestEnvInterpolation(t *testing.T) {
	eachMode(t, func(t *testing.T, e *env) {
		e.userConfig(map[string]any{"ts": ts(map[string]any{"env": map[string]any{"SECRET_TOKEN": "${E2E_TOKEN}", "GREETING": "${E2E_UNSET:-hello}"}})})
		expect(t, e.run("ts", "echo", "--text", "x"), 2, "", "environment variable E2E_TOKEN is not set")
		e.vars["E2E_TOKEN"] = "s3cr3t"
		expect(t, e.run("ts", "env", "--name", "SECRET_TOKEN"), 0, "s3cr3t", "")
		expect(t, e.run("ts", "env", "--name", "GREETING"), 0, "hello", "")
		// The secret never appears in listings.
		r := e.run("list", "-o", "json")
		if strings.Contains(r.stdout, "s3cr3t") {
			t.Errorf("list leaked the secret: %s", r)
		}
	})
}

func TestProjectTrust(t *testing.T) {
	eachMode(t, func(t *testing.T, e *env) {
		p := e.projectConfig(map[string]any{"proj": ts(nil)})
		expect(t, e.run("proj", "echo", "--text", "x"), 2, "", "has not been trusted yet")
		expect(t, e.run("list"), 0, "untrusted", "")
		expect(t, e.run("trust"), 0, "proj (from", "")
		expect(t, e.run("trust", "proj"), 0, "Trusted", "")
		expect(t, e.run("proj", "echo", "--text", "trusted now"), 0, "trusted now", "")

		// Changing the definition needs a new approval.
		e.writeJSON(p, map[string]any{"mcpServers": map[string]any{"proj": ts(map[string]any{"args": []string{"--startup-delay", "0s"}})}})
		expect(t, e.run("proj", "echo", "--text", "x"), 2, "", "has not been trusted yet")
		expect(t, e.run("trust", "--all"), 0, "Trusted", "")
		expect(t, e.run("proj", "echo", "--text", "ok"), 0, "ok", "")
		// Works from a subdirectory too.
		sub := filepath.Join(e.project, "a", "b")
		mustMkdir(t, sub)
		expect(t, e.runIn(sub, "", "proj", "echo", "--text", "deep"), 0, "deep", "")
	})
}

func TestAddRemoveImport(t *testing.T) {
	eachMode(t, func(t *testing.T, e *env) {
		expect(t, e.run("add", "one", "--", serverBin), 0, `Added stdio server "one"`, "")
		expect(t, e.run("one", "echo", "--text", "added"), 0, "added", "")
		expect(t, e.run("add", "one", "--", serverBin), 2, "", "already exists")
		expect(t, e.run("add", "list", "--", serverBin), 2, "", "built-in mcpsh command")
		expect(t, e.run("add", "x"), 2, "", `give a command after "--" or a --url`)

		// Project scope refuses clear-text secrets, accepts references, and trusts what it writes.
		expect(t, e.run("add", "p", "--scope", "project", "--env", "API_KEY=plain", "--", serverBin), 2, "", "env.API_KEY")
		expect(t, e.run("add", "p", "--scope", "project", "--env", "API_KEY=${E2E_KEY:-k}", "--", serverBin), 0, "(project)", "")
		expect(t, e.run("p", "env", "--name", "API_KEY"), 0, "k", "")

		expect(t, e.run("remove", "one"), 0, "Removed", "")
		expect(t, e.run("one"), 2, "", "unknown server")

		// Import a Claude-style .mcp.json.
		e.writeJSON(filepath.Join(e.project, ".mcp.json"), map[string]any{"mcpServers": map[string]any{
			"imported": map[string]any{"command": serverBin},
			"p":        map[string]any{"command": "other"},
			"leaky":    map[string]any{"command": serverBin, "env": map[string]any{"GITHUB_TOKEN": "ghp_x"}},
		}})
		r := e.run("import")
		expect(t, r, 0, "+ imported", "")
		if !strings.Contains(r.stdout, "skipped p (exists") || !strings.Contains(r.stdout, "skipped leaky (clear-text secrets") {
			t.Errorf("import report: %s", r)
		}
		expect(t, e.run("imported", "echo", "--text", "via import"), 0, "via import", "")
	})
}

func TestRootsAndWorkingDirectory(t *testing.T) {
	eachMode(t, func(t *testing.T, e *env) {
		e.userConfig(map[string]any{"ts": ts(nil), "noroots": ts(map[string]any{"roots": false})})
		sub := filepath.Join(e.project, "sub")
		mustMkdir(t, sub)
		r := e.runIn(sub, "", "ts", "roots")
		if r.code != 0 || strings.TrimSpace(r.stdout) != mcpclient.FileURI(e.project) {
			t.Errorf("roots should be the project root %s: %s", mcpclient.FileURI(e.project), r)
		}
		r = e.runIn(sub, "", "ts", "cwd")
		if got := strings.TrimSpace(r.stdout); !sameDir(got, e.project) {
			t.Errorf("cwd = %q, want the project root %q", got, e.project)
		}
		r = e.run("noroots", "roots")
		if r.code != 0 || strings.TrimSpace(r.stdout) != "" {
			t.Errorf("roots disabled: %s", r)
		}
	})
}

func sameDir(a, b string) bool {
	ai, err1 := os.Stat(a)
	bi, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(ai, bi)
}

func TestToolFilters(t *testing.T) {
	eachMode(t, func(t *testing.T, e *env) {
		e.userConfig(map[string]any{"ts": ts(map[string]any{"allowTools": []string{"e*", "add"}, "denyTools": []string{"env"}})})
		r := e.run("ts", "-o", "json")
		var tools []struct{ Name string }
		_ = json.Unmarshal([]byte(r.stdout), &tools)
		var names []string
		for _, tl := range tools {
			names = append(names, tl.Name)
		}
		if strings.Join(names, ",") != "add,echo" {
			t.Errorf("filtered tools = %v (%s)", names, r)
		}
		expect(t, e.run("ts", "pid"), 2, "", `has no tool "pid"`)
	})
}

func TestNoOrphanProcesses(t *testing.T) {
	eachMode(t, func(t *testing.T, e *env) {
		e.userConfig(map[string]any{"ts": ts(nil)})
		r := e.run("ts", "spawn_child")
		pid, err := strconv.Atoi(strings.TrimSpace(r.stdout))
		if err != nil || r.code != 0 {
			t.Fatalf("spawn_child: %s", r)
		}
		e.stopDaemon() // the daemon keeps the server (and its child) until it stops
		deadline := time.Now().Add(10 * time.Second)
		for proc.Alive(pid) && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
		if proc.Alive(pid) {
			t.Errorf("grandchild %d outlived mcpsh", pid)
		}
	})
}

func TestWindowsBatchShim(t *testing.T) {
	eachMode(t, func(t *testing.T, e *env) {
		if runtime.GOOS != "windows" {
			t.Skip("Windows only")
		}
		shim := filepath.Join(e.root, "bin dir", "server.cmd")
		mustMkdir(t, filepath.Dir(shim))
		if err := os.WriteFile(shim, []byte("@echo off\r\n\""+serverBin+"\" %*\r\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		e.userConfig(map[string]any{"shim": map[string]any{"command": shim, "args": []string{"--startup-delay", "0s"}}})
		expect(t, e.run("shim", "echo", "--text", `a&b | "c" %PATH% ^d`), 0, `a&b | "c" %PATH% ^d`, "")
		r := e.run("shim", "spawn_child")
		pid, err := strconv.Atoi(strings.TrimSpace(r.stdout))
		if err != nil {
			t.Fatalf("spawn_child through the shim: %s", r)
		}
		e.stopDaemon()
		deadline := time.Now().Add(10 * time.Second)
		for proc.Alive(pid) && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
		if proc.Alive(pid) {
			t.Errorf("grandchild %d behind a .cmd shim outlived mcpsh", pid)
		}
	})
}
