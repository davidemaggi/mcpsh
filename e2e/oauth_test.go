package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOAuthLogin(t *testing.T) {
	eachMode(t, func(t *testing.T, e *env) {
		// Access tokens live 3s: the second call below needs a refresh.
		url := startRemote(t, "--http", "--oauth", "--oauth-ttl", "3s")
		e.userConfig(map[string]any{"remote": map[string]any{"url": url}})
		// The test server doubles as the browser: it follows the redirect back to mcpsh.
		e.vars["BROWSER"] = serverBin

		// Without a login, calls fail with a usage error that says what to do.
		expect(t, e.run("remote", "echo", "--text", "x"), 2, "", `mcpsh auth login remote`)
		expect(t, e.run("auth", "status"), 0, "not logged in", "")

		r := e.run("auth", "login", "remote")
		expect(t, r, 0, "Logged in to \"remote\"", "Opening the browser")
		expect(t, e.run("auth", "status"), 0, "logged in (refreshed automatically)", "")
		expect(t, e.run("remote", "echo", "--text", "authorized"), 0, "authorized", "")

		// The stored login is not a plain-text token on Windows (DPAPI) and private elsewhere.
		files, _ := filepath.Glob(filepath.Join(e.vars["MCPSH_STATE_DIR"], "auth", "*.json"))
		if len(files) != 1 {
			t.Fatalf("expected one stored login, got %v", files)
		}
		b, _ := os.ReadFile(files[0])
		if json.Valid(b) == (os.PathSeparator == '\\') {
			t.Errorf("stored login: unexpected format (valid JSON: %v)", json.Valid(b))
		}
		if strings.Contains(string(b), "access-") && os.PathSeparator == '\\' {
			t.Errorf("token in clear text on Windows")
		}

		time.Sleep(4 * time.Second)
		expect(t, e.run("remote", "echo", "--text", "refreshed"), 0, "refreshed", "")

		expect(t, e.run("auth", "logout", "remote"), 0, "Logged out", "")
		expect(t, e.run("remote", "echo", "--text", "x"), 2, "", `mcpsh auth login remote`)
		expect(t, e.run("auth", "logout", "remote"), 0, "Not logged in", "")
	})
}

func TestOAuthNotApplicable(t *testing.T) {
	e := newEnv(t, "direct")
	e.userConfig(map[string]any{
		"local":  ts(nil),
		"static": map[string]any{"url": "http://127.0.0.1:1/mcp", "headers": map[string]any{"Authorization": "Bearer ${TOKEN}"}},
	})
	e.vars["TOKEN"] = "t"
	expect(t, e.run("auth", "login", "local"), 2, "", "Streamable HTTP servers only")
	expect(t, e.run("auth", "login", "static"), 2, "", "OAuth is off")
	expect(t, e.run("auth", "status"), 0, "No server can use OAuth", "")
}
