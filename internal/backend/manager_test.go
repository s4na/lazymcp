package backend

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/s4na/lazymcp/internal/config"
	"github.com/s4na/lazymcp/internal/mcp"
)

func TestIDsEqualSupportsStringIDs(t *testing.T) {
	if !idsEqual("42", "42") {
		t.Fatalf("expected string ID to match")
	}
	if idsEqual("43", "42") {
		t.Fatalf("expected different string ID not to match")
	}
}

func TestManagerTracksIdleStoppedAndRespawns(t *testing.T) {
	startCountPath := t.TempDir() + "/starts"
	srv := helperServer(startCountPath)
	srv.IdleTimeout = config.Duration(20 * time.Millisecond)

	manager := NewManager(os.Stderr)
	ctx := context.Background()
	if _, callErr := manager.Call(ctx, "test", srv, "ping", json.RawMessage(`{}`), nil); callErr != nil {
		t.Fatalf("first call: %v", callErr)
	}

	waitForState(t, manager, "test", StatusIdleStopped)
	if _, callErr := manager.Call(ctx, "test", srv, "ping", json.RawMessage(`{}`), nil); callErr != nil {
		t.Fatalf("second call: %v", callErr)
	}

	if got := readStartCount(t, startCountPath); got != 2 {
		t.Fatalf("backend starts = %d, want 2", got)
	}
	state := manager.States([]string{"test"})["test"]
	if state.Status != StatusRunning {
		t.Fatalf("state = %s, want %s", state.Status, StatusRunning)
	}
	manager.Shutdown()
}

func TestManagerMarksShutdownReason(t *testing.T) {
	startCountPath := t.TempDir() + "/starts"
	manager := NewManager(os.Stderr)
	if _, callErr := manager.Call(context.Background(), "test", helperServer(startCountPath), "ping", json.RawMessage(`{}`), nil); callErr != nil {
		t.Fatalf("call: %v", callErr)
	}

	manager.Shutdown()

	state := manager.States([]string{"test"})["test"]
	if state.Status != StatusStopped {
		t.Fatalf("state = %s, want %s", state.Status, StatusStopped)
	}
	if state.StopReason != StopReasonShutdown {
		t.Fatalf("stop reason = %s, want %s", state.StopReason, StopReasonShutdown)
	}
	if state.LastStarted.IsZero() || state.LastStopped.IsZero() {
		t.Fatalf("expected lifecycle timestamps: %#v", state)
	}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
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
		case "tools/call":
			_ = codec.Write(mcp.NewResult(msg.ID, map[string]any{"content": []map[string]any{
				{"type": "text", "text": "ok"},
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
			"GO_WANT_HELPER_PROCESS=1",
			"LAZYMCP_START_COUNT_PATH=" + startCountPath,
			os.Args[0],
			"-test.run=TestHelperProcess",
			"--",
		},
		IdleTimeout:    config.Duration(time.Minute),
		RequestTimeout: config.Duration(time.Second),
	}
}

func waitForState(t *testing.T, manager *Manager, name string, want Status) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state := manager.States([]string{name})[name]
		if state.Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", want)
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
