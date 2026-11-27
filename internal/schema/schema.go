// Package schema maps a tool's JSON Schema (inputSchema) to CLI flags, turns flag values and
// --json input into tool arguments, and validates them.
package schema

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/google/jsonschema-go/jsonschema"
)

// Kind is how a parameter is read from the command line.
type Kind int

const (
	KindJSON    Kind = iota // any JSON value (objects, mixed types, unknown)
	KindString              // plain string
	KindInteger             // int64
	KindNumber              // float64
	KindBoolean             // --flag, --flag=false
	KindArray               // repeatable flag; items of ItemKind
)

// Param is one top-level property of the input schema.
type Param struct {
	Name        string   // property name, sent to the server
	Flag        string   // flag name on the command line
	Alias       string   // kebab-case alias, "" when none
	Kind        Kind     // how the value is parsed
	ItemKind    Kind     // for KindArray
	Enum        []string // allowed values, as typed on the command line
	Required    bool
	Default     string // raw JSON, "" when none
	Description string
}

// TypeHint is the short type shown in help.
func (p *Param) TypeHint() string {
	if len(p.Enum) > 0 && p.Kind != KindArray {
		return strings.Join(p.Enum, "|")
	}
	switch p.Kind {
	case KindString:
		return "string"
	case KindInteger:
		return "int"
	case KindNumber:
		return "number"
	case KindBoolean:
		return ""
	case KindArray:
		item := (&Param{Kind: p.ItemKind, Enum: p.Enum}).TypeHint()
		if item == "" {
			item = "bool"
		}
		return item + "..."
	default:
		return "json"
	}
}

// Spec is the parsed input schema of a tool.
type Spec struct {
	Params   []*Param // sorted: required first, then by name
	raw      *jsonschema.Schema
	resolved *jsonschema.Resolved // nil when the schema cannot be resolved (validation is skipped)
}

// ReservedFlags are global mcpsh flags; colliding parameters get the "param-" prefix.
var ReservedFlags = []string{"help", "output", "json", "timeout", "yes", "schema", "no-daemon"}

// Build parses an inputSchema as delivered by the SDK (usually a map[string]any).
func Build(inputSchema any) (*Spec, error) {
	s := &Spec{}
	if inputSchema == nil {
		s.raw = &jsonschema.Schema{Type: "object"}
	} else {
		b, err := json.Marshal(inputSchema)
		if err != nil {
			return nil, fmt.Errorf("reading inputSchema: %w", err)
		}
		s.raw = &jsonschema.Schema{}
		if err := json.Unmarshal(b, s.raw); err != nil {
			return nil, fmt.Errorf("reading inputSchema: %w", err)
		}
	}
	if rs, err := s.raw.Resolve(nil); err == nil {
		s.resolved = rs
	}

	used := map[string]bool{}
	for _, r := range ReservedFlags {
		used[r] = true
	}
	names := make([]string, 0, len(s.raw.Properties))
	for n := range s.raw.Properties {
		names = append(names, n)
	}
	required := map[string]bool{}
	for _, r := range s.raw.Required {
		required[r] = true
	}
	sort.Slice(names, func(i, j int) bool {
		if required[names[i]] != required[names[j]] {
			return required[names[i]]
		}
		return names[i] < names[j]
	})
	// First pass: primary flag names, so aliases never steal a real name.
	for _, n := range names {
		p := s.param(n, s.raw.Properties[n], required[n])
		p.Flag = flagName(n)
		if used[p.Flag] {
			p.Flag = "param-" + p.Flag
		}
		for used[p.Flag] {
			p.Flag += "_"
		}
		used[p.Flag] = true
		s.Params = append(s.Params, p)
	}
	for _, p := range s.Params {
		if a := Kebab(p.Flag); a != p.Flag && !used[a] {
			p.Alias = a
			used[a] = true
		}
	}
	return s, nil
}

func (s *Spec) param(name string, ps *jsonschema.Schema, required bool) *Param {
	ps = s.deref(ps)
	p := &Param{Name: name, Required: required, Description: oneLine(ps.Description)}
	if p.Description == "" {
		p.Description = oneLine(ps.Title)
	}
	if len(ps.Default) > 0 {
		p.Default = string(ps.Default)
	}
	p.Kind, p.Enum = s.kindOf(ps)
	if p.Kind == KindArray {
		item := &jsonschema.Schema{}
		if ps.Items != nil {
			item = s.deref(ps.Items)
		}
		p.ItemKind, p.Enum = s.kindOf(item)
		if p.ItemKind == KindArray {
			p.ItemKind = KindJSON
		}
	}
	return p
}

// kindOf maps a property schema to a Kind, unwrapping nullable unions.
func (s *Spec) kindOf(ps *jsonschema.Schema) (Kind, []string) {
	ps = s.deref(ps)
	typ := ps.Type
	if typ == "" {
		var nonNull []string
		for _, t := range ps.Types {
			if t != "null" {
				nonNull = append(nonNull, t)
			}
		}
		if len(nonNull) == 1 {
			typ = nonNull[0]
		}
	}
	if typ == "" && len(ps.Types) == 0 {
		// anyOf/oneOf with a single non-null alternative (e.g. Optional[str]).
		var alts []*jsonschema.Schema
		for _, a := range append(slices.Clone(ps.AnyOf), ps.OneOf...) {
			a = s.deref(a)
			if a.Type != "null" {
				alts = append(alts, a)
			}
		}
		if len(alts) == 1 {
			return s.kindOf(alts[0])
		}
	}
	enum := enumStrings(ps.Enum)
	if typ == "" && len(ps.Enum) > 0 {
		typ = enumType(ps.Enum)
	}
	switch typ {
	case "string":
		return KindString, enum
	case "integer":
		return KindInteger, enum
	case "number":
		return KindNumber, enum
	case "boolean":
		return KindBoolean, nil
	case "array":
		return KindArray, nil
	default:
		return KindJSON, nil
	}
}

