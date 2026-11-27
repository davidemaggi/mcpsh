package e2e

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

func TestResourcesAndPrompts(t *testing.T) {
	eachMode(t, func(t *testing.T, e *env) {
		e.userConfig(map[string]any{"ts": ts(nil)})

		// Resources: list, read text, read a templated URI, read binary to a file.
		expect(t, e.run("resources", "ts"), 0, "test://readme", "")
		expect(t, e.run("resources", "ts"), 0, "test://items/{id}", "")
		r := e.run("resources", "ts", "-o", "json")
		var list struct {
			Resources []map[string]any `json:"resources"`
			Templates []map[string]any `json:"resourceTemplates"`
		}
		if err := json.Unmarshal([]byte(r.stdout), &list); err != nil || len(list.Resources) != 2 || len(list.Templates) != 1 {
			t.Fatalf("resources json: %v %s", err, r)
		}
		expect(t, e.run("resources", "ts", "test://readme"), 0, "# mcpsh test server", "")
		expect(t, e.run("resources", "ts", "test://items/42"), 0, "item 42", "")
		r = e.run("resources", "ts", "test://logo")
		expect(t, r, 0, "saved to", "")
		m := regexp.MustCompile(`saved to (\S+\.png)`).FindStringSubmatch(r.stdout)
		if m == nil {
			t.Fatalf("no file in %s", r)
		}
		if b, err := os.ReadFile(m[1]); err != nil || len(b) < 8 || string(b[1:4]) != "PNG" {
			t.Fatalf("saved file is not a PNG: %v", err)
		}
		os.Remove(m[1])
		// The server answers "invalid params" (-32602): a usage error, exit 2
		expect(t, e.run("resources", "ts", "test://missing"), 2, "", "Resource not found")
		expect(t, e.run("resources"), 2, "", "")

		// Prompts: list, get with arguments, validation before the call.
		expect(t, e.run("prompts", "ts"), 0, "greet", "")
		expect(t, e.run("prompts", "ts"), 0, "name*, style", "")
		expect(t, e.run("prompts", "ts", "greet", "--arg", "name=Ada", "-a", "style=formal"), 0, "Good morning, Ada!", "")
		expect(t, e.run("prompts", "ts", "greet", "--arg", "name=Ada"), 0, "[assistant]", "")
		expect(t, e.run("prompts", "ts", "plain"), 0, "plain prompt", "")
		r = e.run("prompts", "ts", "greet", "-a", "name=Ada", "-o", "json")
		var got struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.Unmarshal([]byte(r.stdout), &got); err != nil || len(got.Messages) != 2 {
			t.Fatalf("prompt json: %v %s", err, r)
		}
		expect(t, e.run("prompts", "ts", "greet"), 2, "", "needs --arg name=")
		expect(t, e.run("prompts", "ts", "greet", "--arg", "name"), 2, "", "must be name=value")
		expect(t, e.run("prompts", "ts", "greet", "--arg", "name=x", "--arg", "mood=y"), 2, "", `has no argument "mood"`)
		expect(t, e.run("prompts", "ts", "nope"), 2, "", `has no prompt "nope"`)
	})
}
