package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitCreatesEmptyConfig(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nested", "config.yaml")
	cmd := newRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--config", target, "init"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(data) != "servers: {}\n" {
		t.Fatalf("config = %q", string(data))
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("permissions = %o, want 600", got)
	}
	if !strings.Contains(out.String(), "created config: "+target) {
		t.Fatalf("output = %q", out.String())
	}
}

func TestInitRejectsExistingConfig(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(target, []byte("servers: {}\n"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	cmd := newRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--config", target, "init"})

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "config already exists") {
		t.Fatalf("error = %v", err)
	}
}

func TestOpenLogFileCreatesHomeTmpLazyMCPLog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	file, err := openLogFile()
	if err != nil {
		t.Fatalf("open log file: %v", err)
	}
	if _, err := file.WriteString("hello\n"); err != nil {
		t.Fatalf("write log: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close log: %v", err)
	}

	path := filepath.Join(home, "tmp", "lazymcp", "lazymcp.log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if string(data) != "hello\n" {
		t.Fatalf("log = %q", string(data))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat log: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("permissions = %o, want 600", got)
	}
}

func TestOpenLogFileRestrictsExistingLogPermissions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, "tmp", "lazymcp", "lazymcp.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	file, err := openLogFile()
	if err != nil {
		t.Fatalf("open log file: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close log: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat log: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("permissions = %o, want 600", got)
	}
}

func TestServeLogsConfigLoadFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	target := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(target, []byte("servers: {}\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cmd := newRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--config", target, "serve"})

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "config must define at least one server") {
		t.Fatalf("error = %v", err)
	}
	logPath := filepath.Join(home, "tmp", "lazymcp", "lazymcp.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), "lazymcp serve failed to load config") {
		t.Fatalf("log missing load failure:\n%s", string(data))
	}
}

func TestMigrateCommandHidesSourceSelection(t *testing.T) {
	cmd := newRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"migrate", "--help"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute help: %v", err)
	}
	help := out.String()
	if strings.Contains(help, "codex") || strings.Contains(help, "claude") {
		t.Fatalf("help still mentions source selection:\n%s", help)
	}
	if !strings.Contains(help, "Migrate Codex MCP settings into lazymcp config") {
		t.Fatalf("help does not describe Codex migration:\n%s", help)
	}
	if !strings.Contains(help, "write lazymcp config and update Codex MCP settings") {
		t.Fatalf("help does not describe write registration behavior:\n%s", help)
	}
	if strings.Contains(help, "register-client") {
		t.Fatalf("help still mentions hidden register-client flag:\n%s", help)
	}
}

func TestStatusShowsCodexCLIAppAndLazyMCPSettings(t *testing.T) {
	dir := t.TempDir()
	codexConfig := filepath.Join(dir, "config.toml")
	lazyConfig := filepath.Join(dir, "lazymcp.yaml")
	err := os.WriteFile(codexConfig, []byte(`
[mcp_servers.github]
command = "npx"
args = ["-y", "github", "--api-key=secret"]

[mcp_servers.remote]
type = "http"
url = "https://ghp_secret@example.com/mcp?api_key=secret&debug=1"

[mcp_servers.malformed]
type = "http"
url = "http://example.com/%zz?token=secret"
`), 0o600)
	if err != nil {
		t.Fatalf("write codex config: %v", err)
	}
	pluginDir := filepath.Join(dir, ".tmp", "plugins", "plugins", "build-ios-apps")
	if err := os.MkdirAll(pluginDir, 0o700); err != nil {
		t.Fatalf("mkdir plugin dir: %v", err)
	}
	err = os.WriteFile(filepath.Join(pluginDir, ".mcp.json"), []byte(`
{
  "mcpServers": {
    "xcodebuildmcp": {
      "command": "npx",
      "args": ["-y", "xcodebuildmcp@latest", "mcp"]
    },
    "cloudflare-api": {
      "type": "http",
      "url": "https://mcp.cloudflare.com/mcp?token=secret"
    }
  }
}
`), 0o600)
	if err != nil {
		t.Fatalf("write plugin manifest: %v", err)
	}
	cacheDir := filepath.Join(dir, "cache", "codex_apps_tools")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatalf("mkdir app cache dir: %v", err)
	}
	err = os.WriteFile(filepath.Join(cacheDir, "tools.json"), []byte(`
{
  "tools": [
    {
      "server_name": "codex_apps",
      "connector_name": "GitHub",
      "tool_name": "search"
    },
    {
      "server_name": "codex_apps",
      "connector_name": "GitHub",
      "tool_name": "search"
    },
    {
      "server_name": "codex_apps",
      "connector_name": "Asana",
      "tool_name": "list_tasks"
    }
  ]
}
`), 0o600)
	if err != nil {
		t.Fatalf("write app cache: %v", err)
	}
	err = os.WriteFile(lazyConfig, []byte(`
servers:
  filesystem:
    command: npx
    args:
      - -y
      - filesystem
    namespace: fs
`), 0o600)
	if err != nil {
		t.Fatalf("write lazy config: %v", err)
	}

	cmd := newRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"--config", lazyConfig,
		"status", "--codex-config", codexConfig,
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"Codex CLI (" + codexConfig + ")",
		"github",
		"importable",
		"npx -y github --api-key=<redacted>",
		"remote",
		"https://%3Credacted%3E@example.com/mcp?api_key=%3Credacted%3E&debug=1",
		"<redacted-url>",
		"Codex App (" + dir + ")",
		"xcodebuildmcp",
		"cloudflare-api",
		"https://mcp.cloudflare.com/mcp?token=%3Credacted%3E",
		"GitHub",
		"1 cached tools; no local stdio MCP command",
		"Asana",
		"lazymcp (" + lazyConfig + ")",
		"filesystem",
		"namespace=fs command=npx -y filesystem",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("status output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "secret") {
		t.Fatalf("status output leaked secret:\n%s", got)
	}
}

