package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/davidemaggi/mcpsh/internal/platform"
)

// TrustFileName is the trust store inside platform.StateDir().
const TrustFileName = "trust.json"

// UntrustedError is a project server that has not been approved with "mcpsh trust".
type UntrustedError struct {
	Name string
	File string
}

func (e *UntrustedError) Error() string {
	return fmt.Sprintf("server %q comes from the project file %s and has not been trusted yet; "+
		"review it with \"mcpsh trust\" and approve it with \"mcpsh trust %s\"", e.Name, e.File, e.Name)
}

// TrustStore records approved project servers: project file -> server name -> definition hash.
type TrustStore struct {
	path    string
	Entries map[string]map[string]string `json:"projects"`
}

// LoadTrust reads the trust store (an empty one when the file does not exist).
func LoadTrust() (*TrustStore, error) {
	dir, err := platform.StateDir()
	if err != nil {
		return nil, err
	}
	ts := &TrustStore{path: filepath.Join(dir, TrustFileName), Entries: map[string]map[string]string{}}
	b, err := os.ReadFile(ts.path)
	if errors.Is(err, fs.ErrNotExist) {
		return ts, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, ts); err != nil {
		return nil, &FileError{Path: ts.path, Err: err}
	}
	if ts.Entries == nil {
		ts.Entries = map[string]map[string]string{}
	}
	return ts, nil
}

func trustKey(file string) string {
	if abs, err := filepath.Abs(file); err == nil {
		file = abs
	}
	return filepath.Clean(file)
}

// Trusted reports whether e may run: user servers always may, project servers only when their
// current definition was approved.
func (ts *TrustStore) Trusted(e *Entry) bool {
	if e.Scope != ScopeProject {
		return true
	}
	return ts.Entries[trustKey(e.File)][e.Name] == DefinitionHash(&e.Server)
}

// Check returns an UntrustedError when e may not run.
func (ts *TrustStore) Check(e *Entry) error {
	if ts.Trusted(e) {
		return nil
	}
	return &UntrustedError{Name: e.Name, File: e.File}
}

// Trust approves the current definition of e.
func (ts *TrustStore) Trust(e *Entry) {
	k := trustKey(e.File)
	if ts.Entries[k] == nil {
		ts.Entries[k] = map[string]string{}
	}
	ts.Entries[k][e.Name] = DefinitionHash(&e.Server)
}

// Revoke removes the approval of a project server.
func (ts *TrustStore) Revoke(file, name string) {
	k := trustKey(file)
	delete(ts.Entries[k], name)
	if len(ts.Entries[k]) == 0 {
		delete(ts.Entries, k)
	}
}

// Save writes the store, readable only by the current user.
func (ts *TrustStore) Save() error {
	b, err := json.MarshalIndent(ts, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(ts.path, append(b, '\n'), 0o600)
}
