package e2e

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/davidemaggi/mcpsh/internal/proc"
)

type daemonStatus struct {
	Running bool `json:"running"`
	Status  struct {
		Daemon struct {
			Pid int    `json:"pid"`
			Exe string `json:"exe"`
		} `json:"daemon"`
		Sessions []struct {
			Name      string `json:"name"`
			State     string `json:"state"`
			Pid       int    `json:"pid"`
			Restarts  int    `json:"restarts"`
			LastError string `json:"lastError"`
		} `json:"sessions"`
	} `json:"status"`
}

func (e *env) status() daemonStatus {
	e.t.Helper()
	r := e.run("daemon", "status", "-o", "json")
	var st daemonStatus
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &st) != nil {
		e.t.Fatalf("daemon status: %s", r)
	}
	return st
}

func (e *env) pidOf(server string) int {
	e.t.Helper()
	r := e.run(server, "pid")
	pid, err := strconv.Atoi(strings.TrimSpace(r.stdout))
	if err != nil {
		e.t.Fatalf("%s pid: %s", server, r)
	}
	return pid
}

func eventually(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestDaemonAutostartAndReuse(t *testing.T) {
	e := newEnv(t, modeDaemon)
	e.vars["E2E_TOKEN"] = "do-not-log-me"
	e.userConfig(map[string]any{"ts": ts(map[string]any{"env": map[string]any{"SECRET_TOKEN": "${E2E_TOKEN}"}})})

	if e.status().Running {
		t.Fatal("daemon running before the first call")
	}
	// Help comes from the cache once it exists and never needs the daemon; a call starts it.
	first := e.pidOf("ts")
	st := e.status()
	if !st.Running || len(st.Status.Sessions) != 1 || st.Status.Sessions[0].Pid != first {
		t.Fatalf("status after the first call: %+v", st)
	}
	if second := e.pidOf("ts"); second != first {
		t.Errorf("the session was not reused: pid %d then %d", first, second)
	}
	expect(t, e.run("list"), 0, "running (pid "+strconv.Itoa(first)+")", "")
	expect(t, e.run("ts", "env", "--name", "SECRET_TOKEN"), 0, "do-not-log-me", "")

	logs := e.run("daemon", "logs", "-n", "1000")
	if !strings.Contains(logs.stdout, "daemon started") || strings.Contains(logs.stdout, "do-not-log-me") {
		t.Errorf("daemon log is missing its start or contains the secret:\n%s", logs.stdout)
	}
	daemonPid := st.Status.Daemon.Pid
	e.stopDaemon()
	eventually(t, 10*time.Second, "server and daemon to exit", func() bool {
		return !proc.Alive(first) && !proc.Alive(daemonPid)
	})
	if e.status().Running {
		t.Error("daemon still answers after stop")
	}
}

func TestNoDaemonModeNeverStartsIt(t *testing.T) {
	e := newEnv(t, modeDaemon)
	e.userConfig(map[string]any{"ts": ts(nil)})
	expect(t, e.run("--no-daemon", "ts", "echo", "--text", "x"), 0, "x", "")
	e.vars["MCPSH_NO_DAEMON"] = "1"
	expect(t, e.run("ts", "echo", "--text", "y"), 0, "y", "")
	delete(e.vars, "MCPSH_NO_DAEMON")
	if e.status().Running {
		t.Error("--no-daemon started the daemon")
	}
}

func TestServerIdleTimeout(t *testing.T) {
	e := newEnv(t, modeDaemon)
	e.userConfig(map[string]any{"ts": ts(map[string]any{"idleTimeout": "1s"})})
	first := e.pidOf("ts")
	eventually(t, 15*time.Second, "the idle server to stop", func() bool { return !proc.Alive(first) })
	if st := e.status(); !st.Running || len(st.Status.Sessions) != 0 {
		t.Errorf("after the server idle timeout: %+v", st)
	}
	if second := e.pidOf("ts"); second == first {
		t.Error("expected a new server process")
	}
}

func TestDaemonIdleShutdown(t *testing.T) {
	e := newEnv(t, modeDaemon)
	e.settings["daemonIdleTimeout"] = "2s"
	e.userConfig(map[string]any{"ts": ts(map[string]any{"idleTimeout": "1s"})})
	server := e.pidOf("ts")
	daemonPid := e.status().Status.Daemon.Pid
	eventually(t, 20*time.Second, "the idle daemon to exit", func() bool {
		return !proc.Alive(daemonPid) && !proc.Alive(server)
	})
	if e.status().Running {
		t.Error("daemon still answering")
	}
}

func TestCrashRestart(t *testing.T) {
	e := newEnv(t, modeDaemon)
	e.userConfig(map[string]any{"ts": ts(nil)})
	first := e.pidOf("ts")
	expect(t, e.run("ts", "crash"), 3, "", "server process exited")
	st := e.status()
	if len(st.Status.Sessions) != 1 || st.Status.Sessions[0].State != "crashed" || st.Status.Sessions[0].Restarts != 1 ||
		st.Status.Sessions[0].LastError == "" {
		t.Fatalf("status after crash: %+v", st)
	}
	expect(t, e.run("list"), 0, "crashed", "")
	// The next call restarts the server (after a short backoff).
	second := e.pidOf("ts")
	if second == first || proc.Alive(first) {
		t.Errorf("restart: first %d (alive %v), second %d", first, proc.Alive(first), second)
	}
	if s := e.status().Status.Sessions[0]; s.State != "running" {
		t.Errorf("state after restart: %+v", s)
	}
}

func TestConcurrentCalls(t *testing.T) {
	e := newEnv(t, modeDaemon)
	e.userConfig(map[string]any{"ts": ts(nil)})
	e.pidOf("ts") // warm up
	const n = 12
	var wg sync.WaitGroup
	results := make([]result, n)
	start := time.Now()
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = e.run("ts", "slow", "--ms", "1500")
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	for i, r := range results {
		if r.code != 0 || !strings.Contains(r.stdout, "waited 1500ms") {
			t.Errorf("call %d: %s", i, r)
		}
	}
	// Sequential calls would take n*1.5s; multiplexed ones overlap.
	if elapsed > time.Duration(n)*1500*time.Millisecond/2 {
		t.Errorf("%d concurrent calls took %v: not multiplexed", n, elapsed)
	}
	if st := e.status(); len(st.Status.Sessions) != 1 {
		t.Errorf("expected one shared session, got %+v", st.Status.Sessions)
	}
}

func TestDefinitionChangeReplacesSession(t *testing.T) {
	e := newEnv(t, modeDaemon)
	e.userConfig(map[string]any{"ts": ts(map[string]any{"env": map[string]any{"V": "one"}})})
	first := e.pidOf("ts")
	e.userConfig(map[string]any{"ts": ts(map[string]any{"env": map[string]any{"V": "two"}})})
	expect(t, e.run("ts", "env", "--name", "V"), 0, "two", "")
	eventually(t, 10*time.Second, "the old server to stop", func() bool { return !proc.Alive(first) })
	if st := e.status(); len(st.Status.Sessions) != 1 {
		t.Errorf("sessions after the change: %+v", st.Status.Sessions)
	}
}

func TestNewerCLIRestartsDaemon(t *testing.T) {
	e := newEnv(t, modeDaemon)
	e.userConfig(map[string]any{"ts": ts(nil)})
	e.pidOf("ts")
	old := e.status().Status.Daemon

	// A copy of the binary with a later modification time stands in for an upgrade.
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	newer := filepath.Join(e.root, "upgraded", "mcpsh"+exe)
	copyFile(t, mcpshBin, newer)
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(newer, later, later); err != nil {
		t.Fatal(err)
	}
	saved := mcpshBin
	mcpshBin = newer
	defer func() { mcpshBin = saved }()

	expect(t, e.run("ts", "echo", "--text", "after upgrade"), 0, "after upgrade", "")
	st := e.status().Status.Daemon
	if st.Pid == old.Pid || !sameFile(st.Exe, newer) {
		t.Errorf("daemon not replaced: before %+v, after %+v", old, st)
	}
	eventually(t, 10*time.Second, "the old daemon to exit", func() bool { return !proc.Alive(old.Pid) })

	// The older binary keeps working with the newer daemon (same protocol): no flip-flop.
	mcpshBin = saved
	expect(t, e.run("ts", "echo", "--text", "old cli"), 0, "old cli", "")
	if again := e.status().Status.Daemon; again.Pid != st.Pid {
		t.Errorf("an older CLI replaced a newer daemon: %+v -> %+v", st, again)
	}
	mcpshBin = newer // stop with the binary that owns the daemon
	e.stopDaemon()
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(dst))
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

func sameFile(a, b string) bool {
	ai, err1 := os.Stat(a)
	bi, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(ai, bi)
}

func TestSocketIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the named pipe ACL is set by SDDL; Unix checks file permissions")
	}
	e := newEnv(t, modeDaemon)
	e.userConfig(map[string]any{"ts": ts(nil)})
	e.pidOf("ts")
	// The socket lives in a per-user directory; find it through the running daemon's files.
	dirs := []string{filepath.Join(os.TempDir(), "mcpsh-"+strconv.Itoa(os.Getuid()))}
	if rd := os.Getenv("XDG_RUNTIME_DIR"); rd != "" {
		dirs = append(dirs, filepath.Join(rd, "mcpsh"))
	}
	found := false
	for _, d := range dirs {
		fi, err := os.Stat(d)
		if err != nil {
			continue
		}
		if fi.Mode().Perm() != 0o700 {
			t.Errorf("%s has mode %v, want 0700", d, fi.Mode().Perm())
		}
		entries, _ := os.ReadDir(d)
		for _, en := range entries {
			if !strings.HasSuffix(en.Name(), ".sock") {
				continue
			}
			found = true
			info, err := os.Stat(filepath.Join(d, en.Name()))
			if err == nil && info.Mode().Perm()&0o077 != 0 {
				t.Errorf("socket %s has mode %v", en.Name(), info.Mode().Perm())
			}
		}
	}
	if !found {
		t.Error("no daemon socket found")
	}
}
