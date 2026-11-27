// Package update replaces the running mcpsh with a release from GitHub ("mcpsh self-update").
//
// It downloads the same archives as install.sh / install.ps1 (the Authenticode-signed zip on
// Windows when the release has one), verifies them against the release checksums, and swaps the
// executable in place: rename-over on Unix; on Windows the running file is renamed to .old
// (allowed while it runs) and removed on a later start. Installs owned by a package manager are
// left to it.
package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

// Defaults; MCPSH_UPDATE_API and MCPSH_UPDATE_BASE_URL override them (tests, mirrors).
const (
	defaultAPI  = "https://api.github.com/repos/davidemaggi/mcpsh/releases/latest"
	defaultBase = "https://github.com/davidemaggi/mcpsh/releases"
	maxDownload = 200 << 20
)

// ManagedError: another tool installed mcpsh and must update it.
type ManagedError struct{ Method, Command string }

func (e *ManagedError) Error() string {
	return fmt.Sprintf("this mcpsh comes from %s: update it with %q (or --force)", e.Method, e.Command)
}

// Method tells how the executable at exe was installed, and the command that updates it.
// An empty method means a standalone install that self-update may replace.
func Method(exe, currentVersion string, getenv func(string) string) (method, command string) {
	p := strings.ToLower(strings.ReplaceAll(exe, `\`, "/"))
	switch {
	case getenv("MCPSH_INSTALL_METHOD") == "npm" || strings.Contains(p, "/node_modules/"):
		return "npm", "npm install -g @davidemaggi/mcpsh@latest"
	case strings.Contains(p, "/caskroom/") || strings.Contains(p, "/cellar/") || strings.Contains(p, "/homebrew/"):
		return "Homebrew", "brew upgrade --cask mcpsh"
	case strings.Contains(p, "/scoop/apps/") || strings.Contains(p, "/scoop/shims/"):
		return "Scoop", "scoop update mcpsh"
	case strings.Contains(p, "/microsoft/winget/"):
		return "winget", "winget upgrade DavideMaggi.mcpsh"
	}
	if currentVersion == "dev" {
		if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
			return "go install", "go install github.com/davidemaggi/mcpsh/cmd/mcpsh@latest"
		}
		return "a development build", "git pull && go build ./cmd/mcpsh"
	}
	return "", ""
}

// Updater talks to the release server.
type Updater struct {
	API     string // latest-release endpoint (GitHub API)
	Base    string // download base: <Base>/download/v<version>/<asset>
	HTTP    *http.Client
	GOOS    string
	GOARCH  string
	Timeout time.Duration
}

// New returns an updater for this platform.
func New(getenv func(string) string) *Updater {
	u := &Updater{API: defaultAPI, Base: defaultBase, HTTP: http.DefaultClient, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	if v := getenv("MCPSH_UPDATE_API"); v != "" {
		u.API = v
	}
	if v := getenv("MCPSH_UPDATE_BASE_URL"); v != "" {
		u.Base = strings.TrimRight(v, "/")
	}
	return u
}

// Latest returns the version of the latest release, without a leading "v".
func (u *Updater) Latest(ctx context.Context) (string, error) {
	b, err := u.get(ctx, u.API, 1<<20)
	if err != nil {
		return "", fmt.Errorf("cannot find the latest release: %w", err)
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(b, &rel); err != nil || rel.TagName == "" {
		return "", fmt.Errorf("cannot read the latest release from %s", u.API)
	}
	return strings.TrimPrefix(rel.TagName, "v"), nil
}

// Download fetches and verifies the release archive for version and returns the mcpsh
// executable it contains, and the name of the archive it came from.
func (u *Updater) Download(ctx context.Context, version string) ([]byte, string, error) {
	version = strings.TrimPrefix(version, "v")
	type candidate struct{ asset, sums string }
	var cands []candidate
	if u.GOOS == "windows" {
		cands = append(cands, candidate{fmt.Sprintf("mcpsh_%s_windows_%s-signed.zip", version, u.GOARCH), "checksums-signed.txt"})
		cands = append(cands, candidate{fmt.Sprintf("mcpsh_%s_windows_%s.zip", version, u.GOARCH), "checksums.txt"})
	} else {
		cands = append(cands, candidate{fmt.Sprintf("mcpsh_%s_%s_%s.tar.gz", version, u.GOOS, u.GOARCH), "checksums.txt"})
	}
	dir := u.Base + "/download/v" + version + "/"
	var lastErr error
	for _, c := range cands {
		sums, err := u.get(ctx, dir+c.sums, 1<<20)
		if err != nil {
			lastErr = err
			continue
		}
		want := checksumFor(sums, c.asset)
		if want == "" {
			lastErr = fmt.Errorf("%s is not listed in %s", c.asset, c.sums)
			continue
		}
		data, err := u.get(ctx, dir+c.asset, maxDownload)
		if err != nil {
			return nil, "", err
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != want {
			return nil, "", fmt.Errorf("checksum mismatch for %s (expected %s, got %s): not installing it", c.asset, want, got)
		}
		exe, err := extract(data, c.asset, u.GOOS)
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", c.asset, err)
		}
		return exe, c.asset, nil
	}
	return nil, "", fmt.Errorf("no %s/%s build of mcpsh %s: %w", u.GOOS, u.GOARCH, version, lastErr)
}

func (u *Updater) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "mcpsh-self-update")
	resp, err := u.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s: larger than %d bytes", url, limit)
	}
	return b, nil
}

func checksumFor(sums []byte, name string) string {
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return strings.ToLower(f[0])
		}
	}
	return ""
}

func extract(data []byte, asset, goos string) ([]byte, error) {
	want := "mcpsh"
	if goos == "windows" {
		want = "mcpsh.exe"
	}
	if strings.HasSuffix(asset, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if path.Base(f.Name) == want && !f.FileInfo().IsDir() {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(io.LimitReader(rc, maxDownload))
			}
		}
		return nil, errors.New("the archive does not contain " + want)
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, errors.New("the archive does not contain " + want)
		}
		if err != nil {
			return nil, err
		}
		if path.Base(h.Name) == want && h.Typeflag == tar.TypeReg {
			return io.ReadAll(io.LimitReader(tr, maxDownload))
		}
	}
}

// Replace swaps the executable at exe for data.
func Replace(exe string, data []byte) error {
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".mcpsh-update-*")
	if err != nil {
		return fmt.Errorf("cannot write next to %s: %w", exe, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		// A running executable cannot be overwritten or deleted, but it can be renamed.
		old := exe + ".old"
		_ = os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			return fmt.Errorf("cannot move the running mcpsh aside: %w", err)
		}
		if err := os.Rename(tmp.Name(), exe); err != nil {
			_ = os.Rename(old, exe)
			return err
		}
		_ = os.Remove(old) // works when nothing runs it any more
		return nil
	}
	return os.Rename(tmp.Name(), exe)
}

// CleanupOld removes the executable left behind by an update on Windows, once nothing runs it.
func CleanupOld(exe string) {
	if runtime.GOOS == "windows" {
		_ = os.Remove(exe + ".old")
	}
}

// Newer reports whether version a is newer than b (X.Y.Z, a prerelease sorts before its release).
func Newer(a, b string) bool {
	pa, okA := parse(a)
	pb, okB := parse(b)
	if !okA || !okB {
		return okA && !okB // anything beats an unparsable (dev) version
	}
	for i := 0; i < 3; i++ {
		if pa.n[i] != pb.n[i] {
			return pa.n[i] > pb.n[i]
		}
	}
	switch {
	case pa.pre == pb.pre:
		return false
	case pa.pre == "":
		return true
	case pb.pre == "":
		return false
	}
	return pa.pre > pb.pre
}

type semver struct {
	n   [3]int
	pre string
}

func parse(v string) (semver, bool) {
	var s semver
	v = strings.TrimPrefix(v, "v")
	v, s.pre, _ = strings.Cut(v, "-")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return s, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return s, false
		}
		s.n[i] = n
	}
	return s, true
}
