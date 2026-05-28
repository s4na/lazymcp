package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestRunWriteCanReplaceCodexMCPServersWithLazyProxy(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "config.toml")
	target := filepath.Join(dir, "nested", "lazymcp.yaml")
	err := os.WriteFile(source, []byte(`
[projects."/tmp/repo"]
trust_level = "trusted"

[mcp_servers.github]
command = "npx"
args = ["-y", "github"]

[mcp_servers.filesystem]
command = "npx"
args = ["-y", "filesystem", "/tmp"]
`), 0o600)
	if err != nil {
		t.Fatalf("write source: %v", err)
	}

	plan, err := Run(Options{
		Source:       SourceCodex,
		ConfigPath:   target,
		SourcePath:   source,
		Write:        true,
		UpdateClient: true,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(plan.ChangedFiles) != 2 {
		t.Fatalf("changed files = %#v", plan.ChangedFiles)
	}
	if len(plan.Backups) != 1 || !strings.HasPrefix(plan.Backups[0], source+".bak.") {
		t.Fatalf("backups = %#v", plan.Backups)
	}

	lazyConfig, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if !strings.Contains(string(lazyConfig), "github:") || !strings.Contains(string(lazyConfig), "filesystem:") {
		t.Fatalf("lazymcp config missing imported servers:\n%s", string(lazyConfig))
	}

	codexConfig, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	got := string(codexConfig)
	if strings.Contains(got, "[mcp_servers.github]") || strings.Contains(got, "[mcp_servers.filesystem]") {
		t.Fatalf("codex config kept direct MCP servers:\n%s", got)
	}
	if !strings.Contains(got, "[mcp_servers.lazymcp]") {
		t.Fatalf("codex config missing lazymcp proxy:\n%s", got)
	}
	if !strings.Contains(got, `args = ['serve', '--config',`) && !strings.Contains(got, `args = ["serve", "--config",`) {
		t.Fatalf("codex config missing serve args:\n%s", got)
	}
	if !strings.Contains(got, `[projects.'/tmp/repo']`) && !strings.Contains(got, `[projects."/tmp/repo"]`) {
		t.Fatalf("codex config did not preserve non-MCP settings:\n%s", got)
	}
}

func TestRunUpdateClientRequiresWrite(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "config.toml")
	err := os.WriteFile(source, []byte(`
[mcp_servers.github]
command = "npx"
`), 0o600)
	if err != nil {
		t.Fatalf("write source: %v", err)
	}

	_, err = Run(Options{
		Source:       SourceCodex,
		ConfigPath:   filepath.Join(dir, "lazymcp.yaml"),
		SourcePath:   source,
		UpdateClient: true,
	})
	if err == nil || !strings.Contains(err.Error(), "requires --write") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunWriteCreatesNewConfigWithParentDir(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "config.toml")
	target := filepath.Join(dir, "nested", "lazymcp.yaml")
	err := os.WriteFile(source, []byte(`
[mcp_servers.github]
command = "npx"
args = ["-y", "github"]

[mcp_servers.github.env]
GITHUB_TOKEN = "secret-token"
`), 0o600)
	if err != nil {
		t.Fatalf("write source: %v", err)
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
	if len(plan.Backups) != 0 {
		t.Fatalf("backups = %#v, want none", plan.Backups)
	}
	if len(plan.ChangedFiles) != 1 || plan.ChangedFiles[0] != target {
		t.Fatalf("changed files = %#v", plan.ChangedFiles)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("target permissions = %o, want 600", got)
	}
	cfg, err := readLazyConfig(target)
	if err != nil {
		t.Fatalf("read written config: %v", err)
	}
	srv := cfg.Servers["github"]
	if srv.Command != "npx" || len(srv.Args) != 2 || srv.Env["GITHUB_TOKEN"] != "secret-token" {
		t.Fatalf("written server = %#v", srv)
	}
}

func TestRunWriteRestrictsExistingConfigPermissions(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "config.toml")
	target := filepath.Join(dir, "lazymcp.yaml")
	err := os.WriteFile(source, []byte(`
[mcp_servers.github]
command = "npx"

[mcp_servers.github.env]
GITHUB_TOKEN = "secret-token"
`), 0o600)
	if err != nil {
		t.Fatalf("write source: %v", err)
	}
	err = os.WriteFile(target, []byte(`
servers:
  filesystem:
    command: npx
`), 0o644)
	if err != nil {
		t.Fatalf("write target: %v", err)
	}

	_, err = Run(Options{
		Source:     SourceCodex,
		ConfigPath: target,
		SourcePath: source,
		Write:      true,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("target permissions = %o, want 600", got)
	}
}

func TestRunOverwriteReplacesMatchingServer(t *testing.T) {
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
  github:
    command: old
    namespace: github
`), 0o600)
	if err != nil {
		t.Fatalf("write target: %v", err)
	}

	_, err = Run(Options{
		Source:     SourceCodex,
		ConfigPath: target,
		SourcePath: source,
		Write:      true,
		Overwrite:  true,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	cfg, err := readLazyConfig(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	srv := cfg.Servers["github"]
	if srv.Command != "npx" || len(srv.Args) != 2 {
		t.Fatalf("server was not overwritten: %#v", srv)
	}
}

func TestRunWriteCreatesDistinctBackups(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "config.toml")
	target := filepath.Join(dir, "lazymcp.yaml")
	originalNowUTC := nowUTC
	nowUTC = func() time.Time {
		return time.Date(2026, 5, 28, 12, 34, 56, 123, time.UTC)
	}
	defer func() {
		nowUTC = originalNowUTC
	}()
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
	if !strings.HasSuffix(second.Backups[0], ".1") {
		t.Fatalf("second backup path = %s, want collision suffix", second.Backups[0])
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

func TestRunRejectsNamespaceConflictEvenWithOverwrite(t *testing.T) {
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
  filesystem:
    command: npx
    namespace: github
`), 0o600)
	if err != nil {
		t.Fatalf("write target: %v", err)
	}

	plan, err := Run(Options{
		Source:     SourceCodex,
		ConfigPath: target,
		SourcePath: source,
		Write:      true,
		Overwrite:  true,
	})
	if err == nil {
		t.Fatalf("expected namespace conflict")
	}
	if plan == nil || len(plan.Conflicts) != 1 || !strings.Contains(plan.Conflicts[0], "namespace") {
		t.Fatalf("conflicts = %#v", plan)
	}
}
