package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
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
	states map[string]State
}

func NewManager(stderr io.Writer) *Manager {
	return &Manager{stderr: stderr, procs: map[string]*Process{}, states: map[string]State{}}
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
	m.remove(serverName, proc, StopReasonCrashed, callErr.Message)
	proc, err = m.get(ctx, serverName, srv, initParams)
	if err != nil {
		return nil, &mcp.Error{Code: -32000, Message: err.Error()}
	}
	return proc.Call(ctx, toolName, args)
}

func (m *Manager) ListTools(ctx context.Context, serverName string, srv config.Server, initParams json.RawMessage) ([]config.Tool, *mcp.Error) {
	proc, err := m.get(ctx, serverName, srv, initParams)
	if err != nil {
		return nil, &mcp.Error{Code: -32000, Message: err.Error()}
	}
	tools, listErr := proc.ListTools(ctx)
	if listErr == nil || listErr.Code != -32000 {
		return tools, listErr
	}
	m.remove(serverName, proc, StopReasonCrashed, listErr.Message)
	proc, err = m.get(ctx, serverName, srv, initParams)
	if err != nil {
		return nil, &mcp.Error{Code: -32000, Message: err.Error()}
	}
	return proc.ListTools(ctx)
}

func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, proc := range m.procs {
		proc.Stop(StopReasonShutdown, "")
		m.recordStoppedLocked(name, proc.Snapshot())
		delete(m.procs, name)
	}
}

type Status string

const (
	StatusStopped     Status = "stopped"
	StatusRunning     Status = "running"
	StatusCrashed     Status = "crashed"
	StatusIdleStopped Status = "idle-stopped"
)

type StopReason string

const (
	StopReasonNone           StopReason = ""
	StopReasonCrashed        StopReason = "crashed"
	StopReasonIdleTimeout    StopReason = "idle-timeout"
	StopReasonRequestTimeout StopReason = "request-timeout"
	StopReasonShutdown       StopReason = "shutdown"
)

type State struct {
	Status      Status
	LastStarted time.Time
	LastStopped time.Time
	StopReason  StopReason
	LastError   string
}

func NewStoppedState() State {
	return State{Status: StatusStopped}
}

func (m *Manager) States(serverNames []string) map[string]State {
	m.mu.Lock()
	defer m.mu.Unlock()

	states := make(map[string]State, len(serverNames))
	for _, name := range serverNames {
		state, ok := m.states[name]
		if !ok {
			state = NewStoppedState()
		}
		if proc := m.procs[name]; proc != nil {
			state = proc.Snapshot()
		}
		states[name] = state
	}
	return states
}

func (m *Manager) get(ctx context.Context, name string, srv config.Server, initParams json.RawMessage) (*Process, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if proc := m.procs[name]; proc != nil && proc.Running() {
		return proc, nil
	}
	proc, err := Start(ctx, srv, m.stderr)
	if err != nil {
		m.recordStartErrorLocked(name, err)
		return nil, err
	}
	m.states[name] = proc.Snapshot()
	if err := proc.Initialize(ctx, initParams); err != nil {
		proc.Stop(StopReasonCrashed, err.Error())
		proc.Wait()
		m.recordStoppedLocked(name, proc.Snapshot())
		return nil, err
	}
	m.procs[name] = proc
	go m.reap(name, proc)
	return proc, nil
}

func (m *Manager) reap(name string, proc *Process) {
	err := proc.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	state := proc.Snapshot()
	if state.Status == StatusRunning {
		if err != nil {
			proc.MarkStopped(StopReasonCrashed, err.Error())
		} else {
			proc.MarkStopped(StopReasonCrashed, "backend exited unexpectedly")
		}
		state = proc.Snapshot()
	}
	if m.procs[name] == proc {
		m.recordStoppedLocked(name, state)
		delete(m.procs, name)
	}
}

func (m *Manager) remove(name string, proc *Process, reason StopReason, lastError string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.procs[name] == proc {
		delete(m.procs, name)
	}
	proc.Stop(reason, lastError)
	m.recordStoppedLocked(name, proc.Snapshot())
}