// deref follows local "#/$defs/..." and "#/definitions/..." references.
func (s *Spec) deref(ps *jsonschema.Schema) *jsonschema.Schema {
	for i := 0; ps != nil && ps.Ref != "" && i < 10; i++ {
		var next *jsonschema.Schema
		switch {
		case strings.HasPrefix(ps.Ref, "#/$defs/"):
			next = s.raw.Defs[strings.TrimPrefix(ps.Ref, "#/$defs/")]
		case strings.HasPrefix(ps.Ref, "#/definitions/"):
			next = s.raw.Definitions[strings.TrimPrefix(ps.Ref, "#/definitions/")]
		}
		if next == nil {
			return ps // unresolvable or remote reference: use as is
		}
		if ps.Description != "" && next.Description == "" {
			c := *next
			c.Description = ps.Description
			next = &c
		}
		ps = next
	}
	if ps == nil {
		return &jsonschema.Schema{}
	}
	return ps
}

func enumStrings(vals []any) []string {
	var out []string
	for _, v := range vals {
		switch x := v.(type) {
		case string:
			out = append(out, x)
		case nil:
		default:
			b, _ := json.Marshal(x)
			out = append(out, string(b))
		}
	}
	return out
}

func enumType(vals []any) string {
	t := ""
	for _, v := range vals {
		var vt string
		switch x := v.(type) {
		case string:
			vt = "string"
		case float64:
			if x == float64(int64(x)) {
				vt = "integer"
			} else {
				vt = "number"
			}
		case bool:
			vt = "boolean"
		case nil:
			continue
		default:
			return ""
		}
		if t == "" || (t == "integer" && vt == "number") {
			t = vt
		} else if t != vt && !(t == "number" && vt == "integer") {
			return ""
		}
	}
	return t
}

// Param returns the parameter with the given property name.
func (s *Spec) Param(name string) *Param {
	for _, p := range s.Params {
		if p.Name == name {
			return p
		}
	}
	return nil
}

// Missing returns the required parameters absent from args.
func (s *Spec) Missing(args map[string]any) []*Param {
	var out []*Param
	for _, p := range s.Params {
		if _, ok := args[p.Name]; p.Required && !ok {
			out = append(out, p)
		}
	}
	// Required names that are not declared as properties.
	for _, r := range s.raw.Required {
		if _, ok := args[r]; !ok && s.raw.Properties[r] == nil {
			out = append(out, &Param{Name: r, Flag: r, Required: true})
		}
	}
	return out
}

// Validate checks args against the full schema. It returns nil when the schema could not be
// resolved (the server will validate).
func (s *Spec) Validate(args map[string]any) error {
	if s.resolved == nil {
		return nil
	}
	// Validate the JSON form, which is what the server receives.
	b, err := json.Marshal(args)
	if err != nil {
		return err
	}
	var inst any
	if err := json.Unmarshal(b, &inst); err != nil {
		return err
	}
	if err := s.resolved.Validate(inst); err != nil {
		return fmt.Errorf("%s", s.friendly(err.Error()))
	}
	return nil
}

// friendly rewrites jsonschema-go messages such as
// "validating /properties/count: minimum: 0/1 is less than 1.000000" into
// "--count: minimum: 0 is less than 1".
func (s *Spec) friendly(msg string) string {
	msg = strings.TrimPrefix(msg, "validating root: ")
	if rest, ok := strings.CutPrefix(msg, "validating /properties/"); ok {
		if name, detail, ok := strings.Cut(rest, ": "); ok {
			prop, _, _ := strings.Cut(name, "/")
			flag := prop
			if p := s.Param(prop); p != nil {
				flag = p.Flag
			}
			msg = "--" + flag + ": " + detail
		}
	}
	msg = ratRE.ReplaceAllString(msg, "$1")
	msg = trailingZerosRE.ReplaceAllString(msg, "$1")
	return msg
}

var (
	ratRE           = regexp.MustCompile(`\b(-?\d+)/1\b`)
	trailingZerosRE = regexp.MustCompile(`\b(\d+)\.0+\b`)
)

// flagName makes a property name usable as a flag: characters other than letters, digits,
// '-', '_' and '.' become '-'.
func flagName(n string) string {
	var b strings.Builder
	for _, r := range n {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	f := strings.TrimLeft(b.String(), "-")
	if f == "" {
		f = "param"
	}
	return f
}

// Kebab converts camelCase and snake_case to kebab-case: "issueNumber", "issue_number" -> "issue-number".
func Kebab(s string) string {
	var b strings.Builder
	rs := []rune(s)
	for i, r := range rs {
		switch {
		case r == '_' || r == ' ':
			b.WriteByte('-')
		case unicode.IsUpper(r):
			if i > 0 && rs[i-1] != '_' && rs[i-1] != '-' &&
				(unicode.IsLower(rs[i-1]) || unicode.IsDigit(rs[i-1]) || (i+1 < len(rs) && unicode.IsLower(rs[i+1]))) {
				b.WriteByte('-')
			}
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
