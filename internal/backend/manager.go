package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
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
	result, callErr := proc.Call(ctx, toolName, args)
	if callErr == nil || callErr.Code != -32000 {
		return result, callErr
	}
	m.remove(serverName, proc)
	proc, err = m.get(ctx, serverName, srv, initParams)
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

func (m *Manager) remove(name string, proc *Process) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.procs[name] == proc {
		delete(m.procs, name)
	}
	proc.Stop()
}

type Process struct {
	cmd         *exec.Cmd
	codec       *mcp.Codec
	mu          sync.Mutex
	stateMu     sync.Mutex
	done        chan struct{}
	idleTimeout time.Duration
	reqTimeout  time.Duration
	idleTimer   *time.Timer
	nextID      uint64
	inFlight    bool
	stopped     bool
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
		reqTimeout:  time.Duration(srv.RequestTimeout),
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
	p.beginRequest()
	defer p.endRequest()
	if p.reqTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.reqTimeout)
		defer cancel()
	}
	p.nextID++
	id := strconv.FormatUint(p.nextID, 10)
	if err := p.codec.Write(mcp.NewRequest(id, method, params)); err != nil {
		return mcp.Message{}, err
	}
	for {
		result := make(chan readResult, 1)
		go func() {
			msg, err := p.codec.Read()
			result <- readResult{msg: msg, err: err}
		}()
		select {
		case <-ctx.Done():
			p.Stop()
			<-result
			return mcp.Message{}, ctx.Err()
		case got := <-result:
			if got.err != nil {
				return mcp.Message{}, got.err
			}
			if idsEqual(got.msg.ID, id) {
				return got.msg, nil
			}
		}
	}
}

type readResult struct {
	msg mcp.Message
	err error
}

func idsEqual(got any, want string) bool {
	switch v := got.(type) {
	case string:
		return v == want
	case float64:
		return strconv.FormatInt(int64(v), 10) == want
	case int64:
		return strconv.FormatInt(v, 10) == want
	case int:
		return strconv.Itoa(v) == want
	default:
		return fmt.Sprint(v) == want
	}
}

func (p *Process) Running() bool {
	p.stateMu.Lock()
	stopped := p.stopped
	p.stateMu.Unlock()
	if stopped {
		return false
	}
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
	p.stateMu.Lock()
	if p.stopped {
		p.stateMu.Unlock()
		return
	}
	p.stopped = true
	p.stopIdleTimerLocked()
	p.stateMu.Unlock()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

func (p *Process) beginRequest() {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	p.inFlight = true
	p.stopIdleTimerLocked()
}

func (p *Process) endRequest() {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	p.inFlight = false
	p.resetIdleTimerLocked()
}

func (p *Process) resetIdleTimer() {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	p.resetIdleTimerLocked()
}

func (p *Process) resetIdleTimerLocked() {
	if p.stopped || p.inFlight {
		return
	}
	if p.idleTimeout <= 0 {
		return
	}
	if p.idleTimer == nil {
		p.idleTimer = time.AfterFunc(p.idleTimeout, p.stopIfIdle)
		return
	}
	p.idleTimer.Reset(p.idleTimeout)
}

func (p *Process) stopIdleTimerLocked() {
	if p.idleTimer != nil {
		p.idleTimer.Stop()
	}
}

func (p *Process) stopIfIdle() {
	p.stateMu.Lock()
	if p.inFlight || p.stopped {
		p.stateMu.Unlock()
		return
	}
	p.stopped = true
	p.stateMu.Unlock()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}
