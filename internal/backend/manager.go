package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/s4na/lazymcp/internal/config"
	"github.com/s4na/lazymcp/internal/mcp"
)

type Manager struct {
	stderr io.Writer
	mu     sync.Mutex
	procs  map[string]*Process
}

func NewManager(stderr io.Writer) *Manager {
	return &Manager{stderr: stderr, procs: map[string]*Process{}}
}

func (m *Manager) Call(ctx context.Context, serverName string, srv config.Server, toolName string, args json.RawMessage, initParams json.RawMessage) (any, *mcp.Error) {
	proc, err := m.get(ctx, serverName, srv, initParams)
	if err != nil {
		return nil, &mcp.Error{Code: -32000, Message: err.Error()}
	}
	return proc.Call(ctx, toolName, args)
}

func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, proc := range m.procs {
		proc.Stop()
		delete(m.procs, name)
	}
}

func (m *Manager) get(ctx context.Context, name string, srv config.Server, initParams json.RawMessage) (*Process, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if proc := m.procs[name]; proc != nil && proc.Running() {
		return proc, nil
	}
	proc, err := Start(ctx, srv, m.stderr)
	if err != nil {
		return nil, err
	}
	if err := proc.Initialize(ctx, initParams); err != nil {
		proc.Stop()
		proc.Wait()
		return nil, err
	}
	m.procs[name] = proc
	go m.reap(name, proc)
	return proc, nil
}

func (m *Manager) reap(name string, proc *Process) {
	proc.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.procs[name] == proc {
		delete(m.procs, name)
	}
}

type Process struct {
	cmd         *exec.Cmd
	codec       *mcp.Codec
	mu          sync.Mutex
	done        chan struct{}
	idleTimeout time.Duration
	idleTimer   *time.Timer
}

func Start(ctx context.Context, srv config.Server, stderr io.Writer) (*Process, error) {
	cmd := exec.CommandContext(ctx, srv.Command, srv.Args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("spawn %s: %w", srv.CommandLine(), err)
	}
	p := &Process{
		cmd:         cmd,
		codec:       mcp.NewCodec(stdout, stdin),
		done:        make(chan struct{}),
		idleTimeout: time.Duration(srv.IdleTimeout),
	}
	p.resetIdleTimer()
	return p, nil
}

func (p *Process) Initialize(ctx context.Context, params json.RawMessage) error {
	if len(params) == 0 {
		params = json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"lazymcp","version":"0.1.0"}}`)
	}
	resp, err := p.request(ctx, "initialize", params)
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("backend initialize: %s", resp.Error.Message)
	}
	return p.codec.Write(mcp.NewNotification("notifications/initialized", map[string]any{}))
}

func (p *Process) Call(ctx context.Context, toolName string, args json.RawMessage) (any, *mcp.Error) {
	params, _ := json.Marshal(map[string]any{
		"name":      toolName,
		"arguments": json.RawMessage(args),
	})
	resp, err := p.request(ctx, "tools/call", params)
	if err != nil {
		return nil, &mcp.Error{Code: -32000, Message: err.Error()}
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	return resp.Result, nil
}

func (p *Process) request(ctx context.Context, method string, params json.RawMessage) (mcp.Message, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopIdleTimer()
	defer p.resetIdleTimer()
	id := time.Now().UnixNano()
	if err := p.codec.Write(mcp.NewRequest(id, method, params)); err != nil {
		return mcp.Message{}, err
	}
	for {
		select {
		case <-ctx.Done():
			return mcp.Message{}, ctx.Err()
		default:
		}
		msg, err := p.codec.Read()
		if err != nil {
			return mcp.Message{}, err
		}
		if idsEqual(msg.ID, id) {
			return msg, nil
		}
	}
}

func (p *Process) Running() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

func (p *Process) Wait() {
	_ = p.cmd.Wait()
	close(p.done)
}

func (p *Process) Stop() {
	if p.idleTimer != nil {
		p.idleTimer.Stop()
	}
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

func (p *Process) resetIdleTimer() {
	if p.idleTimeout <= 0 {
		return
	}
	if p.idleTimer == nil {
		p.idleTimer = time.AfterFunc(p.idleTimeout, p.Stop)
		return
	}
	p.idleTimer.Reset(p.idleTimeout)
}

func (p *Process) stopIdleTimer() {
	if p.idleTimer != nil {
		p.idleTimer.Stop()
	}
}

func idsEqual(got any, want int64) bool {
	switch v := got.(type) {
	case float64:
		return int64(v) == want
	case int64:
		return v == want
	case int:
		return int64(v) == want
	default:
		return fmt.Sprint(v) == fmt.Sprint(want)
	}
}
