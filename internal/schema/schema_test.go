package schema

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

const issueSchema = `{
  "type": "object",
  "properties": {
    "repo":         {"type": "string", "description": "Repository, owner/name"},
    "title":        {"type": "string"},
    "issueNumber":  {"type": "integer"},
    "weight":       {"type": "number", "default": 1.5},
    "draft":        {"type": "boolean"},
    "state":        {"type": "string", "enum": ["open", "closed"], "default": "open"},
    "labels":       {"type": "array", "items": {"type": "string"}},
    "ids":          {"type": "array", "items": {"type": "integer"}},
    "meta":         {"type": "object", "properties": {"a": {"type": "string"}}},
    "maybe":        {"anyOf": [{"type": "string"}, {"type": "null"}]},
    "nullable":     {"type": ["integer", "null"]},
    "level":        {"$ref": "#/$defs/Level"},
    "output":       {"type": "string", "description": "collides with --output"},
    "free":         {}
  },
  "required": ["repo", "title"],
  "$defs": {"Level": {"type": "string", "enum": ["low", "high"]}}
}`

func build(t *testing.T, s string) *Spec {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	spec, err := Build(m)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestBuildKinds(t *testing.T) {
	s := build(t, issueSchema)
	want := map[string]struct {
		flag string
		kind Kind
		item Kind
		enum string
		req  bool
	}{
		"repo":        {"repo", KindString, 0, "", true},
		"title":       {"title", KindString, 0, "", true},
		"issueNumber": {"issueNumber", KindInteger, 0, "", false},
		"weight":      {"weight", KindNumber, 0, "", false},
		"draft":       {"draft", KindBoolean, 0, "", false},
		"state":       {"state", KindString, 0, "open,closed", false},
		"labels":      {"labels", KindArray, KindString, "", false},
		"ids":         {"ids", KindArray, KindInteger, "", false},
		"meta":        {"meta", KindJSON, 0, "", false},
		"maybe":       {"maybe", KindString, 0, "", false},
		"nullable":    {"nullable", KindInteger, 0, "", false},
		"level":       {"level", KindString, 0, "low,high", false},
		"output":      {"param-output", KindString, 0, "", false},
		"free":        {"free", KindJSON, 0, "", false},
	}
	if len(s.Params) != len(want) {
		t.Fatalf("got %d params", len(s.Params))
	}
	if s.Params[0].Name != "repo" || s.Params[1].Name != "title" {
		t.Errorf("required params must come first: %s, %s", s.Params[0].Name, s.Params[1].Name)
	}
	for name, w := range want {
		p := s.Param(name)
		if p == nil {
			t.Errorf("%s missing", name)
			continue
		}
		if p.Flag != w.flag || p.Kind != w.kind || (w.kind == KindArray && p.ItemKind != w.item) ||
			strings.Join(p.Enum, ",") != w.enum || p.Required != w.req {
			t.Errorf("%s = %+v, want %+v", name, p, w)
		}
	}
	if p := s.Param("issueNumber"); p.Alias != "issue-number" {
		t.Errorf("alias = %q", p.Alias)
	}
	if p := s.Param("state"); p.Default != `"open"` {
		t.Errorf("default = %q", p.Default)
	}
}

func parse(t *testing.T, s *Spec, args ...string) (map[string]any, error) {
	t.Helper()
	fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
	b := s.Bind(fs)
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	return b.Args(nil), nil
}

func TestFlagsToArgs(t *testing.T) {
	s := build(t, issueSchema)
	got, err := parse(t, s,
		"--repo", "a/b", "--title=Hello world", "--issue-number", "42", "--weight", "2.5", "--draft",
		"--state", "closed", "--labels", "bug", "--labels", "ui", "--ids", "[1,2]", "--ids", "3",
		"--meta", `{"a":"x"}`, "--param-output", "o", "--free", "word", "--level", "high")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"repo": "a/b", "title": "Hello world", "issueNumber": int64(42), "weight": 2.5, "draft": true,
		"state": "closed", "labels": []any{"bug", "ui"}, "ids": []any{float64(1), float64(2), int64(3)},
		"meta": map[string]any{"a": "x"}, "output": "o", "free": "word", "level": "high",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args =\n%#v\nwant\n%#v", got, want)
	}
	if err := s.Validate(got); err != nil {
		t.Errorf("valid args rejected: %v", err)
	}
}

func TestFlagErrors(t *testing.T) {
	s := build(t, issueSchema)
	for _, args := range [][]string{
		{"--issueNumber", "x"},
		{"--weight", "heavy"},
		{"--draft=maybe"},
		{"--state", "merged"},
		{"--meta", "{bad"},
		{"--level", "medium"},
		{"--nope", "1"},
	} {
		if _, err := parse(t, s, args...); err == nil {
			t.Errorf("%v: want error", args)
		}
	}
	got, err := parse(t, s, "--draft=false")
	if err != nil || got["draft"] != false {
		t.Errorf("--draft=false: %v %v", got, err)
	}
}

func TestJSONBaseAndRequired(t *testing.T) {
	s := build(t, issueSchema)
	fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
	b := s.Bind(fs)
	if err := fs.Parse([]string{"--title", "from flag"}); err != nil {
		t.Fatal(err)
	}
	args := b.Args(map[string]any{"title": "from json", "meta": map[string]any{"a": "1"}})
	if args["title"] != "from flag" || args["meta"] == nil {
		t.Errorf("flags must override --json keys: %v", args)
	}
	miss := s.Missing(args)
	if len(miss) != 1 || miss[0].Name != "repo" {
		t.Errorf("missing = %v", miss)
	}
}

func TestValidate(t *testing.T) {
	s := build(t, `{"type":"object","properties":{"n":{"type":"integer","minimum":1},"tags":{"type":"array","items":{"type":"string"}}},"additionalProperties":false}`)
	if err := s.Validate(map[string]any{"n": int64(0)}); err == nil {
		t.Error("minimum not enforced")
	}
	if err := s.Validate(map[string]any{"n": int64(2), "x": 1}); err == nil {
		t.Error("additionalProperties not enforced")
	}
	if err := s.Validate(map[string]any{"n": int64(2), "tags": []any{"a"}}); err != nil {
		t.Errorf("valid args rejected: %v", err)
	}
}

func TestNoSchemaAndUnresolvable(t *testing.T) {
	s, err := Build(nil)
	if err != nil || len(s.Params) != 0 || s.Validate(map[string]any{}) != nil {
		t.Errorf("nil schema: %v %v", s, err)
	}
	s = build(t, `{"type":"object","properties":{"x":{"$ref":"https://example.com/remote.json"}}}`)
	if p := s.Param("x"); p == nil || p.Kind != KindJSON {
		t.Errorf("remote ref param = %+v", p)
	}
}

func TestKebab(t *testing.T) {
	for in, want := range map[string]string{
		"issueNumber": "issue-number", "issue_number": "issue-number", "HTTPServer": "http-server",
		"userID": "user-id", "already-kebab": "already-kebab", "x": "x", "getV2Data": "get-v2-data",
	} {
		if got := Kebab(in); got != want {
			t.Errorf("Kebab(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFlagUsages(t *testing.T) {
	u := build(t, issueSchema).FlagUsages()
	for _, want := range []string{
		"--repo string", "Repository, owner/name (required)", "--state open|closed",
		`default "open"`, "--labels string...", "repeatable", "alias --issue-number",
		`--param-output string`, `parameter "output"`,
	} {
		if !strings.Contains(u, want) {
			t.Errorf("usage lacks %q:\n%s", want, u)
		}
	}
}
