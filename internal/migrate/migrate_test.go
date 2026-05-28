package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCodexDryRunMasksEnvSecrets(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "config.toml")
	target := filepath.Join(dir, "lazymcp.yaml")
	err := os.WriteFile(source, []byte(`
[mcp_servers.github]
command = "npx"
args = ["-y", "@modelcontextprotocol/server-github"]

[mcp_servers.github.env]
GITHUB_TOKEN = "secret-token"
DEBUG = "1"
`), 0o600)
	if err != nil {
		t.Fatalf("write source: %v", err)
	}

	plan, err := Run(Options{
		Source:     SourceCodex,
		ConfigPath: target,
		SourcePath: source,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	report := FormatPlan(plan)
	if strings.Contains(report, "secret-token") {
		t.Fatalf("report leaked secret: %s", report)
	}
	if !strings.Contains(report, "GITHUB_TOKEN: <redacted>") {
		t.Fatalf("report did not redact token: %s", report)
	}
	if !strings.Contains(report, "DEBUG: <set>") {
		t.Fatalf("report did not mask non-secret env value: %s", report)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote target config")
	}
}

func TestRunWriteCreatesBackupAndMergesConfig(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "config.toml")
	target := filepath.Join(dir, "lazymcp.yaml")
	err := os.WriteFile(source, []byte(`
[mcp_servers.github]
command = "npx"
args = ["-y", "github"]
`), 0o600)
	if err != nil {
		t.Fatalf("write source: %v", err)
	}
	err = os.WriteFile(target, []byte(`
servers:
  filesystem:
    command: npx
    namespace: filesystem
`), 0o600)
	if err != nil {
		t.Fatalf("write target: %v", err)
	}

	plan, err := Run(Options{
		Source:     SourceCodex,
		ConfigPath: target,
		SourcePath: source,
		Write:      true,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(plan.Backups) != 1 {
		t.Fatalf("backups = %#v", plan.Backups)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "github:") || !strings.Contains(got, "filesystem:") {
		t.Fatalf("merged config missing servers:\n%s", got)
	}
}

func TestRunRejectsExistingServerName(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "config.toml")
	target := filepath.Join(dir, "lazymcp.yaml")
	err := os.WriteFile(source, []byte(`
[mcp_servers.github]
command = "npx"
`), 0o600)
	if err != nil {
		t.Fatalf("write source: %v", err)
	}
	err = os.WriteFile(target, []byte(`
servers:
  github:
    command: existing
`), 0o600)
	if err != nil {
		t.Fatalf("write target: %v", err)
	}

	plan, err := Run(Options{
		Source:     SourceCodex,
		ConfigPath: target,
		SourcePath: source,
		Write:      true,
	})
	if err == nil {
		t.Fatalf("expected conflict")
	}
	if plan == nil || len(plan.Conflicts) != 1 {
		t.Fatalf("conflicts = %#v", plan)
	}
}

func TestRunClaudeFindsProjectConfig(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	source := filepath.Join(project, ".mcp.json")
	err := os.WriteFile(source, []byte(`{
  "mcpServers": {
    "github": {
      "command": "npx",
      "args": ["-y", "github"]
    }
  }
}`), 0o600)
	if err != nil {
		t.Fatalf("write source: %v", err)
	}

	plan, err := Run(Options{
		Source:      SourceClaude,
		ConfigPath:  filepath.Join(dir, "lazymcp.yaml"),
		SourcePath:  source,
		ProjectPath: project,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, ok := plan.Servers["github"]; !ok {
		t.Fatalf("github server was not imported")
	}
}

func TestRunClaudeFindsProjectEntryInClaudeJSON(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	source := filepath.Join(dir, ".claude.json")
	err := os.WriteFile(source, []byte(`{
  "projects": {
    "`+jsonPath(project)+`": {
      "mcpServers": {
        "github": {
          "command": "npx",
          "args": ["-y", "github"]
        }
      }
    }
  }
}`), 0o600)
	if err != nil {
		t.Fatalf("write source: %v", err)
	}

	plan, err := Run(Options{
		Source:      SourceClaude,
		ConfigPath:  filepath.Join(dir, "lazymcp.yaml"),
		SourcePath:  source,
		ProjectPath: project,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, ok := plan.Servers["github"]; !ok {
		t.Fatalf("github server was not imported")
	}
}

func jsonPath(path string) string {
	return strings.ReplaceAll(path, `\`, `\\`)
}
