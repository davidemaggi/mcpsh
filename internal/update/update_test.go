package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func tarGz(t *testing.T, name string, body []byte) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		name string
		body []byte
	}{{"LICENSE", []byte("MIT")}, {name, body}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write(f.body)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func zipOf(t *testing.T, name string, body []byte) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(body)
	zw.Close()
	return buf.Bytes()
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// fakeRelease serves a release 1.2.0 with a Linux archive, an unsigned and a signed Windows zip.
func fakeRelease(t *testing.T, tamper bool) *httptest.Server {
	files := map[string][]byte{
		"mcpsh_1.2.0_linux_amd64.tar.gz":       tarGz(t, "mcpsh", []byte("linux binary")),
		"mcpsh_1.2.0_windows_amd64.zip":        zipOf(t, "mcpsh.exe", []byte("unsigned exe")),
		"mcpsh_1.2.0_windows_amd64-signed.zip": zipOf(t, "mcpsh.exe", []byte("signed exe")),
	}
	var sums, signed strings.Builder
	for name, b := range files {
		s := sha(b)
		if tamper {
			s = strings.Repeat("0", 64)
		}
		if strings.Contains(name, "signed") {
			fmt.Fprintf(&signed, "%s  %s\n", s, name)
		} else {
			fmt.Fprintf(&sums, "%s  %s\n", s, name)
		}
	}
	files["checksums.txt"] = []byte(sums.String())
	files["checksums-signed.txt"] = []byte(signed.String())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/latest" {
			fmt.Fprint(w, `{"tag_name": "v1.2.0"}`)
			return
		}
		b, ok := files[strings.TrimPrefix(r.URL.Path, "/download/v1.2.0/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func updater(srv *httptest.Server, goos string) *Updater {
	u := New(func(k string) string {
		return map[string]string{"MCPSH_UPDATE_API": srv.URL + "/api/latest", "MCPSH_UPDATE_BASE_URL": srv.URL}[k]
	})
	u.GOOS, u.GOARCH = goos, "amd64"
	return u
}

func TestLatestAndDownload(t *testing.T) {
	srv := fakeRelease(t, false)
	ctx := context.Background()
	v, err := updater(srv, "linux").Latest(ctx)
	if err != nil || v != "1.2.0" {
		t.Fatalf("latest: %q %v", v, err)
	}
	exe, asset, err := updater(srv, "linux").Download(ctx, v)
	if err != nil || string(exe) != "linux binary" || asset != "mcpsh_1.2.0_linux_amd64.tar.gz" {
		t.Fatalf("linux: %q %q %v", exe, asset, err)
	}
	exe, asset, err = updater(srv, "windows").Download(ctx, "v1.2.0")
	if err != nil || string(exe) != "signed exe" || !strings.HasSuffix(asset, "-signed.zip") {
		t.Fatalf("windows should prefer the signed build: %q %q %v", exe, asset, err)
	}
	if _, _, err := updater(srv, "darwin").Download(ctx, v); err == nil {
		t.Error("a missing platform must fail")
	}
}

func TestDownloadRejectsBadChecksum(t *testing.T) {
	srv := fakeRelease(t, true)
	_, _, err := updater(srv, "linux").Download(context.Background(), "1.2.0")
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("want a checksum error, got %v", err)
	}
}

func TestReplace(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "mcpsh")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Replace(exe, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new" {
		t.Fatalf("content %q", b)
	}
	if fi, _ := os.Stat(exe); runtime.GOOS != "windows" && fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("not executable: %v", fi.Mode())
	}
	left, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(left) != 1 {
		t.Errorf("leftovers: %v", left)
	}
}

func TestMethod(t *testing.T) {
	none := func(string) string { return "" }
	cases := map[string]string{
		"/opt/homebrew/Caskroom/mcpsh/1.0.0/mcpsh":                                  "Homebrew",
		`C:\Users\me\scoop\apps\mcpsh\current\mcpsh.exe`:                            "Scoop",
		`C:\Users\me\AppData\Local\Microsoft\WinGet\Packages\DavideMaggi\mcpsh.exe`: "winget",
		"/usr/lib/node_modules/@davidemaggi/mcpsh-linux-x64/bin/mcpsh":              "npm",
		"/home/me/.local/bin/mcpsh":                                                 "",
		`C:\Users\me\AppData\Local\Programs\mcpsh\mcpsh.exe`:                        "",
	}
	for exe, want := range cases {
		if got, _ := Method(exe, "1.0.0", none); got != want {
			t.Errorf("%s: %q, want %q", exe, got, want)
		}
	}
	npm := func(k string) string { return map[string]string{"MCPSH_INSTALL_METHOD": "npm"}[k] }
	if got, _ := Method("/x/mcpsh", "1.0.0", npm); got != "npm" {
		t.Errorf("env npm: %q", got)
	}
	if got, _ := Method("/x/mcpsh", "dev", none); got == "" {
		t.Error("a dev build must not replace itself")
	}
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"1.2.0", "1.1.9", true}, {"1.2.0", "1.2.0", false}, {"1.1.0", "1.2.0", false},
		{"v2.0.0", "1.9.9", true}, {"1.2.0", "1.2.0-rc.1", true}, {"1.2.0-rc.1", "1.2.0", false},
		{"1.0.0", "dev", true}, {"dev", "1.0.0", false},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%s, %s) = %v", c.a, c.b, got)
		}
	}
}
