package config

import (
	"sort"
	"strings"
)

var secretWords = []string{"token", "secret", "key", "password", "passwd", "pwd", "auth", "credential", "cookie", "session", "bearer"}

// LooksSecret reports whether an env or header name suggests that its value is a secret.
func LooksSecret(name string) bool {
	n := strings.ToLower(name)
	for _, w := range secretWords {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

// LiteralSecrets lists env and header entries of s whose name looks secret and whose value
// is written in clear text instead of a ${VAR} reference. Results are "env.NAME" / "headers.NAME".
func LiteralSecrets(s *Server) []string {
	var out []string
	for k, v := range s.Env {
		if LooksSecret(k) && v != "" && !HasReference(v) {
			out = append(out, "env."+k)
		}
	}
	for k, v := range s.Headers {
		if LooksSecret(k) && v != "" && !HasReference(v) {
			out = append(out, "headers."+k)
		}
	}
	if s.OAuth != nil && s.OAuth.ClientSecret != "" && !HasReference(s.OAuth.ClientSecret) {
		out = append(out, "oauth.clientSecret")
	}
	sort.Strings(out)
	return out
}
