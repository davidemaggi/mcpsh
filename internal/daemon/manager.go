// Package daemon keeps MCP sessions alive between CLI invocations. The CLI resolves the
// config with its own environment and sends the resolved server definition; sessions are keyed
// by its ID (a hash of the definition and working directory), started lazily, restarted with
// backoff after a crash, and closed after their idle timeout. The daemon exits when it has been
// idle for its own timeout.
package daemon

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/davidemaggi/mcpsh/internal/config"
	"github.com/davidemaggi/mcpsh/internal/ipc"
	"github.com/davidemaggi/mcpsh/internal/logging"
	"github.com/davidemaggi/mcpsh/internal/mcpclient"
	"github.com/davidemaggi/mcpsh/internal/toolcache"
)

const (
	maxBackoff   = 30 * time.Second
	sweepEvery   = 500 * time.Millisecond
	refreshAfter = 30 * time.Second // timeout of the list refresh after tools/list_changed
)

type session struct {
	rs  *config.Resolved
	key string

	mu        sync.Mutex // serializes connecting
	conn      *mcpclient.Conn
	state     string // starting, running, crashed
	startedAt time.Time
	lastUsed  time.Time
	inflight  int
	calls     int64
	restarts  int
	failures  int // consecutive failures, drives the backoff
	lastErr   string
	backoff   time.Time // no new start before this time
	retired   bool      // replaced by a new definition: close when idle
}

// Manager owns the sessions.
type Manager struct {
	logger *slog.Logger

	mu           sync.Mutex
	sessions     map[string]*session // by Resolved.ID
	keys         map[string]string   // definition key -> ID
	inflight     int
	lastActivity time.Time
	idleTimeout  time.Duration
}

// NewManager returns an empty manager.
func NewManager(logger *slog.Logger, idle time.Duration) *Manager {
	return &Manager{
		logger:       logger,
		sessions:     map[string]*session{},
		keys:         map[string]string{},
		lastActivity: time.Now(),
		idleTimeout:  idle,
	}
}

// SetIdleTimeout updates the daemon's own idle timeout (sent by each CLI request).
func (m *Manager) SetIdleTimeout(d time.Duration) {
	if d <= 0 {
		return
	}
	m.mu.Lock()
	m.idleTimeout = d
	m.mu.Unlock()
}

// acquire returns a connected session for rs, starting the server if needed. The caller must
// call m.release(s) when done.
func (m *Manager) acquire(ctx context.Context, rs *config.Resolved, key string) (*session, *mcpclient.Conn, error) {
	m.mu.Lock()
	if old, ok := m.keys[key]; ok && old != rs.ID {
		if s := m.sessions[old]; s != nil {
			m.logger.Info("definition changed, retiring the old session", "server", rs.Name)
			s.retired = true
			delete(m.sessions, old)
			if s.inflight == 0 {
				go m.closeSession(s, "definition changed")
			}
		}
	}
	m.keys[key] = rs.ID
	s := m.sessions[rs.ID]
	if s == nil {
		s = &session{rs: rs, key: key, state: "starting"}
		m.sessions[rs.ID] = s
	}
	s.inflight++
	m.inflight++
	m.lastActivity = time.Now()
	m.mu.Unlock()

	c, err := m.connect(ctx, s)
	if err != nil {
		m.release(s, false)
		return nil, nil, err
	}
	return s, c, nil
}

func (m *Manager) connect(ctx context.Context, s *session) (*mcpclient.Conn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		return s.conn, nil
	}
	if wait := time.Until(s.backoff); wait > 0 {
		m.logger.Info("waiting before restarting the server", "server", s.rs.Name, "wait", wait.Round(time.Millisecond))
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	m.setState(s, "starting")
	m.logger.Info("starting server", "server", s.rs.Name, "id", s.rs.ID, "target", s.rs.Describe())
	stderr := &logging.LineWriter{Logger: m.logger, Msg: "server stderr", Attrs: []any{"server", s.rs.Name}}
	// The server keeps starting even if the request that triggered it goes away: the
	// startup timeout bounds it, and the next request will use it.
	c, err := mcpclient.Connect(context.WithoutCancel(ctx), s.rs, mcpclient.Options{
		Logger:         m.logger,
		Stderr:         stderr,
		OnToolsChanged: func() { go m.refreshTools(s) },
	})
	if err != nil {
		s.failures++
		s.lastErr = firstLine(err.Error())
		s.backoff = time.Now().Add(backoffFor(s.failures))
		m.setState(s, "crashed")
		m.logger.Warn("server failed to start", "server", s.rs.Name, "error", s.lastErr)
		return nil, err
	}
	s.conn = c
	s.startedAt = time.Now()
	m.setState(s, "running")
	m.logger.Info("server started", "server", s.rs.Name, "pid", c.Pid())
	go m.watch(s, c)
	return c, nil
}

func (m *Manager) setState(s *session, state string) {
	m.mu.Lock()
	s.state = state
	m.mu.Unlock()
}

func backoffFor(failures int) time.Duration {
	if failures <= 0 {
		return 0
	}
	d := time.Second << min(failures-1, 5)
	return min(d, maxBackoff)
}

