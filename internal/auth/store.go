package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/oauth2"

	"github.com/davidemaggi/mcpsh/internal/platform"
)

// Record is what a login leaves behind for one server URL: the client the authorization server
// knows us as and the token it issued. It lives in the user's state directory (encrypted with
// DPAPI on Windows, mode 0600 elsewhere), never in config files or logs.
type Record struct {
	ServerURL    string        `json:"serverUrl"`
	ClientID     string        `json:"clientId"`
	ClientSecret string        `json:"clientSecret,omitempty"`
	AuthURL      string        `json:"authUrl"`
	TokenURL     string        `json:"tokenUrl"`
	AuthStyle    int           `json:"authStyle,omitempty"`
	Scopes       []string      `json:"scopes,omitempty"`
	Token        *oauth2.Token `json:"token"`
	// LoggedInAt (unix seconds) identifies the login; refreshing the token keeps it.
	LoggedInAt int64 `json:"loggedInAt"`
}

// Config rebuilds the oauth2 configuration that refreshes the record's token.
func (r *Record) Config() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     r.ClientID,
		ClientSecret: r.ClientSecret,
		Endpoint:     oauth2.Endpoint{AuthURL: r.AuthURL, TokenURL: r.TokenURL, AuthStyle: oauth2.AuthStyle(r.AuthStyle)},
		Scopes:       r.Scopes,
	}
}

func recordPath(serverURL string) (string, error) {
	dir, err := platform.StateDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(serverURL))
	return filepath.Join(dir, "auth", hex.EncodeToString(sum[:12])+".json"), nil
}

// Load returns the stored login for serverURL, or nil when there is none.
func Load(serverURL string) (*Record, error) {
	p, err := recordPath(serverURL)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if b, err = unprotect(b); err != nil {
		return nil, fmt.Errorf("cannot decrypt the stored login (%s): %v; run \"mcpsh auth login\" again", p, err)
	}
	var r Record
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("the stored login %s is damaged: %v; run \"mcpsh auth login\" again", p, err)
	}
	if r.ServerURL != serverURL || r.Token == nil {
		return nil, nil
	}
	return &r, nil
}

// Save stores the login atomically.
func Save(r *Record) error {
	p, err := recordPath(r.ServerURL)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if b, err = protect(b); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".login-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// Delete removes the stored login; it reports whether there was one.
func Delete(serverURL string) (bool, error) {
	p, err := recordPath(serverURL)
	if err != nil {
		return false, err
	}
	err = os.Remove(p)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// Stamp identifies the current stored login (empty when there is none): it changes on every
// login and logout, but not when a token is refreshed in place.
func Stamp(serverURL string) string {
	r, err := Load(serverURL)
	if err != nil || r == nil {
		return ""
	}
	return r.ClientID + "@" + strconv.FormatInt(r.LoggedInAt, 10)
}
