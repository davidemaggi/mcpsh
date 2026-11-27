package testserver

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// OAuth is a minimal authorization server in front of the MCP endpoint, for the login tests:
// protected resource and authorization server metadata, dynamic client registration, an
// authorize endpoint that approves at once (no user), and a token endpoint with PKCE checks
// and refresh tokens. The MCP endpoint answers 401 without a valid access token.
type OAuth struct {
	Base     string        // e.g. http://127.0.0.1:1234
	Resource string        // the MCP endpoint URL
	TTL      time.Duration // lifetime of access tokens

	mu      sync.Mutex
	n       int
	clients map[string]bool
	codes   map[string]string // code -> PKCE challenge
	access  map[string]time.Time
	refresh map[string]bool
}

// NewOAuth returns the server for the MCP endpoint at base+path.
func NewOAuth(base, path string, ttl time.Duration) *OAuth {
	return &OAuth{Base: base, Resource: base + path, TTL: ttl,
		clients: map[string]bool{}, codes: map[string]string{}, access: map[string]time.Time{}, refresh: map[string]bool{}}
}

func (o *OAuth) next(prefix string) string {
	o.n++
	return fmt.Sprintf("%s-%d", prefix, o.n)
}

// Register adds the OAuth endpoints to mux and protects mcp.
func (o *OAuth) Register(mux *http.ServeMux, path string, mcp http.Handler) {
	prm := map[string]any{"resource": o.Resource, "authorization_servers": []string{o.Base}, "scopes_supported": []string{"mcp"}}
	mux.HandleFunc("/.well-known/oauth-protected-resource", jsonHandler(prm))
	mux.HandleFunc("/.well-known/oauth-protected-resource"+path, jsonHandler(prm))
	mux.HandleFunc("/.well-known/oauth-authorization-server", jsonHandler(map[string]any{
		"issuer":                                o.Base,
		"authorization_endpoint":                o.Base + "/authorize",
		"token_endpoint":                        o.Base + "/token",
		"registration_endpoint":                 o.Base + "/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{"mcp", "offline_access"},
	}))
	mux.HandleFunc("/register", o.register)
	mux.HandleFunc("/authorize", o.authorize)
	mux.HandleFunc("/token", o.token)
	mux.Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		o.mu.Lock()
		exp, ok := o.access[tok]
		o.mu.Unlock()
		if !ok || time.Now().After(exp) {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource%s"`, o.Base, path))
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mcp.ServeHTTP(w, r)
	}))
}

func jsonHandler(v any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, v) }
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (o *OAuth) register(w http.ResponseWriter, r *http.Request) {
	var md map[string]any
	if err := json.NewDecoder(r.Body).Decode(&md); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_client_metadata"})
		return
	}
	o.mu.Lock()
	id := o.next("client")
	o.clients[id] = true
	o.mu.Unlock()
	md["client_id"] = id
	md["client_id_issued_at"] = time.Now().Unix()
	writeJSON(w, http.StatusCreated, md)
}

func (o *OAuth) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirect, err := url.Parse(q.Get("redirect_uri"))
	o.mu.Lock()
	known := o.clients[q.Get("client_id")]
	o.mu.Unlock()
	if err != nil || !known || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		http.Error(w, "bad authorization request", http.StatusBadRequest)
		return
	}
	o.mu.Lock()
	code := o.next("code")
	o.codes[code] = q.Get("code_challenge")
	o.mu.Unlock()
	back := redirect.Query()
	back.Set("code", code)
	back.Set("state", q.Get("state"))
	redirect.RawQuery = back.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

func (o *OAuth) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		challenge, ok := o.codes[r.PostForm.Get("code")]
		delete(o.codes, r.PostForm.Get("code"))
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
			return
		}
	case "refresh_token":
		if !o.refresh[r.PostForm.Get("refresh_token")] {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
			return
		}
		delete(o.refresh, r.PostForm.Get("refresh_token"))
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_grant_type"})
		return
	}
	at, rt := o.next("access"), o.next("refresh")
	o.access[at] = time.Now().Add(o.TTL)
	o.refresh[rt] = true
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": at, "token_type": "Bearer", "expires_in": int(o.TTL.Seconds()), "refresh_token": rt,
	})
}

// FollowRedirects is the test "browser": it opens url and follows the redirects back to the
// client's loopback listener.
func FollowRedirects(u string) error {
	resp, err := http.Get(u)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", u, resp.Status)
	}
	return nil
}
