package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/s4na/lazymcp/internal/backend"
	"github.com/s4na/lazymcp/internal/config"
	"github.com/s4na/lazymcp/internal/mcp"
)

type Server struct {
	cfg                    *config.Config
	codec                  *mcp.Codec
	backends               *backend.Manager
	stderr                 io.Writer
	initParams             json.RawMessage
	toolDiscoveryAttempted map[string]bool
}

const toolDiscoveryTimeout = 30 * time.Second

func New(cfg *config.Config, stdin io.Reader, stdout io.Writer, stderr io.Writer) *Server {
	return &Server{
		cfg:                    cfg,
		codec:                  mcp.NewCodec(stdin, stdout),
		backends:               backend.NewManager(stderr),
		stderr:                 stderr,
		toolDiscoveryAttempted: map[string]bool{},
	}
}

func (s *Server) Run(ctx context.Context) error {
	defer s.backends.Shutdown()
	for {
		msg, err := s.codec.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if msg.ID == nil {
			continue
		}
		if err := s.handle(ctx, msg); err != nil {
			return err
		}
	}
}

func (s *Server) handle(ctx context.Context, msg mcp.Message) error {
	switch msg.Method {
	case "initialize":
		s.initParams = msg.Params
		return s.codec.Write(mcp.NewResult(msg.ID, map[string]any{
			"protocolVersion": "2024-11-05",
			"serverInfo": map[string]any{
				"name":    "lazymcp",
				"version": "0.1.0",
			},
			"capabilities": map[string]any{
				"tools": map[string]any{"listChanged": false},
			},
		}))
	case "tools/list":
		s.discoverUnconfiguredTools(ctx)
		return s.codec.Write(mcp.NewResult(msg.ID, map[string]any{"tools": s.cfg.Tools()}))
	case "tools/call":
		return s.handleToolCall(ctx, msg)
	default:
		return s.codec.Write(mcp.NewError(msg.ID, -32601, "method not found"))
	}
}

func (s *Server) discoverUnconfiguredTools(ctx context.Context) {
	for _, name := range s.cfg.ServerNames() {
		srv := s.cfg.Servers[name]
		if len(srv.Tools) > 0 || s.toolDiscoveryAttempted[name] {
			continue
		}
		s.toolDiscoveryAttempted[name] = true
		discoveryCtx, cancel := context.WithTimeout(ctx, toolDiscoveryTimeout)
		tools, listErr := s.backends.ListTools(discoveryCtx, name, srv, s.initParams)
		cancel()
		if listErr != nil {
			fmt.Fprintf(s.stderr, "lazymcp: failed to discover tools for %s: %s\n", name, listErr.Message)
			continue
		}
		if err := s.cfg.SetServerTools(name, tools); err != nil {
			fmt.Fprintf(s.stderr, "lazymcp: failed to register discovered tools for %s: %s\n", name, err)
		}
	}
}

func (s *Server) handleToolCall(ctx context.Context, msg mcp.Message) error {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return s.codec.Write(mcp.NewError(msg.ID, -32602, "invalid tools/call params"))
	}
	serverName, srv, backendToolName, ok := s.cfg.ServerForTool(params.Name)
	if !ok {
		return s.codec.Write(mcp.NewError(msg.ID, -32602, "unknown tool namespace"))
	}
	result, callErr := s.backends.Call(ctx, serverName, srv, backendToolName, params.Arguments, s.initParams)
	if callErr != nil {
		return s.codec.Write(mcp.Message{JSONRPC: "2.0", ID: msg.ID, Error: callErr})
	}
	return s.codec.Write(mcp.NewResult(msg.ID, result))
}
