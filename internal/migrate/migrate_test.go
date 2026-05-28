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

func TestRunCodexDryRunMasksSecretAssignmentsInArgs(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "config.toml")
	err := os.WriteFile(source, []byte(`
[mcp_servers.github]
command = "npx"
args = ["-y", "github", "--api-key=sk-secret", "--api_key=sk-secret-two"]
`), 0o600)
	if err != nil {
		t.Fatalf("write source: %v", err)
	}

	plan, err := Run(Options{
		Source:     SourceCodex,
		ConfigPath: filepath.Join(dir, "lazymcp.yaml"),
		SourcePath: source,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	report := FormatPlan(plan)
	if strings.Contains(report, "sk-secret") {
		t.Fatalf("report leaked secret arg: %s", report)
	}
	if !strings.Contains(report, "--api-key=<redacted>") || !strings.Contains(report, "--api_key=<redacted>") {
		t.Fatalf("report did not redact api key args: %s", report)
	}
}

func TestRunCodexRejectsMissingMCPServersTable(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "config.toml")
	err := os.WriteFile(source, []byte(`
[projects."/tmp/repo"]
trust_level = "trusted"
`), 0o600)
	if err != nil {
		t.Fatalf("write source: %v", err)
	}

	_, err = Run(Options{
		Source:     SourceCodex,
		ConfigPath: filepath.Join(dir, "lazymcp.yaml"),
		SourcePath: source,
	})
	if err == nil || !strings.Contains(err.Error(), "missing [mcp_servers] table") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunCodexRejectsInvalidServerShape(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "missing command",
			body: `
[mcp_servers.github]
args = ["-y", "github"]
`,
			want: "command is required",
		},
		{
			name: "non string command",
			body: `
[mcp_servers.github]
command = ["npx"]
`,
			want: "command must be a non-empty string",
		},
		{
			name: "args is not string array",
			body: `
[mcp_servers.github]
command = "npx"
args = ["-y", 1]
`,
			want: "args must be an array of strings",
		},
		{
			name: "env value is not string",
			body: `
[mcp_servers.github]
command = "npx"

[mcp_servers.github.env]
GITHUB_TOKEN = 1
`,
			want: "env must be a table of string values",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "config.toml")
			err := os.WriteFile(source, []byte(tt.body), 0o600)
			if err != nil {
				t.Fatalf("write source: %v", err)
			}

			_, err = Run(Options{
				Source:     SourceCodex,
				ConfigPath: filepath.Join(dir, "lazymcp.yaml"),
				SourcePath: source,
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestRunCodexSkipsExistingLazyMCPProxy(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "config.toml")
	err := os.WriteFile(source, []byte(`
[mcp_servers.lazymcp]
command = "lazymcp"
args = ["serve", "--config", "/tmp/lazymcp.yaml"]
`), 0o600)
	if err != nil {
		t.Fatalf("write source: %v", err)
	}

	_, err = Run(Options{
		Source:     SourceCodex,
		ConfigPath: filepath.Join(dir, "lazymcp.yaml"),
		SourcePath: source,
	})
	if err == nil || !strings.Contains(err.Error(), "no Codex MCP servers to import") {
		t.Fatalf("error = %v", err)
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

func TestRunWriteCreatesDistinctBackups(t *testing.T) {
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
    command: old
`), 0o600)
	if err != nil {
		t.Fatalf("write target: %v", err)
	}

	first, err := Run(Options{
		Source:     SourceCodex,
		ConfigPath: target,
		SourcePath: source,
		Write:      true,
		Overwrite:  true,
	})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	second, err := Run(Options{
		Source:     SourceCodex,
		ConfigPath: target,
		SourcePath: source,
		Write:      true,
		Overwrite:  true,
	})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if first.Backups[0] == second.Backups[0] {
		t.Fatalf("backup path reused: %s", first.Backups[0])
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