// watch notices a session that ends on its own (crash, server exit).
func (m *Manager) watch(s *session, c *mcpclient.Conn) {
	<-c.Done()
	s.mu.Lock()
	if s.conn != c { // closed on purpose
		s.mu.Unlock()
		return
	}
	s.conn = nil
	s.restarts++
	s.failures++
	s.backoff = time.Now().Add(backoffFor(s.failures))
	msg := "server exited"
	if tail := c.StderrTail(); tail != "" {
		lines := strings.Split(tail, "\n")
		msg += ": " + lines[len(lines)-1]
	}
	s.lastErr = msg
	s.mu.Unlock()
	m.setState(s, "crashed")
	_ = c.Close() // reap the process tree
	m.logger.Warn("server exited unexpectedly", "server", s.rs.Name, "restarts", s.restarts)
}

func (m *Manager) release(s *session, ok bool) {
	m.mu.Lock()
	s.inflight--
	m.inflight--
	s.lastUsed = time.Now()
	m.lastActivity = s.lastUsed
	if ok {
		s.calls++
	}
	closeNow := s.retired && s.inflight == 0
	m.mu.Unlock()
	if ok {
		s.mu.Lock()
		s.failures = 0
		s.mu.Unlock()
	}
	if closeNow {
		go m.closeSession(s, "definition changed")
	}
}

func (m *Manager) closeSession(s *session, why string) {
	s.mu.Lock()
	c := s.conn
	s.conn = nil
	s.mu.Unlock()
	if c != nil {
		m.logger.Info("stopping server", "server", s.rs.Name, "reason", why)
		_ = c.Close()
	}
}

// ListTools fetches the tools and refreshes the discovery cache.
func (m *Manager) ListTools(ctx context.Context, rs *config.Resolved, key string, timeout time.Duration) ([]*mcp.Tool, error) {
	s, c, err := m.acquire(ctx, rs, key)
	if err != nil {
		return nil, err
	}
	cctx, cancel := withTimeout(ctx, timeout)
	defer cancel()
	tools, err := c.ListTools(cctx)
	m.release(s, err == nil)
	if err != nil {
		return nil, err
	}
	if err := toolcache.Put(rs.ID, rs.Name, tools); err != nil {
		m.logger.Warn("cannot write the tool cache", "server", rs.Name, "error", err)
	}
	return tools, nil
}

// CallTool calls a tool on the session for rs.
func (m *Manager) CallTool(ctx context.Context, rs *config.Resolved, key, tool string, args map[string]any, timeout time.Duration) (*mcp.CallToolResult, error) {
	s, c, err := m.acquire(ctx, rs, key)
	if err != nil {
		return nil, err
	}
	cctx, cancel := withTimeout(ctx, timeout)
	defer cancel()
	res, err := c.CallTool(cctx, tool, args)
	var se *mcpclient.ServerError
	m.release(s, err == nil || errors.As(err, &se))
	return res, err
}

func withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, d)
}

// refreshTools re-reads the tool list after notifications/tools/list_changed.
func (m *Manager) refreshTools(s *session) {
	s.mu.Lock()
	c := s.conn
	s.mu.Unlock()
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), refreshAfter)
	defer cancel()
	tools, err := c.ListTools(ctx)
	if err != nil {
		m.logger.Warn("cannot refresh tools after list_changed", "server", s.rs.Name, "error", err)
		return
	}
	if err := toolcache.Put(s.rs.ID, s.rs.Name, tools); err != nil {
		m.logger.Warn("cannot write the tool cache", "server", s.rs.Name, "error", err)
		return
	}
	m.logger.Info("tool list changed, cache updated", "server", s.rs.Name, "tools", len(tools))
}

// sweep closes idle sessions, forgets old crashed ones and reports whether the daemon itself
// has been idle long enough to exit.
func (m *Manager) sweep(now time.Time) (daemonIdle bool) {
	m.mu.Lock()
	var toClose []*session
	for id, s := range m.sessions {
		if s.inflight > 0 {
			continue
		}
		last := s.lastUsed
		if last.IsZero() {
			last = s.startedAt
		}
		if now.Sub(last) < s.rs.IdleTimeout {
			continue
		}
		delete(m.sessions, id)
		if m.keys[s.key] == id {
			delete(m.keys, s.key)
		}
		toClose = append(toClose, s)
	}
	idle := m.inflight == 0 && len(m.sessions) == 0 && now.Sub(m.lastActivity) >= m.idleTimeout
	m.mu.Unlock()
	for _, s := range toClose {
		m.closeSession(s, "idle timeout")
	}
	return idle
}

// CloseAll stops every server (daemon shutdown).
func (m *Manager) CloseAll() {
	m.mu.Lock()
	all := make([]*session, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	m.sessions = map[string]*session{}
	m.keys = map[string]string{}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range all {
		wg.Add(1)
		go func() { defer wg.Done(); m.closeSession(s, "daemon shutdown") }()
	}
	wg.Wait()
}

// Status snapshots the sessions.
func (m *Manager) Status() ([]ipc.SessionStatus, time.Duration) {
	m.mu.Lock()
	sessions := make([]*session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	idle := m.idleTimeout
	m.mu.Unlock()
	out := make([]ipc.SessionStatus, 0, len(sessions))
	for _, s := range sessions {
		st := ipc.SessionStatus{Name: s.rs.Name, ID: s.rs.ID, File: s.rs.File, Target: s.rs.Describe()}
		// s.mu is held while a server starts: skip the details then instead of blocking.
		if s.mu.TryLock() {
			if s.conn != nil {
				st.Pid = s.conn.Pid()
			}
			st.StartedAt, st.Restarts, st.LastError = s.startedAt, s.restarts, s.lastErr
			s.mu.Unlock()
		}
		m.mu.Lock()
		st.State, st.LastUsed, st.Inflight, st.Calls = s.state, s.lastUsed, s.inflight, s.calls
		m.mu.Unlock()
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, idle
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}
