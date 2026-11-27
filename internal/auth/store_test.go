package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestStoreRoundTrip(t *testing.T) {
	t.Setenv("MCPSH_STATE_DIR", t.TempDir())
	const u = "https://example.com/mcp"
	if r, err := Load(u); r != nil || err != nil {
		t.Fatalf("empty store: %v %v", r, err)
	}
	if Stamp(u) != "" {
		t.Fatal("stamp without a login")
	}
	rec := &Record{ServerURL: u, ClientID: "c1", TokenURL: "https://example.com/token", LoggedInAt: 100,
		Token: &oauth2.Token{AccessToken: "secret-access", RefreshToken: "r", Expiry: time.Now().Add(time.Hour)}}
	if err := Save(rec); err != nil {
		t.Fatal(err)
	}
	got, err := Load(u)
	if err != nil || got == nil || got.Token.AccessToken != "secret-access" || got.ClientID != "c1" {
		t.Fatalf("load: %+v %v", got, err)
	}
	s1 := Stamp(u)
	rec.Token.AccessToken = "refreshed"
	_ = Save(rec)
	if Stamp(u) != s1 {
		t.Error("a refresh must not change the stamp")
	}
	rec.LoggedInAt = 200
	_ = Save(rec)
	if Stamp(u) == s1 {
		t.Error("a new login must change the stamp")
	}
	// Another URL has its own login.
	if r, _ := Load("https://other.example.com/mcp"); r != nil {
		t.Error("logins leak between servers")
	}
	files, _ := filepath.Glob(filepath.Join(os.Getenv("MCPSH_STATE_DIR"), "auth", "*"))
	if len(files) != 1 {
		t.Fatalf("files: %v", files)
	}
	b, _ := os.ReadFile(files[0])
	if strings.Contains(string(b), "refreshed") != (os.PathSeparator == '/') {
		t.Errorf("clear text token: %v (expected only outside Windows)", strings.Contains(string(b), "refreshed"))
	}
	if had, err := Delete(u); !had || err != nil {
		t.Fatalf("delete: %v %v", had, err)
	}
	if had, _ := Delete(u); had {
		t.Error("deleted twice")
	}
}

func TestPersistingSourceDoesNotResurrect(t *testing.T) {
	t.Setenv("MCPSH_STATE_DIR", t.TempDir())
	const u = "https://example.com/mcp"
	rec := &Record{ServerURL: u, ClientID: "c1", LoggedInAt: 1, Token: &oauth2.Token{AccessToken: "a"}}
	if err := Save(rec); err != nil {
		t.Fatal(err)
	}
	s := &persistingSource{rec: rec, base: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "b"})}
	if _, err := s.Token(); err != nil {
		t.Fatal(err)
	}
	if got, _ := Load(u); got == nil || got.Token.AccessToken != "b" {
		t.Fatalf("refreshed token not stored: %+v", got)
	}
	_, _ = Delete(u)
	s.base = oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "c"})
	if _, err := s.Token(); err != nil {
		t.Fatal(err)
	}
	if got, _ := Load(u); got != nil {
		t.Fatal("a refresh brought a deleted login back")
	}
}
