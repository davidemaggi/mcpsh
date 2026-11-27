package schema

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/pflag"
)

// Binding connects a Spec to a FlagSet and collects the values given on the command line.
type Binding struct {
	spec   *Spec
	values map[string]*paramValue // by property name
}

// Bind registers one flag per parameter (plus a hidden kebab-case alias) on fs.
func (s *Spec) Bind(fs *pflag.FlagSet) *Binding {
	b := &Binding{spec: s, values: map[string]*paramValue{}}
	for _, p := range s.Params {
		v := &paramValue{p: p}
		b.values[p.Name] = v
		f := fs.VarPF(v, p.Flag, "", p.Description)
		if p.Kind == KindBoolean {
			f.NoOptDefVal = "true"
		}
		if p.Alias != "" {
			a := fs.VarPF(v, p.Alias, "", p.Description)
			a.Hidden = true
			if p.Kind == KindBoolean {
				a.NoOptDefVal = "true"
			}
		}
	}
	return b
}

// Args merges the flags that were set over base (from --json) and returns the arguments.
func (b *Binding) Args(base map[string]any) map[string]any {
	args := map[string]any{}
	for k, v := range base {
		args[k] = v
	}
	for name, v := range b.values {
		if v.set {
			args[name] = v.value()
		}
	}
	return args
}

// paramValue implements pflag.Value for one parameter.
type paramValue struct {
	p     *Param
	set   bool
	one   any
	items []any
}

func (v *paramValue) String() string {
	if !v.set {
		return ""
	}
	b, _ := json.Marshal(v.value())
	return string(b)
}

func (v *paramValue) Type() string { return v.p.TypeHint() }

func (v *paramValue) value() any {
	if v.p.Kind == KindArray {
		if v.items == nil {
			return []any{}
		}
		return v.items
	}
	return v.one
}

func (v *paramValue) Set(s string) error {
	if v.p.Kind == KindArray {
		// A JSON array sets all items at once: --tags '["a","b"]'.
		if strings.HasPrefix(strings.TrimSpace(s), "[") {
			var arr []any
			if err := json.Unmarshal([]byte(s), &arr); err == nil {
				for _, it := range arr {
					if err := v.p.checkEnum(it); err != nil {
						return err
					}
				}
				v.items = append(v.items, arr...)
				v.set = true
				return nil
			}
		}
		x, err := parseScalar(v.p.ItemKind, s)
		if err != nil {
			return err
		}
		if err := v.p.checkEnum(x); err != nil {
			return err
		}
		v.items = append(v.items, x)
		v.set = true
		return nil
	}
	x, err := parseScalar(v.p.Kind, s)
	if err != nil {
		return err
	}
	if err := v.p.checkEnum(x); err != nil {
		return err
	}
	v.one = x
	v.set = true
	return nil
}

func (p *Param) checkEnum(x any) error {
	if len(p.Enum) == 0 {
		return nil
	}
	var s string
	if str, ok := x.(string); ok {
		s = str
	} else {
		b, _ := json.Marshal(x)
		s = string(b)
	}
	if slices.Contains(p.Enum, s) {
		return nil
	}
	return fmt.Errorf("must be one of: %s", strings.Join(p.Enum, ", "))
}

func parseScalar(k Kind, s string) (any, error) {
	switch k {
	case KindString:
		return s, nil
	case KindInteger:
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("expected an integer, got %q", s)
		}
		return n, nil
	case KindNumber:
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return nil, fmt.Errorf("expected a number, got %q", s)
		}
		return f, nil
	case KindBoolean:
		bv, err := strconv.ParseBool(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("expected true or false, got %q", s)
		}
		return bv, nil
	default:
		var x any
		if err := json.Unmarshal([]byte(s), &x); err != nil {
			// A bare word is accepted as a string where any JSON value is allowed,
			// but text that tries to be an object, array or string must be valid JSON.
			t := strings.TrimSpace(s)
			if t != "" && !strings.ContainsRune("{[\"", rune(t[0])) {
				return s, nil
			}
			return nil, fmt.Errorf("expected a JSON value: %v", err)
		}
		return x, nil
	}
}
