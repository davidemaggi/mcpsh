package proc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const helperEnv = "MCPSH_PROC_HELPER"

// TestMain lets the test binary act as a child process.
func TestMain(m *testing.M) {
	switch os.Getenv(helperEnv) {
	case "parent":
		// Start a grandchild, report both PIDs, then wait until killed.
		gc := exec.Command(os.Args[0])
		gc.Env = append(os.Environ(), helperEnv+"=sleep")
		if err := gc.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("%d %d\n", os.Getpid(), gc.Process.Pid)
		fmt.Fprintln(os.Stderr, "parent started")
		time.Sleep(time.Hour)
	case "sleep":
		time.Sleep(time.Hour)
	case "stdin-exit":
		// Exit as soon as stdin closes, like a well-behaved MCP stdio server.
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	case "echoargs":
		_ = json.NewEncoder(os.Stdout).Encode(os.Args[1:])
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func helper(mode string) Spec {
	return Spec{Command: os.Args[0], Env: append(os.Environ(), helperEnv+"="+mode)}
}

func TestCloseKillsWholeTree(t *testing.T) {
	p, err := Start(helper("parent"))
	if err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(p.Stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("reading PIDs: %v (stderr: %s)", err, p.StderrTail())
	}
	var parent, child int
	if _, err := fmt.Sscan(line, &parent, &child); err != nil {
		t.Fatalf("bad PID line %q", line)
	}
	if parent != p.Pid() || !Alive(child) {
		t.Fatalf("parent %d (want %d), child alive %v", parent, p.Pid(), Alive(child))
	}
	start := time.Now()
	if err := p.Close(200 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("close took %v", time.Since(start))
	}
	deadline := time.Now().Add(5 * time.Second)
	for Alive(child) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if Alive(child) {
		t.Errorf("grandchild %d survived", child)
	}
	if Alive(parent) {
		t.Errorf("child %d survived", parent)
	}
	if !strings.Contains(p.StderrTail(), "parent started") {
		t.Errorf("stderr tail = %q", p.StderrTail())
	}
}

func TestCloseIsGracefulWhenChildExitsOnStdinClose(t *testing.T) {
	p, err := Start(helper("stdin-exit"))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := p.Close(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("graceful close took %v", d)
	}
	if p.ExitErr() != nil {
		t.Errorf("exit = %v, want clean exit", p.ExitErr())
	}
}

func TestStartMissingCommand(t *testing.T) {
	if _, err := Start(Spec{Command: "definitely-not-a-command-mcpsh"}); err == nil {
		t.Fatal("want error")
	}
}

// TestBatchArgs checks that arguments survive a .cmd shim exactly (npx.cmd, uvx.cmd style).
func TestBatchArgs(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("batch files are Windows-only")
	}
	dir := t.TempDir()
	shim := filepath.Join(dir, "my shim.cmd")
	script := "@echo off\r\n\"" + os.Args[0] + "\" %*\r\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	args := []string{"plain", "with space", "a&b", "x|y", "<in>", "100%", "%PATH%", `q"uote`, `trail\`,
		`C:\dir with space\`, "^caret", "a^^b", "^&x", "bang!", "(paren)", "\"&\"", `a"b&c"d^e`, ""}
	s := Spec{Command: shim, Args: args, Env: append(os.Environ(), helperEnv+"=echoargs")}
	p, err := Start(s)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close(time.Second)
	var got []string
	if err := json.NewDecoder(p.Stdout).Decode(&got); err != nil {
		t.Fatalf("decode: %v (stderr: %s)", err, p.StderrTail())
	}
	if !reflect.DeepEqual(got, args) {
		t.Errorf("args through the shim:\n got  %q\n want %q", got, args)
	}
}

func TestTail(t *testing.T) {
	tl := newTail(5)
	_, _ = tl.Write([]byte("abc"))
	_, _ = tl.Write([]byte("defg"))
	if tl.String() != "cdefg" {
		t.Errorf("tail = %q", tl.String())
	}
	_ = strconv.Itoa
}
