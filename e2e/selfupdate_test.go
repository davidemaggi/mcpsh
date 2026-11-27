package e2e

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// buildVersion builds mcpsh with a release version stamped in.
func buildVersion(t *testing.T, out, ver string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", out,
		"-ldflags", "-X github.com/davidemaggi/mcpsh/internal/version.Version="+ver, "../cmd/mcpsh")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", ver, err, b)
	}
}

func archiveOf(t *testing.T, exePath string) (name string, data []byte) {
	t.Helper()
	body, err := os.ReadFile(exePath)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if runtime.GOOS == "windows" {
		zw := zip.NewWriter(&buf)
		w, _ := zw.Create("mcpsh.exe")
		w.Write(body)
		zw.Close()
		return fmt.Sprintf("mcpsh_1.1.0_windows_%s.zip", runtime.GOARCH), buf.Bytes()
	}
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "mcpsh", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	tw.Write(body)
	tw.Close()
	gz.Close()
	return fmt.Sprintf("mcpsh_1.1.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH), buf.Bytes()
}

func TestSelfUpdate(t *testing.T) {
	e := newEnv(t, "daemon")
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	installed := filepath.Join(e.root, "bin", "mcpsh"+exe)
	buildVersion(t, installed, "1.0.0")
	newer := filepath.Join(e.root, "build", "mcpsh"+exe)
	buildVersion(t, newer, "1.1.0")
	name, data := archiveOf(t, newer)
	sum := sha256.Sum256(data)
	sums := hex.EncodeToString(sum[:]) + "  " + name + "\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/download/v1.1.0/") {
		case "/api/latest":
			fmt.Fprint(w, `{"tag_name": "v1.1.0"}`)
		case "checksums.txt":
			fmt.Fprint(w, sums)
		case name:
			w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	e.vars["MCPSH_UPDATE_API"] = srv.URL + "/api/latest"
	e.vars["MCPSH_UPDATE_BASE_URL"] = srv.URL
	e.userConfig(map[string]any{"ts": ts(nil)})

	run := func(args ...string) result {
		t.Helper()
		cmd := exec.Command(installed, args...)
		cmd.Dir = e.project
		cmd.Env = os.Environ()
		for k, v := range e.vars {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		return result{code: code, stdout: out.String(), stderr: errb.String()}
	}

	// A daemon from the old binary is running (and holds the file open on Windows).
	expect(t, run("ts", "echo", "--text", "before"), 0, "before", "")
	expect(t, run("self-update", "--check"), 0, "mcpsh 1.1.0 is available", "")
	expect(t, run("self-update"), 0, "Updated mcpsh 1.0.0 -> 1.1.0", "")
	expect(t, run("version"), 0, "mcpsh 1.1.0", "")
	expect(t, run("ts", "echo", "--text", "after"), 0, "after", "")
	expect(t, run("self-update"), 0, "is up to date", "")
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(installed), "*"))
	if len(left) != 1 {
		t.Errorf("leftovers next to the executable: %v", left)
	}
	run("daemon", "stop")
}
