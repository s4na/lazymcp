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

func TestEnvListSortsAndFormatsValues(t *testing.T) {
	got := envList(map[string]string{
		"Z_TOKEN": "z",
		"A_TOKEN": "a",
	})
	want := []string{"A_TOKEN=a", "Z_TOKEN=z"}
	if len(got) != len(want) {
		t.Fatalf("env list length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("env list[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestManagerPassesConfiguredEnvToBackend(t *testing.T) {
	startCountPath := t.TempDir() + "/starts"
	srv := helperServer(startCountPath)
	srv.Env = map[string]string{"LAZYMCP_CONFIG_ENV": "from-config"}

	manager := NewManager(os.Stderr)
	result, callErr := manager.Call(context.Background(), "test", srv, "ping", json.RawMessage(`{}`), nil)
	if callErr != nil {
		t.Fatalf("call: %v", callErr)
	}

	text := helperResultText(t, result)
	if text != "from-config" {
		t.Fatalf("backend env = %q, want %q", text, "from-config")
	}
	manager.Shutdown()
}

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

func TestManagerMarksUnrequestedExitAsCrashed(t *testing.T) {
	startCountPath := t.TempDir() + "/starts"
	manager := NewManager(os.Stderr)
	srv := helperServerWithEnv(startCountPath, "LAZYMCP_EXIT_AFTER_INITIALIZED=1")
	if _, err := manager.get(context.Background(), "test", srv, nil); err != nil {
		t.Fatalf("get: %v", err)
	}

	waitForState(t, manager, "test", StatusCrashed)

	state := manager.States([]string{"test"})["test"]
	if state.StopReason != StopReasonCrashed {
		t.Fatalf("stop reason = %s, want %s", state.StopReason, StopReasonCrashed)
	}
	if state.LastError == "" {
		t.Fatalf("expected last error for unexpected exit")
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
			if os.Getenv("LAZYMCP_EXIT_AFTER_INITIALIZED") == "1" {
				os.Exit(0)
			}
		case "tools/call":
			text := os.Getenv("LAZYMCP_CONFIG_ENV")
			if text == "" {
				text = "ok"
			}
			_ = codec.Write(mcp.NewResult(msg.ID, map[string]any{"content": []map[string]any{
				{"type": "text", "text": text},
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
		"GO_WANT_HELPER_PROCESS=1",
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

func waitForState(t *testing.T, manager *Manager, name string, want Status) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
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

func helperResultText(t *testing.T, result any) string {
	t.Helper()
	resultMap, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v, want map", result)
	}
	content, ok := resultMap["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("content = %#v, want non-empty slice", resultMap["content"])
	}
	first, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("content[0] = %#v, want map", content[0])
	}
	text, ok := first["text"].(string)
	if !ok {
		t.Fatalf("content[0].text = %#v, want string", first["text"])
	}
	return text
}
