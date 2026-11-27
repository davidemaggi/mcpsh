package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
)

// ReservedNames are built-in commands; servers cannot use them as names.
var ReservedNames = []string{
	"add", "agents-md", "call", "completion", "config", "daemon", "help", "import", "list",
	"prompts", "refresh", "remove", "resources", "self-update", "skill", "trust", "version",
}

var serverNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// ValidateName checks that name can be used as a server name.
func ValidateName(name string) error {
	if !serverNameRE.MatchString(name) {
		return fmt.Errorf("invalid server name %q: use letters, digits, '-', '_' and '.', starting with a letter or digit", name)
	}
	if slices.Contains(ReservedNames, name) {
		return fmt.Errorf("%q is a built-in mcpsh command and cannot be a server name", name)
	}
	return nil
}

// rawFile keeps every top-level key so edits preserve what mcpsh does not understand.
type rawFile struct {
	path    string
	top     map[string]json.RawMessage
	servers map[string]json.RawMessage
}

func openRaw(path string) (*rawFile, error) {
	rf := &rawFile{path: path, top: map[string]json.RawMessage{}, servers: map[string]json.RawMessage{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return rf, nil
	}
	if err != nil {
		return nil, &FileError{Path: path, Err: err}
	}
	if len(trimSpace(b)) == 0 {
		return rf, nil
	}
	if err := json.Unmarshal(b, &rf.top); err != nil {
		return nil, &FileError{Path: path, Err: err}
	}
	if s, ok := rf.top["mcpServers"]; ok && string(s) != "null" {
		if err := json.Unmarshal(s, &rf.servers); err != nil {
			return nil, &FileError{Path: path, Err: fmt.Errorf("mcpServers: %w", err)}
		}
	}
	return rf, nil
}

func (rf *rawFile) save() error {
	s, err := json.Marshal(rf.servers)
	if err != nil {
		return err
	}
	rf.top["mcpServers"] = s
	b, err := json.MarshalIndent(rf.top, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(rf.path, append(b, '\n'), 0o600)
}

// AddServer writes s under name into the file at path, creating it if needed.
// It fails when the name exists, unless replace is true.
func AddServer(path, name string, s Server, replace bool) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if err := s.Validate(); err != nil {
		return err
	}
	rf, err := openRaw(path)
	if err != nil {
		return err
	}
	if _, ok := rf.servers[name]; ok && !replace {
		return fmt.Errorf("server %q already exists in %s (use --force to replace it)", name, path)
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	rf.servers[name] = b
	return rf.save()
}

// RemoveServer deletes name from the file at path. It reports whether it was there.
func RemoveServer(path, name string) (bool, error) {
	rf, err := openRaw(path)
	if err != nil {
		return false, err
	}
	if _, ok := rf.servers[name]; !ok {
		return false, nil
	}
	delete(rf.servers, name)
	return true, rf.save()
}

// ReadServers reads the "mcpServers" of any file in the standard format (for import).
func ReadServers(path string) (map[string]Server, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, &FileError{Path: path, Err: err}
	}
	var fd fileData
	if err := json.Unmarshal(b, &fd); err != nil {
		return nil, &FileError{Path: path, Err: err}
	}
	return fd.Servers, nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), perm); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
