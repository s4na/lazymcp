package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/s4na/lazymcp/internal/config"
	"github.com/s4na/lazymcp/internal/mcp"
)

func TestToolsListDiscoversUnconfiguredBackendTools(t *testing.T) {
	startCountPath := filepath.Join(t.TempDir(), "starts")
	cfg := &config.Config{Servers: map[string]config.Server{
		"github": helperServer(startCountPath),
	}}
	if err := cfg.SetServerTools("github", nil); err != nil {
		t.Fatalf("initialize config routes: %v", err)
	}

	var out bytes.Buffer
	var stderr bytes.Buffer
	srv := New(cfg, bytes.NewReader(nil), &out, &stderr)
	if err := srv.handle(context.Background(), mcp.Message{
		JSONRPC: "2.0",
		ID:      "1",
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}`),
	}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if err := srv.handle(context.Background(), mcp.Message{JSONRPC: "2.0", ID: "2", Method: "tools/list"}); err != nil {
		t.Fatalf("tools/list: %v", err)
	}

	reader := mcp.NewCodec(&out, io.Discard)
	if _, err := reader.Read(); err != nil {
		t.Fatalf("read initialize response: %v", err)
	}
	listResponse, err := reader.Read()
	if err != nil {
		t.Fatalf("read tools/list response: %v", err)
	}
	tools := resultTools(t, listResponse.Result)
	if len(tools) != 1 || tools[0].Name != "github.ping" {
		t.Fatalf("tools = %#v, want github.ping", tools)
	}
	if _, _, backendTool, ok := cfg.ServerForTool("github.ping"); !ok || backendTool != "ping" {
		t.Fatalf("route for github.ping = %q, %t; want ping route", backendTool, ok)
	}
	if got := readStartCount(t, startCountPath); got != 1 {
		t.Fatalf("backend starts = %d, want 1", got)
	}
	srv.backends.Shutdown()
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_SERVER_HELPER_PROCESS") != "1" {
		return
	}

	incrementStartCount(os.Getenv("LAZYMCP_START_COUNT_PATH"))
	codec := mcp.NewCodec(os.Stdin, os.Stdout)
	for {
		msg, err := codec.Read()
		if err != nil {
			os.Exit(0)
		}
		switch msg.Method {
		case "initialize":
			_ = codec.Write(mcp.NewResult(msg.ID, map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "helper", "version": "test"},
			}))
		case "notifications/initialized":
		case "tools/list":
			_ = codec.Write(mcp.NewResult(msg.ID, map[string]any{"tools": []map[string]any{
				{
					"name":        "ping",
					"description": "Ping the helper backend.",
					"inputSchema": map[string]any{"type": "object"},
				},
			}}))
		default:
			_ = codec.Write(mcp.NewError(msg.ID, -32601, "method not found"))
		}
	}
}

func helperServer(startCountPath string) config.Server {
	return config.Server{
		Command: "env",
		Args: []string{
			"GO_WANT_SERVER_HELPER_PROCESS=1",
			"LAZYMCP_START_COUNT_PATH=" + startCountPath,
			os.Args[0],
			"-test.run=TestHelperProcess",
			"--",
		},
		IdleTimeout:    config.Duration(time.Minute),
		RequestTimeout: config.Duration(time.Second),
	}
}

func resultTools(t *testing.T, result any) []config.Tool {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var payload struct {
		Tools []config.Tool `json:"tools"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal tools: %v", err)
	}
	return payload.Tools
}

func incrementStartCount(path string) {
	count := 0
	if data, err := os.ReadFile(path); err == nil {
		count, _ = strconv.Atoi(string(data))
	}
	_ = os.WriteFile(path, []byte(strconv.Itoa(count+1)), 0o600)
}

func readStartCount(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read start count: %v", err)
	}
	count, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatalf("parse start count: %v", err)
	}
	return count
}
