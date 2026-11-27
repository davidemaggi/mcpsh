package daemon

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/davidemaggi/mcpsh/internal/config"
)

func TestBackoff(t *testing.T) {
	want := []time.Duration{0, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	for failures, w := range want {
		if got := backoffFor(failures); got != w {
			t.Errorf("backoffFor(%d) = %v, want %v", failures, got, w)
		}
	}
}

func TestSweepIdle(t *testing.T) {
	m := NewManager(slog.New(slog.NewTextHandler(io.Discard, nil)), time.Minute)
	now := time.Now()
	rs := &config.Resolved{Name: "s", ID: "id1", IdleTimeout: time.Second}
	busy := &config.Resolved{Name: "b", ID: "id2", IdleTimeout: time.Second}
	m.sessions["id1"] = &session{rs: rs, key: "k1", lastUsed: now.Add(-2 * time.Second)}
	m.sessions["id2"] = &session{rs: busy, key: "k2", lastUsed: now.Add(-2 * time.Second), inflight: 1}
	m.keys["k1"], m.keys["k2"] = "id1", "id2"
	m.lastActivity = now.Add(-2 * time.Minute)

	if m.sweep(now) {
		t.Error("daemon reported idle while a call is in flight")
	}
	if _, ok := m.sessions["id1"]; ok {
		t.Error("idle session not closed")
	}
	if _, ok := m.sessions["id2"]; !ok {
		t.Error("busy session closed")
	}
	m.sessions["id2"].inflight = 0
	m.inflight = 0
	m.sweep(now) // closes id2
	if !m.sweep(now) {
		t.Error("daemon with no sessions and no activity should be idle")
	}
}