func TestStatusReportsMissingFilesWithoutFailing(t *testing.T) {
	dir := t.TempDir()
	cmd := newRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"--config", filepath.Join(dir, "missing-lazymcp.yaml"),
		"status", "--codex-config", filepath.Join(dir, "missing-codex.toml"),
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"Codex CLI",
		"warnings:",
		"missing-codex.toml: not found",
		"Codex App",
		"lazymcp",
		"missing-lazymcp.yaml: not found",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("status output missing %q:\n%s", want, got)
		}
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

func TestMigrateWriteCreatesConfigAndRegistersProxy(t *testing.T) {
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
		"migrate", "--source-path", source, "--write",
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
	codexConfig, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	got := string(codexConfig)
	if strings.Contains(got, "[mcp_servers.github]") {
		t.Fatalf("source config kept direct server:\n%s", got)
	}
	if !strings.Contains(got, "[mcp_servers.lazymcp]") {
		t.Fatalf("source config missing lazymcp:\n%s", got)
	}
}

func TestMigrateYesDefaultsToCodexAndRegistersProxy(t *testing.T) {
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
		"migrate", "--source-path", source, "-y",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	codexConfig, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	got := string(codexConfig)
	if strings.Contains(got, "[mcp_servers.github]") {
		t.Fatalf("source config kept direct server:\n%s", got)
	}
	if !strings.Contains(got, "[mcp_servers.lazymcp]") {
		t.Fatalf("source config missing lazymcp:\n%s", got)
	}
	if !strings.Contains(out.String(), "backups created:") {
		t.Fatalf("output did not report backup:\n%s", out.String())
	}
}

func TestMigrateCodexWriteRegistersProxyWithoutPrompt(t *testing.T) {
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

	cmd := newRootCommand()
	var out bytes.Buffer
	cmd.SetIn(strings.NewReader("no\n"))
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"--config", target,
		"migrate", "--source-path", source, "--write", "codex",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if strings.Contains(out.String(), "Register lazymcp as the only MCP server") {
		t.Fatalf("output unexpectedly prompted:\n%s", out.String())
	}
	codexConfig, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	if !strings.Contains(string(codexConfig), "[mcp_servers.lazymcp]") {
		t.Fatalf("source config missing lazymcp:\n%s", string(codexConfig))
	}
}

func TestMigrateRejectsUnexpectedArgs(t *testing.T) {
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

func TestMigrateWithoutSubcommandDefaultsToCodex(t *testing.T) {
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

	cmd := newRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"--config", target,
		"migrate", "--source-path", source, "--dry-run",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out.String(), "source: codex") {
		t.Fatalf("output did not default to codex:\n%s", out.String())
	}
}
