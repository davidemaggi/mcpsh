package e2e

import (
	"bufio"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// startRemote runs the test server over HTTP ("--http") or SSE ("--sse") and returns its URL.
func startRemote(t *testing.T, flag string, extra ...string) string {
	t.Helper()
	cmd := exec.Command(serverBin, append([]string{flag, "127.0.0.1:0"}, extra...)...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	lines := make(chan string, 1)
	go func() {
		l, _ := bufio.NewReader(out).ReadString('\n')
		lines <- strings.TrimSpace(l)
	}()
	select {
	case u := <-lines:
		if !strings.HasPrefix(u, "http://") {
			t.Fatalf("test server printed %q", u)
		}
		return u
	case <-time.After(60 * time.Second):
		t.Fatal("test server did not print its URL")
	}
	return ""
}

func TestStreamableHTTP(t *testing.T) {
	eachMode(t, func(t *testing.T, e *env) {
		url := startRemote(t, "--http", "--require-token", "s3cret")
		expect(t, e.run("add", "web", "--url", url, "--header", "Authorization: Bearer ${E2E_TOKEN}"), 0, `Added http server "web"`, "")

		expect(t, e.run("web", "echo", "--text", "x"), 2, "", "environment variable E2E_TOKEN is not set")
		e.vars["E2E_TOKEN"] = "wrong"
		expect(t, e.run("web", "echo", "--text", "x"), 3, "", "Unauthorized")
		e.vars["E2E_TOKEN"] = "s3cret"
		expect(t, e.run("web", "echo", "--text", "over http"), 0, "over http", "")
		expect(t, e.run("web", "header", "--name", "Authorization"), 0, "Bearer s3cret", "")
		expect(t, e.run("web", "header", "--name", "User-Agent"), 0, "mcpsh/", "")
		expect(t, e.run("web", "add", "--a", "1", "--b", "2", "-o", "json"), 0, `"sum": 3`, "")
		expect(t, e.run("web", "fail"), 1, "", "tool failed on purpose")
		expect(t, e.run("web", "slow", "--ms", "5000", "--timeout", "500ms"), 3, "", "did not answer in time")
		expect(t, e.run("list"), 0, "http", "")
		// The token never shows up in listings or (daemon mode) in the log.
		if r := e.run("list", "-o", "json"); strings.Contains(r.stdout, "s3cret") {
			t.Errorf("list leaked the token: %s", r)
		}
		if e.mode == modeDaemon {
			if r := e.run("daemon", "logs", "-n", "1000"); strings.Contains(r.stdout, "s3cret") {
				t.Errorf("daemon log leaked the token:\n%s", r.stdout)
			}
		}
	})
}

func TestLegacySSE(t *testing.T) {
	eachMode(t, func(t *testing.T, e *env) {
		url := startRemote(t, "--sse")
		expect(t, e.run("add", "old", "--url", url, "--transport", "sse"), 0, `Added sse server "old"`, "")
		expect(t, e.run("old", "echo", "--text", "over sse"), 0, "over sse", "")
		expect(t, e.run("old", "inspect-args", "--repo", "r", "--labels", "a"), 0, `"labels":["a"]`, "")
	})
}

func TestUnreachableRemote(t *testing.T) {
	eachMode(t, func(t *testing.T, e *env) {
		e.userConfig(map[string]any{"dead": map[string]any{"url": "http://127.0.0.1:1/mcp", "startupTimeout": "5s"}})
		expect(t, e.run("dead", "echo", "--text", "x"), 3, "", `cannot connect to server "dead"`)
	})
}

// TestToolListChanged: the daemon keeps the session, hears notifications/tools/list_changed and
// refreshes the discovery cache, so a tool added at runtime is callable without "mcpsh refresh".
func TestToolListChanged(t *testing.T) {
	for _, transport := range []string{"stdio", "http"} {
		t.Run(transport, func(t *testing.T) {
			e := newEnv(t, modeDaemon)
			if transport == "http" {
				e.userConfig(map[string]any{"ts": map[string]any{"url": startRemote(t, "--http")}})
			} else {
				e.userConfig(map[string]any{"ts": ts(nil)})
			}
			expect(t, e.run("ts", "echo", "--text", "warm"), 0, "warm", "")
			expect(t, e.run("ts", "mutate"), 0, "tool added", "")
			eventually(t, 20*time.Second, "the cache to list added_tool", func() bool {
				r := e.run("ts", "-o", "json")
				var tools []struct{ Name string }
				_ = json.Unmarshal([]byte(r.stdout), &tools)
				for _, tl := range tools {
					if tl.Name == "added_tool" {
						return true
					}
				}
				return false
			})
			expect(t, e.run("ts", "added-tool"), 0, "added", "")
		})
	}
}
