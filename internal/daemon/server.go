package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/davidemaggi/mcpsh/internal/config"
	"github.com/davidemaggi/mcpsh/internal/ipc"
	"github.com/davidemaggi/mcpsh/internal/logging"
	"github.com/davidemaggi/mcpsh/internal/mcpclient"
)

// Run is the daemon process ("mcpsh daemon run"). It returns when the daemon stops.
// If another daemon already runs for this state directory it returns ipc.ErrLocked.
func Run() error {
	lockPath, err := ipc.LockPath()
	if err != nil {
		return err
	}
	lock, err := ipc.TryLock(lockPath)
	if err != nil {
		return err
	}
	defer lock.Release()

	logger, closer, err := logging.Open(slog.LevelInfo)
	if err != nil {
		return err
	}
	defer closer.Close()

	addr, err := ipc.Address()
	if err != nil {
		return err
	}
	l, err := ipc.Listen(addr)
	if err != nil {
		logger.Error("cannot listen", "address", addr, "error", err)
		return err
	}
	d := &daemon{
		logger:  logger,
		manager: NewManager(logger, config.DefaultDaemonIdleTimeout),
		info: ipc.DaemonInfo{
			BuildInfo: ipc.Self(), Proto: ipc.ProtocolVersion, Pid: os.Getpid(), StartedAt: time.Now(),
		},
		listener: l,
		stop:     make(chan struct{}),
	}
	logger.Info("daemon started", "pid", d.info.Pid, "version", d.info.Version, "address", addr)
	return d.serve()
}

type daemon struct {
	logger   *slog.Logger
	manager  *Manager
	info     ipc.DaemonInfo
	listener net.Listener

	stopOnce sync.Once
	stop     chan struct{}
	conns    sync.WaitGroup
}

func (d *daemon) shutdown(why string) {
	d.stopOnce.Do(func() {
		d.logger.Info("daemon stopping", "reason", why)
		close(d.stop)
		d.listener.Close()
	})
}

func (d *daemon) serve() error {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	go func() {
		select {
		case s := <-sig:
			d.shutdown("signal " + s.String())
		case <-d.stop:
		}
	}()
	go func() {
		t := time.NewTicker(sweepEvery)
		defer t.Stop()
		for {
			select {
			case now := <-t.C:
				if d.manager.sweep(now) {
					d.shutdown("idle timeout")
					return
				}
			case <-d.stop:
				return
			}
		}
	}()
	for {
		c, err := d.listener.Accept()
		if err != nil {
			select {
			case <-d.stop:
			default:
				d.logger.Error("accept failed", "error", err)
				d.shutdown("listener error")
			}
			break
		}
		d.conns.Add(1)
		go func() {
			defer d.conns.Done()
			d.handle(c)
		}()
	}
	// Let in-flight requests finish briefly, then stop every server.
	done := make(chan struct{})
	go func() { d.conns.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
	d.manager.CloseAll()
	d.logger.Info("daemon stopped")
	return nil
}

func (d *daemon) handle(c net.Conn) {
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	dec := json.NewDecoder(c)
	var req ipc.Request
	if err := dec.Decode(&req); err != nil {
		return
	}
	_ = c.SetReadDeadline(time.Time{})

	// The client cancels by closing the connection: watch for EOF.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, _ = io.Copy(io.Discard, io.MultiReader(dec.Buffered(), c))
		cancel()
	}()

	resp := d.dispatch(ctx, &req)
	resp.Proto = ipc.ProtocolVersion
	resp.Daemon = d.info
	_ = json.NewEncoder(c).Encode(resp)
	if resp.Error != nil && resp.Error.Kind == ipc.KindUpgrade {
		d.shutdown("replaced by a newer mcpsh")
	}
}

func (d *daemon) dispatch(ctx context.Context, req *ipc.Request) *ipc.Response {
	switch {
	case req.Proto > ipc.ProtocolVersion, req.Proto == ipc.ProtocolVersion && ipc.Newer(req.Client, d.info.BuildInfo):
		return errResp(ipc.KindUpgrade, "", "the daemon is older than this mcpsh and is restarting")
	case req.Proto < ipc.ProtocolVersion:
		return errResp(ipc.KindVersion, "", fmt.Sprintf(
			"the running daemon (%s) is newer than this mcpsh (%s); run \"mcpsh daemon restart\" or update mcpsh",
			d.info.Version, req.Client.Version))
	}
	if req.DaemonIdleMs > 0 {
		d.manager.SetIdleTimeout(time.Duration(req.DaemonIdleMs) * time.Millisecond)
	}
	timeout := time.Duration(req.TimeoutMs) * time.Millisecond
	switch req.Op {
	case ipc.OpStatus:
		sessions, idle := d.manager.Status()
		return &ipc.Response{Status: &ipc.Status{Daemon: d.info, IdleTimeoutMs: idle.Milliseconds(), Sessions: sessions}}
	case ipc.OpShutdown:
		go d.shutdown("stop requested")
		return &ipc.Response{}
	case ipc.OpListTools, ipc.OpCallTool:
		if req.Server == nil {
			return errResp(ipc.KindInternal, "", "missing server definition")
		}
		d.manager.mu.Lock()
		d.manager.lastActivity = time.Now()
		d.manager.mu.Unlock()
		if req.Op == ipc.OpListTools {
			tools, err := d.manager.ListTools(ctx, req.Server, req.Key, timeout)
			if err != nil {
				return &ipc.Response{Error: toWire(err)}
			}
			b, err := json.Marshal(tools)
			if err != nil {
				return errResp(ipc.KindInternal, req.Server.Name, err.Error())
			}
			return &ipc.Response{Tools: b}
		}
		res, err := d.manager.CallTool(ctx, req.Server, req.Key, req.Tool, req.Args, timeout)
		if err != nil {
			return &ipc.Response{Error: toWire(err)}
		}
		b, err := json.Marshal(res)
		if err != nil {
			return errResp(ipc.KindInternal, req.Server.Name, err.Error())
		}
		return &ipc.Response{Result: b}
	}
	return errResp(ipc.KindInternal, "", "unknown operation "+req.Op)
}

func errResp(kind, server, msg string) *ipc.Response {
	return &ipc.Response{Error: &ipc.WireError{Kind: kind, Server: server, Message: msg}}
}

// toWire classifies an error for the client, which rebuilds the same error type, so messages
// and exit codes match --no-daemon.
func toWire(err error) *ipc.WireError {
	var (
		ce  *mcpclient.ConnectError
		te  *mcpclient.TransportError
		toe *mcpclient.TimeoutError
		se  *mcpclient.ServerError
	)
	switch {
	case errors.As(err, &ce):
		return &ipc.WireError{Kind: ipc.KindConnect, Server: ce.Server, Message: ce.Err.Error(), Stderr: ce.Stderr}
	case errors.As(err, &te):
		return &ipc.WireError{Kind: ipc.KindTransport, Server: te.Server, Message: te.Err.Error(), Stderr: te.Stderr}
	case errors.As(err, &toe):
		return &ipc.WireError{Kind: ipc.KindTimeout, Server: toe.Server}
	case errors.As(err, &se):
		return &ipc.WireError{Kind: ipc.KindServer, Server: se.Server, Message: se.Message, Code: se.Code}
	}
	return &ipc.WireError{Kind: ipc.KindInternal, Message: err.Error()}
}
