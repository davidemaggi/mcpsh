package config

import (
	"fmt"
	"strings"
)

// MissingVarError reports a ${VAR} reference with no value and no default.
type MissingVarError struct {
	Var   string
	Field string
}

func (e *MissingVarError) Error() string {
	return fmt.Sprintf("environment variable %s is not set (referenced in %s); set it or use ${%s:-default}", e.Var, e.Field, e.Var)
}

// Lookup returns the value of an environment variable and whether it is set.
type Lookup func(name string) (string, bool)

// Expand replaces ${VAR} and ${VAR:-default} in s. "$$" is a literal "$".
// A bare "$NAME" (without braces) is left unchanged, so commands that use shell syntax survive.
func Expand(s string, lookup Lookup, field string) (string, error) {
	if !strings.Contains(s, "$") {
		return s, nil
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '$' || i+1 >= len(s) {
			b.WriteByte(c)
			continue
		}
		switch s[i+1] {
		case '$':
			b.WriteByte('$')
			i++
			continue
		case '{':
		default:
			b.WriteByte(c)
			continue
		}
		end := strings.IndexByte(s[i+2:], '}')
		if end < 0 {
			return "", fmt.Errorf("unterminated ${ in %s", field)
		}
		expr := s[i+2 : i+2+end]
		name, def, hasDef := strings.Cut(expr, ":-")
		if !validVarName(name) {
			return "", fmt.Errorf("invalid variable name %q in %s", name, field)
		}
		v, ok := lookup(name)
		switch {
		case ok && v != "":
			b.WriteString(v)
		case hasDef:
			b.WriteString(def)
		case ok:
			// Set but empty, no default: use the empty value.
		default:
			return "", &MissingVarError{Var: name, Field: field}
		}
		i += 2 + end
	}
	return b.String(), nil
}

// HasReference reports whether s contains a ${...} reference.
func HasReference(s string) bool {
	return strings.Contains(strings.ReplaceAll(s, "$$", ""), "${")
}

func validVarName(n string) bool {
	if n == "" {
		return false
	}
	for i, r := range n {
		if r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (i > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}