func (m *Manager) recordStartErrorLocked(name string, err error) {
	state := m.states[name]
	if state.Status == "" {
		state.Status = StatusStopped
	}
	state.LastStopped = time.Now()
	state.StopReason = StopReasonCrashed
	state.LastError = err.Error()
	state.Status = StatusCrashed
	m.states[name] = state
}

func (m *Manager) recordStoppedLocked(name string, state State) {
	m.states[name] = state
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
	lastStarted time.Time
	lastStopped time.Time
	stopReason  StopReason
	lastError   string
}

func Start(ctx context.Context, srv config.Server, stderr io.Writer) (*Process, error) {
	cmd := exec.CommandContext(ctx, srv.Command, srv.Args...)
	if len(srv.Env) > 0 {
		cmd.Env = append(os.Environ(), envList(srv.Env)...)
	}
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
		lastStarted: time.Now(),
	}
	p.resetIdleTimer()
	return p, nil
}

func envList(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for key, value := range env {
		out = append(out, key+"="+value)
	}
	sort.Strings(out)
	return out
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

func (p *Process) ListTools(ctx context.Context) ([]config.Tool, *mcp.Error) {
	var tools []config.Tool
	var cursor *string
	for {
		page, nextCursor, listErr := p.listToolsPage(ctx, cursor)
		if listErr != nil {
			return nil, listErr
		}
		tools = append(tools, page...)
		if nextCursor == nil {
			return tools, nil
		}
		cursor = nextCursor
	}
}

func (p *Process) listToolsPage(ctx context.Context, cursor *string) ([]config.Tool, *string, *mcp.Error) {
	var params json.RawMessage
	if cursor != nil {
		raw, err := json.Marshal(map[string]any{"cursor": *cursor})
		if err != nil {
			return nil, nil, &mcp.Error{Code: -32000, Message: err.Error()}
		}
		params = raw
	}
	resp, err := p.request(ctx, "tools/list", params)
	if err != nil {
		return nil, nil, &mcp.Error{Code: -32000, Message: err.Error()}
	}
	if resp.Error != nil {
		return nil, nil, resp.Error
	}
	var result struct {
		Tools      []config.Tool `json:"tools"`
		NextCursor *string       `json:"nextCursor"`
	}
	data, err := json.Marshal(resp.Result)
	if err != nil {
		return nil, nil, &mcp.Error{Code: -32000, Message: err.Error()}
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, nil, &mcp.Error{Code: -32000, Message: "invalid tools/list result: " + err.Error()}
	}
	return result.Tools, result.NextCursor, nil
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
			p.Stop(StopReasonRequestTimeout, ctx.Err().Error())
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

func (p *Process) Wait() error {
	err := p.cmd.Wait()
	close(p.done)
	return err
}

func (p *Process) Stop(reason StopReason, lastError string) {
	p.stateMu.Lock()
	if p.stopped {
		p.stateMu.Unlock()
		return
	}
	p.markStoppedLocked(reason, lastError)
	p.stateMu.Unlock()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

func (p *Process) MarkStopped(reason StopReason, lastError string) {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	p.markStoppedLocked(reason, lastError)
}

func (p *Process) Snapshot() State {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()

	state := State{
		Status:      StatusRunning,
		LastStarted: p.lastStarted,
		LastStopped: p.lastStopped,
		StopReason:  p.stopReason,
		LastError:   p.lastError,
	}
	if p.stopped {
		switch p.stopReason {
		case StopReasonIdleTimeout:
			state.Status = StatusIdleStopped
		case StopReasonCrashed, StopReasonRequestTimeout:
			state.Status = StatusCrashed
		default:
			state.Status = StatusStopped
		}
	}
	return state
}

func (p *Process) markStoppedLocked(reason StopReason, lastError string) {
	if p.stopped {
		return
	}
	p.stopped = true
	p.lastStopped = time.Now()
	p.stopReason = reason
	p.lastError = lastError
	p.stopIdleTimerLocked()
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
	p.markStoppedLocked(StopReasonIdleTimeout, "")
	p.stateMu.Unlock()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}
