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

func TestToolsListDiscoversPaginatedBackendTools(t *testing.T) {
	startCountPath := filepath.Join(t.TempDir(), "starts")
	cfg := &config.Config{Servers: map[string]config.Server{
		"github": helperServerWithEnv(startCountPath, "LAZYMCP_PAGINATED_TOOLS=1"),
	}}
	if err := cfg.SetServerTools("github", nil); err != nil {
		t.Fatalf("initialize config routes: %v", err)
	}

	var out bytes.Buffer
	var stderr bytes.Buffer
	srv := New(cfg, bytes.NewReader(nil), &out, &stderr)
	if err := srv.handle(context.Background(), mcp.Message{JSONRPC: "2.0", ID: "1", Method: "tools/list"}); err != nil {
		t.Fatalf("tools/list: %v", err)
	}

	reader := mcp.NewCodec(&out, io.Discard)
	listResponse, err := reader.Read()
	if err != nil {
		t.Fatalf("read tools/list response: %v", err)
	}
	tools := resultTools(t, listResponse.Result)
	if len(tools) != 2 || tools[0].Name != "github.first" || tools[1].Name != "github.second" {
		t.Fatalf("tools = %#v, want paginated tools with namespace", tools)
	}
	srv.backends.Shutdown()
}

func TestToolsListDoesNotRediscoverEmptyBackendTools(t *testing.T) {
	dir := t.TempDir()
	startCountPath := filepath.Join(dir, "starts")
	listCountPath := filepath.Join(dir, "lists")
	cfg := &config.Config{Servers: map[string]config.Server{
		"empty": helperServerWithEnv(startCountPath, "LAZYMCP_EMPTY_TOOLS=1", "LAZYMCP_LIST_COUNT_PATH="+listCountPath),
	}}
	if err := cfg.SetServerTools("empty", nil); err != nil {
		t.Fatalf("initialize config routes: %v", err)
	}

	var out bytes.Buffer
	var stderr bytes.Buffer
	srv := New(cfg, bytes.NewReader(nil), &out, &stderr)
	if err := srv.handle(context.Background(), mcp.Message{JSONRPC: "2.0", ID: "1", Method: "tools/list"}); err != nil {
		t.Fatalf("first tools/list: %v", err)
	}
	if err := srv.handle(context.Background(), mcp.Message{JSONRPC: "2.0", ID: "2", Method: "tools/list"}); err != nil {
		t.Fatalf("second tools/list: %v", err)
	}

	if got := readCount(t, listCountPath); got != 1 {
		t.Fatalf("backend tools/list calls = %d, want 1", got)
	}
	srv.backends.Shutdown()
}

func TestToolsListUsesBackendRequestTimeoutDuringDiscovery(t *testing.T) {
	startCountPath := filepath.Join(t.TempDir(), "starts")
	slow := helperServerWithEnv(startCountPath, "LAZYMCP_SLEEP_ON_LIST=1")
	slow.RequestTimeout = config.Duration(20 * time.Millisecond)
	cfg := &config.Config{Servers: map[string]config.Server{"slow": slow}}
	if err := cfg.SetServerTools("slow", nil); err != nil {
		t.Fatalf("initialize config routes: %v", err)
	}

	var out bytes.Buffer
	var stderr bytes.Buffer
	srv := New(cfg, bytes.NewReader(nil), &out, &stderr)
	start := time.Now()
	if err := srv.handle(context.Background(), mcp.Message{JSONRPC: "2.0", ID: "1", Method: "tools/list"}); err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("tools/list discovery took %s, want under 1s", elapsed)
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
			var params struct {
				Cursor *string `json:"cursor"`
			}
			_ = json.Unmarshal(msg.Params, &params)
			if path := os.Getenv("LAZYMCP_LIST_COUNT_PATH"); path != "" {
				incrementCount(path)
			}
			if os.Getenv("LAZYMCP_SLEEP_ON_LIST") == "1" {
				time.Sleep(time.Minute)
			}
			if os.Getenv("LAZYMCP_EMPTY_TOOLS") == "1" {
				_ = codec.Write(mcp.NewResult(msg.ID, map[string]any{"tools": []map[string]any{}}))
				continue
			}
			if os.Getenv("LAZYMCP_PAGINATED_TOOLS") == "1" && params.Cursor == nil {
				_ = codec.Write(mcp.NewResult(msg.ID, map[string]any{
					"tools": []map[string]any{
						{
							"name":        "first",
							"description": "First page tool.",
							"inputSchema": map[string]any{"type": "object"},
						},
					},
					"nextCursor": "page-2",
				}))
				continue
			}
			if os.Getenv("LAZYMCP_PAGINATED_TOOLS") == "1" && params.Cursor != nil && *params.Cursor == "page-2" {
				_ = codec.Write(mcp.NewResult(msg.ID, map[string]any{"tools": []map[string]any{
					{
						"name":        "second",
						"description": "Second page tool.",
						"inputSchema": map[string]any{"type": "object"},
					},
				}}))
				continue
			}
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
	return helperServerWithEnv(startCountPath)
}

func helperServerWithEnv(startCountPath string, extraEnv ...string) config.Server {
	args := []string{
		"GO_WANT_SERVER_HELPER_PROCESS=1",
		"LAZYMCP_START_COUNT_PATH=" + startCountPath,
	}
	args = append(args, extraEnv...)
	args = append(args, os.Args[0], "-test.run=TestHelperProcess", "--")
	return config.Server{
		Command:        "env",
		Args:           args,
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
	incrementCount(path)
}

func incrementCount(path string) {
	count := 0
	if data, err := os.ReadFile(path); err == nil {
		count, _ = strconv.Atoi(string(data))
	}
	_ = os.WriteFile(path, []byte(strconv.Itoa(count+1)), 0o600)
}

func readStartCount(t *testing.T, path string) int {
	return readCount(t, path)
}

func readCount(t *testing.T, path string) int {
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
