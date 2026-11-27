//go:build windows

package proc

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const processSuspendResume = 0x0800

var ntResumeProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

// command resolves the executable. Batch files (.cmd/.bat, such as npx.cmd) are run through
// cmd.exe with an explicitly escaped command line: CreateProcess would start cmd.exe for them
// anyway, but without escaping cmd metacharacters (& | < > ^ % ...) in the arguments.
func command(s Spec) (*exec.Cmd, error) {
	name := s.Command
	if !filepath.IsAbs(name) && strings.ContainsAny(name, `\/`) && s.Dir != "" {
		name = filepath.Join(s.Dir, name)
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return nil, fmt.Errorf("command %q not found: %w", s.Command, err)
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".cmd" && ext != ".bat" {
		cmd := exec.Command(path, s.Args...)
		return cmd, nil
	}
	comspec := os.Getenv("ComSpec")
	if comspec == "" {
		comspec = filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	}
	cmd := exec.Command(comspec)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: BatchCommandLine(path, s.Args)}
	return cmd, nil
}

// BatchCommandLine builds `/d /s /c ""script" args..."` for cmd.exe.
//
// The arguments are parsed twice by cmd: once for this command line, and again when the batch
// file expands %* (or %1...). For the second parse, metacharacters are caret-escaped only where
// cmd will be outside a quoted section, tracking every '"' the way cmd does (it knows nothing
// about \" escapes). For the first parse every metacharacter, quotes included, is escaped once
// more, so this line never enters quote mode and %VAR% is never expanded.
func BatchCommandLine(script string, args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = quoteArg(a)
	}
	second := escapeOutsideQuotes(strings.Join(parts, " "))
	var b strings.Builder
	b.WriteString(`/d /s /c ""` + script + `"`)
	if len(args) > 0 {
		b.WriteByte(' ')
		b.WriteString(escapeAll(second))
	}
	b.WriteByte('"')
	return b.String()
}

// quoteArg always wraps s in double quotes, escaping quotes and the backslashes before them
// as CommandLineToArgvW expects.
func quoteArg(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\\':
			slashes++
		case '"':
			b.WriteString(strings.Repeat(`\`, slashes+1))
			slashes = 0
		default:
			slashes = 0
		}
		b.WriteByte(c)
	}
	b.WriteString(strings.Repeat(`\`, slashes))
	b.WriteByte('"')
	return b.String()
}

// escapeOutsideQuotes caret-escapes cmd metacharacters that cmd would see outside quotes.
func escapeOutsideQuotes(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		if r == '"' {
			in = !in
		} else if !in && strings.ContainsRune(`()<>&|^`, r) {
			b.WriteByte('^')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// escapeAll caret-escapes every cmd metacharacter.
func escapeAll(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`()%!^"<>&|`, r) {
			b.WriteByte('^')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// prepare starts the child suspended, without a console window: it is resumed only after it
// joined its job object, so nothing it spawns can escape the job.
func prepare(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW
}

type jobTree struct {
	job windows.Handle
}

func newTree() (tree, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("creating job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("configuring job object: %w", err)
	}
	return &jobTree{job: job}, nil
}

func (j *jobTree) attach(p *os.Process) error {
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|processSuspendResume,
		false, uint32(p.Pid))
	if err != nil {
		return fmt.Errorf("opening child process: %w", err)
	}
	defer windows.CloseHandle(h)
	assignErr := windows.AssignProcessToJobObject(j.job, h)
	if r, _, _ := ntResumeProcess.Call(uintptr(h)); r != 0 {
		return fmt.Errorf("resuming child process: NTSTATUS 0x%x", r)
	}
	if assignErr != nil {
		return fmt.Errorf("adding child to its job object: %w", assignErr)
	}
	return nil
}

// signal: Windows has no SIGTERM for console processes we do not share a console with.
func (j *jobTree) signal() error { return errors.ErrUnsupported }

func (j *jobTree) kill() error {
	if j.job == 0 {
		return nil
	}
	return windows.TerminateJobObject(j.job, 1)
}

func (j *jobTree) release() {
	if j.job != 0 {
		windows.CloseHandle(j.job) // KILL_ON_JOB_CLOSE: anything left dies here
		j.job = 0
	}
}
