// Package logging writes the daemon log: a rotating file in the user's state directory.
//
// Secrets never reach it by construction: callers log server names, IDs and secret-free
// descriptions (config.Resolved.Describe), never env values, headers or tool arguments.
package logging

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/davidemaggi/mcpsh/internal/platform"
)

// Path is the daemon log file.
func Path() (string, error) {
	dir, err := platform.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "logs", "daemon.log"), nil
}

// Open returns a logger writing to the rotating daemon log (5 MB per file, 3 old files kept).
func Open(level slog.Level) (*slog.Logger, io.Closer, error) {
	p, err := Path()
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, nil, err
	}
	w := &lumberjack.Logger{Filename: p, MaxSize: 5, MaxBackups: 3, Compress: false}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})), w, nil
}

// LineWriter turns a byte stream (a server's stderr) into one log record per line.
type LineWriter struct {
	Logger *slog.Logger
	Msg    string
	Attrs  []any

	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *LineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	for {
		line, err := w.buf.ReadString('\n')
		if err != nil {
			// Incomplete line: keep it for the next write (bounded).
			if len(line) > 8<<10 {
				line = line[:8<<10]
				w.Logger.Info(w.Msg, append(w.Attrs, "line", line)...)
				line = ""
			}
			w.buf.Reset()
			w.buf.WriteString(line)
			return len(p), nil
		}
		if l := strings.TrimRight(line, "\r\n"); l != "" {
			w.Logger.Info(w.Msg, append(w.Attrs, "line", l)...)
		}
	}
}
