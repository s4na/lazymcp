package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadAppliesDefaultsAndPrefixesTools(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	err := os.WriteFile(path, []byte(`
servers:
  github:
    command: npx
    args: ["-y", "server"]
    namespace: gh
    tools:
      - name: search
        input_schema:
          type: object
`), 0o600)
	if err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := time.Duration(cfg.Servers["github"].IdleTimeout); got != 5*time.Minute {
		t.Fatalf("idle timeout = %s", got)
	}
	if got := time.Duration(cfg.Servers["github"].RequestTimeout); got != 10*time.Minute {
		t.Fatalf("request timeout = %s", got)
	}
	tools := cfg.Tools()
	if len(tools) != 1 || tools[0].Name != "gh.search" {
		t.Fatalf("unexpected tools: %#v", tools)
	}
}

func TestLoadRejectsDuplicateNamespace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	err := os.WriteFile(path, []byte(`
servers:
  one:
    command: first
    namespace: shared
  two:
    command: second
    namespace: shared
`), 0o600)
	if err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatalf("expected duplicate namespace error")
	}
}

func TestLoadRejectsDuplicateExposedToolName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	err := os.WriteFile(path, []byte(`
servers:
  github:
    command: npx
    tools:
      - name: search
      - name: github.search
`), 0o600)
	if err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatalf("expected duplicate exposed tool name error")
	}
}

func TestServerForToolStripsNamespace(t *testing.T) {
	cfg := &Config{Servers: map[string]Server{
		"github": {Command: "npx", Namespace: "github", Tools: []Tool{{Name: "search_repositories"}}},
	}}
	cfg.routes = map[string]Route{
		"github.search_repositories": {ServerName: "github", Server: cfg.Servers["github"], BackendTool: "search_repositories"},
	}
	name, _, toolName, ok := cfg.ServerForTool("github.search_repositories")
	if !ok {
		t.Fatalf("expected route")
	}
	if name != "github" || toolName != "search_repositories" {
		t.Fatalf("route = %q %q", name, toolName)
	}
}

func TestServerForToolRejectsUnlistedTool(t *testing.T) {
	cfg := &Config{Servers: map[string]Server{
		"github": {Command: "npx", Namespace: "github", Tools: []Tool{{Name: "search"}}},
	}}
	cfg.routes = map[string]Route{
		"github.search": {ServerName: "github", Server: cfg.Servers["github"], BackendTool: "search"},
	}
	if _, _, _, ok := cfg.ServerForTool("github.delete_repo"); ok {
		t.Fatalf("expected unlisted tool to be rejected")
	}
}
