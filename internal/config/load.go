package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/davidemaggi/mcpsh/internal/platform"
)

// ProjectFileName is the project-level config file, searched from the cwd upwards.
const ProjectFileName = ".mcpsh.json"

// UserFileName is the user-level config file inside platform.ConfigDir().
const UserFileName = "config.json"

// Scope says which file a server comes from.
type Scope string

const (
	ScopeUser    Scope = "user"
	ScopeProject Scope = "project"
)

// fileData is the part of a config file mcpsh understands.
type fileData struct {
	Servers  map[string]Server `json:"mcpServers"`
	Settings Settings          `json:"settings"`
}

// Entry is a server after merging, with where it came from.
type Entry struct {
	Name   string
	Scope  Scope
	File   string
	Server Server
}

// Config is the merged view of the user and project files.
type Config struct {
	UserFile      string // path of the user file (it may not exist)
	ProjectFile   string // path of the project file, "" when none was found
	WorkspaceRoot string // project file dir, else git root, else cwd
	Settings      Settings
	Servers       map[string]*Entry
	Warnings      []string
}

// FileError is a config file that exists but cannot be read or parsed.
type FileError struct {
	Path string
	Err  error
}

func (e *FileError) Error() string { return fmt.Sprintf("config file %s: %v", e.Path, e.Err) }
func (e *FileError) Unwrap() error { return e.Err }

// Load reads the user file and the nearest project file for cwd and merges them.
// A project server replaces the user server with the same name entirely.
func Load(cwd string) (*Config, error) {
	dir, err := platform.ConfigDir()
	if err != nil {
		return nil, fmt.Errorf("locating the user config directory: %w", err)
	}
	cfg := &Config{
		UserFile: filepath.Join(dir, UserFileName),
		Servers:  map[string]*Entry{},
	}
	cfg.ProjectFile = FindProjectFile(cwd)
	cfg.WorkspaceRoot = workspaceRoot(cwd, cfg.ProjectFile)

	user, err := readFile(cfg.UserFile)
	if err != nil {
		return nil, err
	}
	cfg.Settings = user.Settings
	for name, s := range user.Servers {
		cfg.Servers[name] = &Entry{Name: name, Scope: ScopeUser, File: cfg.UserFile, Server: s}
	}
	if cfg.ProjectFile != "" {
		proj, err := readFile(cfg.ProjectFile)
		if err != nil {
			return nil, err
		}
		cfg.Settings = cfg.Settings.merge(proj.Settings)
		for _, name := range sortedKeys(proj.Servers) {
			s := proj.Servers[name]
			cfg.Servers[name] = &Entry{Name: name, Scope: ScopeProject, File: cfg.ProjectFile, Server: s}
			if lit := LiteralSecrets(&s); len(lit) > 0 {
				cfg.Warnings = append(cfg.Warnings, fmt.Sprintf(
					"%s: server %q has secret-looking values in clear text (%v); use ${VAR} references instead",
					cfg.ProjectFile, name, lit))
			}
		}
	}
	return cfg, nil
}

// Names returns the server names in alphabetical order.
func (c *Config) Names() []string { return sortedKeys(c.Servers) }

// FindProjectFile returns the nearest .mcpsh.json from dir upwards, or "".
// The search stops after a directory containing .git, and never looks at the home directory
// itself or above it.
func FindProjectFile(dir string) string {
	home, _ := os.UserHomeDir()
	for d := filepath.Clean(dir); ; {
		if home != "" && samePath(d, home) {
			return ""
		}
		p := filepath.Join(d, ProjectFileName)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
		if exists(filepath.Join(d, ".git")) {
			return ""
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

func workspaceRoot(cwd, projectFile string) string {
	if projectFile != "" {
		return filepath.Dir(projectFile)
	}
	home, _ := os.UserHomeDir()
	for d := filepath.Clean(cwd); ; {
		if home != "" && samePath(d, home) {
			break
		}
		if exists(filepath.Join(d, ".git")) {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return filepath.Clean(cwd)
}

func readFile(path string) (*fileData, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &fileData{}, nil
	}
	if err != nil {
		return nil, &FileError{Path: path, Err: err}
	}
	var fd fileData
	if len(trimSpace(b)) == 0 {
		return &fd, nil
	}
	if err := json.Unmarshal(b, &fd); err != nil {
		return nil, &FileError{Path: path, Err: err}
	}
	return &fd, nil
}

func trimSpace(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && (b[i] == ' ' || b[i] == '\n' || b[i] == '\r' || b[i] == '\t') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\n' || b[j-1] == '\r' || b[j-1] == '\t') {
		j--
	}
	return b[i:j]
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func samePath(a, b string) bool {
	ai, err1 := os.Stat(a)
	bi, err2 := os.Stat(b)
	if err1 != nil || err2 != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return os.SameFile(ai, bi)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
