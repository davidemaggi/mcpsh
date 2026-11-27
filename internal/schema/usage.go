package schema

import (
	"fmt"
	"strings"
)

// FlagUsages renders the parameters as aligned help lines, required ones first.
func (s *Spec) FlagUsages() string {
	type line struct{ left, right string }
	var lines []line
	width := 0
	for _, p := range s.Params {
		left := "  --" + p.Flag
		if h := p.TypeHint(); h != "" {
			left += " " + h
		}
		var notes []string
		if p.Required {
			notes = append(notes, "required")
		}
		if p.Kind == KindArray {
			notes = append(notes, "repeatable")
		}
		if p.Default != "" {
			notes = append(notes, "default "+p.Default)
		}
		if p.Alias != "" {
			notes = append(notes, "alias --"+p.Alias)
		}
		if p.Flag != p.Name {
			notes = append(notes, fmt.Sprintf("parameter %q", p.Name))
		}
		right := p.Description
		if len(notes) > 0 {
			if right != "" {
				right += " "
			}
			right += "(" + strings.Join(notes, ", ") + ")"
		}
		lines = append(lines, line{left, right})
		width = max(width, len(left))
	}
	var b strings.Builder
	for _, l := range lines {
		fmt.Fprintf(&b, "%-*s   %s\n", width, l.left, l.right)
	}
	return b.String()
}
