package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateCommandOnlySupportsCodex(t *testing.T) {
	cmd := newRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"migrate", "--help"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute help: %v", err)
	}
	help := out.String()
	if !strings.Contains(help, "codex") {
		t.Fatalf("help does not include codex subcommand:\n%s", help)
	}
	if strings.Contains(help, "claude") {
		t.Fatalf("help still mentions claude:\n%s", help)
	}
}

func TestMigrateCodexRejectsDryRunWithWrite(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "config.toml")
	err := os.WriteFile(source, []byte(`
[mcp_servers.github]
command = "npx"
`), 0o600)
	if err != nil {
		t.Fatalf("write source: %v", err)
	}

	cmd := newRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"--config", filepath.Join(dir, "lazymcp.yaml"),
		"migrate", "--source-path", source, "--dry-run", "--write", "codex",
	})

	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--dry-run and --write cannot be used together") {
		t.Fatalf("error = %v", err)
	}
}

func TestMigrateCodexWriteCreatesConfig(t *testing.T) {
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

	cmd := newRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"--config", target,
		"migrate", "--source-path", source, "--write", "codex",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out.String(), "files changed:") {
		t.Fatalf("output did not report changed files:\n%s", out.String())
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if !strings.Contains(string(data), "github:") {
		t.Fatalf("target config missing github server:\n%s", string(data))
	}
}

func TestMigrateCodexRejectsUnexpectedArgs(t *testing.T) {
	cmd := newRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"migrate", "codex", "/tmp/config.toml"})

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("error = %v", err)
	}
}

func TestMigrateWithoutSubcommandReturnsError(t *testing.T) {
	cmd := newRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"migrate", "--write"})

	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected migrate without subcommand to fail")
	}
	if !strings.Contains(out.String(), "Migrate existing client MCP settings") {
		t.Fatalf("help was not printed:\n%s", out.String())
	}
}
