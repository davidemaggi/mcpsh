package e2e

import "testing"

// Mistakes that "mcpsh add" used to accept silently and that only showed up on the first call.
func TestAddRejectsMistakes(t *testing.T) {
	e := newEnv(t, "direct")
	e.userConfig(map[string]any{})

	// A stray word after --url: the real URL ended up as an ignored argument.
	expect(t, e.run("add", "deck", "--url", "macro-deck", "http://192.168.1.10:8193/mcp"), 2, "", `unexpected argument "http://192.168.1.10:8193/mcp"`)
	// Not a URL at all.
	expect(t, e.run("add", "deck", "--url", "macro-deck"), 2, "", "must be an absolute http:// or https:// URL")
	// The shell expanded ${TOKEN} to nothing inside double quotes.
	expect(t, e.run("add", "deck", "--url", "http://127.0.0.1:1/mcp", "--header", "Authorization: Bearer "), 2, "", "has no credential")
	expect(t, e.run("list"), 0, "No servers configured", "")

	// The right forms still work: a ${VAR} URL is checked when it is resolved, not here.
	expect(t, e.run("add", "deck", "--url", "http://127.0.0.1:1/mcp", "--header", "Authorization: Bearer ${DECK_TOKEN}"), 0, "Added http server", "")
	expect(t, e.run("add", "env-url", "--url", "${DECK_URL}"), 0, "Added http server", "")
}
