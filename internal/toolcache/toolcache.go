// Package toolcache stores each server's tools/list result on disk, so help and discovery do
// not need to start the server. Entries are keyed by the resolved server ID and hold the
// unfiltered list; allow/deny filters apply when reading.
package toolcache

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/davidemaggi/mcpsh/internal/platform"
)

// Entry is one cached tools/list.
type Entry struct {
	Server    string      `json:"server"`
	FetchedAt time.Time   `json:"fetchedAt"`
	Tools     []*mcp.Tool `json:"tools"`
}

// Fresh reports whether the entry is younger than ttl.
func (e *Entry) Fresh(ttl time.Duration) bool {
	return time.Since(e.FetchedAt) < ttl
}

func path(id string) (string, error) {
	dir, err := platform.CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tools", id+".json"), nil
}

// Get returns the cached entry for a server ID, or nil when there is none (or it is unreadable).
func Get(id string) *Entry {
	p, err := path(id)
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var e Entry
	if json.Unmarshal(b, &e) != nil {
		return nil
	}
	return &e
}

// Put stores the tools of a server ID.
func Put(id, server string, tools []*mcp.Tool) error {
	p, err := path(id)
	if err != nil {
		return err
	}
	b, err := json.Marshal(&Entry{Server: server, FetchedAt: time.Now().UTC(), Tools: tools})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tools-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// Delete removes the cached entry of a server ID.
func Delete(id string) error {
	p, err := path(id)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
