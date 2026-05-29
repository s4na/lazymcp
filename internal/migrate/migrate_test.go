package migrate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/s4na/lazymcp/internal/config"
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

func TestRunCodexDiffShowsConfigChangesWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "config.toml")
	target := filepath.Join(dir, "lazymcp.yaml")
	sourceData := []byte(`
model = "gpt-5"

[mcp_servers.github]
command = "npx"
args = ["-y", "github"]
`)
	targetData := []byte(`
servers:
  filesystem:
    command: npx
    namespace: filesystem
`)
	if err := os.WriteFile(source, sourceData, 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := os.WriteFile(target, targetData, 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}

	plan, err := Run(Options{
		Source:       SourceCodex,
		ConfigPath:   target,
		SourcePath:   source,
		Diff:         true,
		UpdateClient: true,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	report := FormatPlan(plan)
	if len(plan.Diffs) != 2 {
		t.Fatalf("diffs = %#v", plan.Diffs)
	}
	if !strings.Contains(report, "diffs:\n--- "+target) {
		t.Fatalf("report missing target diff:\n%s", report)
	}
	if !strings.Contains(report, "+    github:") {
		t.Fatalf("report missing imported github server:\n%s", report)
	}
	if !strings.Contains(report, "--- "+source) {
		t.Fatalf("report missing source diff:\n%s", report)
	}
	if !strings.Contains(report, "-[mcp_servers.github]") || !strings.Contains(report, "+[mcp_servers.lazymcp]") {
		t.Fatalf("report missing Codex before/after MCP change:\n%s", report)
	}
	gotSource, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	if string(gotSource) != string(sourceData) {
		t.Fatalf("source changed:\n%s", string(gotSource))
	}
	gotTarget, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(gotTarget) != string(targetData) {
		t.Fatalf("target changed:\n%s", string(gotTarget))
	}
}

func TestRunCodexDiffMasksSecrets(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "config.toml")
	target := filepath.Join(dir, "lazymcp.yaml")
	sourceData := []byte(`
api_key = "codex-top-secret"
base_url = "https://codex-user:codex-pass@example.com/mcp?debug=1&token=codex-url-secret"

[mcp_servers.api-key-service]
command = "npx"
args = ["-y", "github", "--api-key=sk-secret", "--apiKey=sk-camel", "--key", "plain-key-secret", "--token", "token-secret", "https://arg-user:arg-pass@example.com/mcp?debug=1&api_key=arg-url-secret", "--endpoint=https://endpoint-user:endpoint-pass@example.com/mcp?password=endpoint-url-secret"]

[mcp_servers.api-key-service.env]
GITHUB_TOKEN = "secret-token"
DEBUG = "1"

[projects."/tmp/repo"]
token = "codex-project-secret"
`)
	targetData := []byte(`
api_key: lazy-top-secret
base_url: https://lazy-user:lazy-pass@example.com/mcp?debug=1&secret=lazy-url-secret
servers:
  existing:
    command: npx
    args:
      - --password=old-secret
    env:
      API_KEY: old-secret
      DEBUG: "1"
    namespace: existing
`)
	if err := os.WriteFile(source, sourceData, 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := os.WriteFile(target, targetData, 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}

	plan, err := Run(Options{
		Source:       SourceCodex,
		ConfigPath:   target,
		SourcePath:   source,
		Diff:         true,
		UpdateClient: true,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	report := FormatPlan(plan)
	for _, secret := range []string{"sk-secret", "sk-camel", "plain-key-secret", "token-secret", "secret-token", "old-secret", "codex-top-secret", "codex-project-secret", "lazy-top-secret", "codex-user", "codex-pass", "codex-url-secret", "arg-user", "arg-pass", "arg-url-secret", "endpoint-user", "endpoint-pass", "endpoint-url-secret", "lazy-user", "lazy-pass", "lazy-url-secret"} {
		if strings.Contains(report, secret) {
			t.Fatalf("diff leaked secret %q:\n%s", secret, report)
		}
	}
	for _, want := range []string{"api-key-service", "command: npx", "--api-key=<redacted>", "--apiKey=<redacted>", "--key <redacted>", "--token <redacted>", "GITHUB_TOKEN", "<redacted>", "DEBUG", "<set>", "API_KEY: <redacted>", "api_key: <redacted>", "token = '<redacted>'"} {
		if !strings.Contains(report, want) {
			t.Fatalf("diff missing redacted value %q:\n%s", want, report)
		}
	}
}

func TestRunCodexDiffOmitsUnchangedFiles(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "config.toml")
	target := filepath.Join(dir, "lazymcp.yaml")
	if err := os.WriteFile(source, []byte(`
[mcp_servers.github]
command = "npx"
args = ["-y", "github"]
`), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := os.WriteFile(target, []byte(`
servers:
  github:
    command: npx
    args:
      - -y
      - github
    namespace: github
    idle_timeout: 5m0s
    request_timeout: 10m0s
    tools: []
`), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}

	plan, err := Run(Options{
		Source:     SourceCodex,
		ConfigPath: target,
		SourcePath: source,
		Diff:       true,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(plan.Diffs) != 0 {
		t.Fatalf("diffs = %#v, want none", plan.Diffs)
	}
}

func TestRunCodexDiffRejectsBlockedClientUpdate(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "config.toml")
	target := filepath.Join(dir, "lazymcp.yaml")
	if err := os.WriteFile(source, []byte(`
[mcp_servers.local]
command = "npx"
args = ["-y", "local-mcp"]

[mcp_servers.remote]
type = "http"
url = "https://example.com/mcp"
`), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	plan, err := Run(Options{
		Source:       SourceCodex,
		ConfigPath:   target,
		SourcePath:   source,
		Diff:         true,
		UpdateClient: true,
	})
	if err == nil || !strings.Contains(err.Error(), "updating the source client would remove unsupported Codex MCP servers") {
		t.Fatalf("error = %v", err)
	}
	if len(plan.Diffs) != 0 {
		t.Fatalf("diffs = %#v, want none", plan.Diffs)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("diff created target config")
	}
	gotCodex, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	if strings.Contains(string(gotCodex), "[mcp_servers.lazymcp]") {
		t.Fatalf("codex source was rewritten despite blocked remote server:\n%s", string(gotCodex))
	}
}

func TestRunCodexExplainsMissingMCPServersTable(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "missing table",
			body: `
[projects."/tmp/repo"]
trust_level = "trusted"
`,
		},
		{
			name: "empty table",
			body: `
[mcp_servers]
`,
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
			if err == nil {
				t.Fatal("expected error")
			}
			for _, want := range []string{
				"no importable Codex MCP servers found",
				"imports Codex config.toml [mcp_servers.*] entries",
				"remote Codex App connectors/plugins may be managed separately",
				source,
			} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %v, want %q", err, want)
				}
			}
		})
	}
}

func TestRunCodexImportsPluginMCPManifestServers(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "config.toml")
	target := filepath.Join(dir, "lazymcp.yaml")
	err := os.WriteFile(source, []byte(`
[mcp_servers.local]
command = "npx"
args = ["-y", "local-mcp"]

[mcp_servers.remote]
type = "http"
url = "https://example.com/mcp"

[projects."/tmp/repo"]
trust_level = "trusted"
`), 0o600)
	if err != nil {
		t.Fatalf("write source: %v", err)
	}
	pluginDir := filepath.Join(dir, ".tmp", "plugins", "plugins", "build-ios-apps")
	if err := os.MkdirAll(pluginDir, 0o700); err != nil {
		t.Fatalf("mkdir plugin dir: %v", err)
	}
	manifest := filepath.Join(pluginDir, ".mcp.json")
	err = os.WriteFile(manifest, []byte(`
{
  "mcpServers": {
    "xcodebuildmcp": {
      "command": "npx",
      "args": ["-y", "xcodebuildmcp@latest", "mcp"],
      "env": {
        "XCODEBUILDMCP_ENABLED_WORKFLOWS": "simulator,ui-automation"
      }
    },
    "cloudflare-api": {
      "type": "http",
      "url": "https://mcp.cloudflare.com/mcp"
    }
  }
}
`), 0o600)
	if err != nil {
		t.Fatalf("write plugin manifest: %v", err)
	}
	cacheDir := filepath.Join(dir, "cache", "codex_apps_tools")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatalf("mkdir app tool cache dir: %v", err)
	}
	appCache := filepath.Join(cacheDir, "tools.json")
	err = os.WriteFile(appCache, []byte(`
{
  "schema_version": 1,
  "tools": [
    {
      "server_name": "codex_apps",
      "tool_name": "search",
      "tool": {
        "name": "github_search",
        "_meta": {
          "connector_id": "connector_github",
          "connector_name": "GitHub"
        }
      }
    },
    {
      "server_name": "codex_apps",
      "connector_id": "connector_github",
      "connector_name": "GitHub",
      "tool_name": "get_issue"
    },
    {
      "server_name": "codex_apps",
      "connector_id": "connector_github",
      "connector_name": "GitHub",
      "tool_name": "get_issue"
    },
    {
      "server_name": "codex_apps",
      "connector_id": "connector_asana",
      "connector_name": "Asana",
      "tool_name": "list_tasks"
    },
    {
      "server_name": "codex_apps",
      "connector_name": "Linear",
      "tool_name": "list_issues"
    },
    {
      "server_name": "codex_apps",
      "connector_name": "Notion",
      "tool_name": "search_pages"
    }
  ]
}
`), 0o600)
	if err != nil {
		t.Fatalf("write app tool cache: %v", err)
	}

	plan, err := Run(Options{
		Source:       SourceCodex,
		ConfigPath:   target,
		SourcePath:   source,
		Write:        true,
		UpdateClient: true,
	})
	if err == nil || !strings.Contains(err.Error(), "updating the source client would remove unsupported Codex MCP servers") {
		t.Fatalf("error = %v", err)
	}
	if _, ok := plan.Servers["xcodebuildmcp"]; !ok {
		t.Fatalf("servers = %#v, want xcodebuildmcp", plan.Servers)
	}
	if _, ok := plan.Servers["local"]; !ok {
		t.Fatalf("servers = %#v, want local Codex MCP", plan.Servers)
	}
	if got := plan.Servers["xcodebuildmcp"].Env["XCODEBUILDMCP_ENABLED_WORKFLOWS"]; got != "simulator,ui-automation" {
		t.Fatalf("env = %q", got)
	}
	if !containsString(plan.SourceFiles, source) || !containsString(plan.SourceFiles, manifest) || !containsString(plan.SourceFiles, appCache) {
		t.Fatalf("source files = %#v, want config, manifest, and app cache", plan.SourceFiles)
	}
	for _, want := range []string{
		"Asana: Codex App connector cache has 1 tools but no local stdio MCP command to import",
		"GitHub: Codex App connector cache has 2 tools but no local stdio MCP command to import",
		"Linear: Codex App connector cache has 1 tools but no local stdio MCP command to import",
		"Notion: Codex App connector cache has 1 tools but no local stdio MCP command to import",
		"cloudflare-api: plugin MCP manifest uses unsupported remote transport",
		"remote: Codex MCP server uses unsupported remote transport",
	} {
		if !containsString(plan.Skipped, want) {
			t.Fatalf("skipped = %#v, want %q", plan.Skipped, want)
		}
	}
	if !containsString(plan.Blocked, "remote: Codex MCP server uses unsupported remote transport") {
		t.Fatalf("blocked = %#v", plan.Blocked)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target config was created despite blocked source client update")
	}
	gotCodex, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	if strings.Contains(string(gotCodex), "[mcp_servers.lazymcp]") {
		t.Fatalf("codex source was rewritten despite blocked remote server:\n%s", string(gotCodex))
	}
}

func TestRunCodexPreservesBundledMCPServersWhenUpdatingClient(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "config.toml")
	target := filepath.Join(dir, "lazymcp.yaml")
	err := os.WriteFile(source, []byte(`
[mcp_servers.node_repl]
command = "/Applications/Codex.app/Contents/Resources/node_repl"
args = []
startup_timeout_sec = 120

[mcp_servers.node_repl.env]
CODEX_CLI_PATH = "/Applications/Codex.app/Contents/Resources/codex"
NODE_REPL_NODE_PATH = "/Applications/Codex.app/Contents/Resources/node"
`), 0o600)
	if err != nil {
		t.Fatalf("write source: %v", err)
	}
	pluginDir := filepath.Join(dir, ".tmp", "plugins", "plugins", "build-ios-apps")
	if err := os.MkdirAll(pluginDir, 0o700); err != nil {
		t.Fatalf("mkdir plugin dir: %v", err)
	}
	manifest := filepath.Join(pluginDir, ".mcp.json")
	err = os.WriteFile(manifest, []byte(`
{
  "mcpServers": {
    "xcodebuildmcp": {
      "command": "npx",
      "args": ["-y", "xcodebuildmcp@latest", "mcp"]
    }
  }
}
`), 0o600)
	if err != nil {
		t.Fatalf("write plugin manifest: %v", err)
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
	if _, ok := plan.Servers["xcodebuildmcp"]; !ok {
		t.Fatalf("servers = %#v, want xcodebuildmcp", plan.Servers)
	}
	if _, ok := plan.Servers["node_repl"]; ok {
		t.Fatalf("servers = %#v, want bundled node_repl skipped", plan.Servers)
	}
	if !containsString(plan.Skipped, "node_repl: Codex bundled MCP server is preserved in Codex config and not migrated by default") {
		t.Fatalf("skipped = %#v", plan.Skipped)
	}

	lazyData, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if strings.Contains(string(lazyData), "node_repl") {
		t.Fatalf("target imported bundled MCP server:\n%s", string(lazyData))
	}
	if !strings.Contains(string(lazyData), "xcodebuildmcp") {
		t.Fatalf("target missing plugin MCP server:\n%s", string(lazyData))
	}

	codexData, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	for _, want := range []string{
		"[mcp_servers.lazymcp]",
		"[mcp_servers.node_repl]",
		"startup_timeout_sec = 120",
		"NODE_REPL_NODE_PATH",
	} {
		if !strings.Contains(string(codexData), want) {
			t.Fatalf("source missing %q after rewrite:\n%s", want, string(codexData))
		}
	}

	plan, err = Run(Options{
		Source:       SourceCodex,
		ConfigPath:   target,
		SourcePath:   source,
		Write:        true,
		UpdateClient: true,
	})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(plan.ChangedFiles) != 0 {
		t.Fatalf("second run changed files = %#v, want none", plan.ChangedFiles)
	}
}

func TestRunCodexDoesNotTreatNodeReplNameAsBundledByItself(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "config.toml")
	err := os.WriteFile(source, []byte(`
[mcp_servers.node_repl]
command = "npx"
args = ["-y", "custom-node-repl"]
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
	if _, ok := plan.Servers["node_repl"]; !ok {
		t.Fatalf("servers = %#v, want custom node_repl imported", plan.Servers)
	}
	if containsSubstring(plan.Skipped, "Codex bundled MCP server") {
		t.Fatalf("skipped = %#v, want no bundled skip", plan.Skipped)
	}
}

func TestRunCodexReportsMalformedAppToolCacheWithoutBlockingImport(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "config.toml")
	err := os.WriteFile(source, []byte(`
[mcp_servers.local]
command = "npx"
args = ["-y", "local-mcp"]
`), 0o600)
	if err != nil {
		t.Fatalf("write source: %v", err)
	}
	cacheDir := filepath.Join(dir, "cache", "codex_apps_tools")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatalf("mkdir app tool cache dir: %v", err)
	}
	badCache := filepath.Join(cacheDir, "broken.json")
	if err := os.WriteFile(badCache, []byte(`{"tools": [`), 0o600); err != nil {
		t.Fatalf("write bad app tool cache: %v", err)
	}

	plan, err := Run(Options{
		Source:     SourceCodex,
		ConfigPath: filepath.Join(dir, "lazymcp.yaml"),
		SourcePath: source,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, ok := plan.Servers["local"]; !ok {
		t.Fatalf("servers = %#v, want local", plan.Servers)
	}
	if !containsString(plan.SourceFiles, badCache) {
		t.Fatalf("source files = %#v, want bad app cache", plan.SourceFiles)
	}
	want := badCache + ": Codex App tool cache could not be read for skipped connector diagnostics"
	if !containsSubstring(plan.Skipped, want) {
		t.Fatalf("skipped = %#v, want substring %q", plan.Skipped, want)
	}
}

func TestRunCodexRejectsOnlyUnsupportedPluginMCPManifests(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "config.toml")
	target := filepath.Join(dir, "lazymcp.yaml")
	err := os.WriteFile(source, []byte(`
[projects."/tmp/repo"]
trust_level = "trusted"
`), 0o600)
	if err != nil {
		t.Fatalf("write source: %v", err)
	}
	pluginDir := filepath.Join(dir, ".tmp", "plugins", "plugins", "cloudflare")
	if err := os.MkdirAll(pluginDir, 0o700); err != nil {
		t.Fatalf("mkdir plugin dir: %v", err)
	}
	err = os.WriteFile(filepath.Join(pluginDir, ".mcp.json"), []byte(`
{
  "mcpServers": {
    "cloudflare-api": {
      "type": "http",
      "url": "https://mcp.cloudflare.com/mcp"
    }
  }
}
`), 0o600)
	if err != nil {
		t.Fatalf("write plugin manifest: %v", err)
	}

	plan, err := Run(Options{
		Source:       SourceCodex,
		ConfigPath:   target,
		SourcePath:   source,
		Write:        true,
		UpdateClient: true,
	})
	if err == nil || !strings.Contains(err.Error(), "no importable Codex MCP servers found") {
		t.Fatalf("error = %v", err)
	}
	if plan == nil || !containsString(plan.Skipped, "cloudflare-api: plugin MCP manifest uses unsupported remote transport") {
		t.Fatalf("plan = %#v, want skipped remote server", plan)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target config was created for unsupported-only plugin")
	}
	gotCodex, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	if strings.Contains(string(gotCodex), "[mcp_servers.lazymcp]") {
		t.Fatalf("source was rewritten despite no importable servers:\n%s", string(gotCodex))
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

func TestRunCodexRejectsMalformedSourceConfig(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "config.toml")
	err := os.WriteFile(source, []byte(`
[mcp_servers.github]
command = "npx"
args = [
`), 0o600)
	if err != nil {
		t.Fatalf("write source: %v", err)
	}

	_, err = Run(Options{
		Source:     SourceCodex,
		ConfigPath: filepath.Join(dir, "lazymcp.yaml"),
		SourcePath: source,
	})
	if err == nil || !strings.Contains(err.Error(), "parse "+source) {
		t.Fatalf("error = %v", err)
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

	plan, err := Run(Options{
		Source:     SourceCodex,
		ConfigPath: filepath.Join(dir, "lazymcp.yaml"),
		SourcePath: source,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(plan.Servers) != 0 || len(plan.Skipped) != 1 {
		t.Fatalf("plan = %#v, want only skipped lazymcp proxy", plan)
	}
}

func TestRunCodexYesIsIdempotentWhenSourceAlreadyUsesLazyProxy(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "config.toml")
	target := filepath.Join(dir, "lazymcp.yaml")
	err := os.WriteFile(source, []byte(fmt.Sprintf(`
[mcp_servers.lazymcp]
command = "lazymcp"
args = ["serve", "--config", %q]
`, target)), 0o600)
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
	if len(plan.ChangedFiles) != 0 || len(plan.Backups) != 0 {
		t.Fatalf("plan changed files/backups = %#v/%#v, want no-op", plan.ChangedFiles, plan.Backups)
	}
}

func TestRunCodexYesTreatsEquivalentLazyProxyConfigPathAsNoop(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	source := filepath.Join(dir, "config.toml")
	err := os.WriteFile(source, []byte(`
[mcp_servers.lazymcp]
command = "lazymcp"
args = ["serve", "--config", "./lazymcp.yaml"]
`), 0o600)
	if err != nil {
		t.Fatalf("write source: %v", err)
	}

	plan, err := Run(Options{
		Source:       SourceCodex,
		ConfigPath:   "lazymcp.yaml",
		SourcePath:   source,
		Write:        true,
		UpdateClient: true,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(plan.ChangedFiles) != 0 || len(plan.Backups) != 0 {
		t.Fatalf("plan changed files/backups = %#v/%#v, want no-op", plan.ChangedFiles, plan.Backups)
	}
}

func TestRunWriteDoesNotCreateEmptyConfigWhenOnlyLazyProxyExists(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "config.toml")
	target := filepath.Join(dir, "lazymcp.yaml")
	err := os.WriteFile(source, []byte(fmt.Sprintf(`
[mcp_servers.lazymcp]
command = "lazymcp"
args = ["serve", "--config", %q]
`, target)), 0o600)
	if err != nil {
		t.Fatalf("write source: %v", err)
	}
	originalTarget := []byte(fmt.Sprintf(`
servers:
  lazymcp:
    command: lazymcp
    args:
      - serve
      - --config
      - %s
`, target))
	err = os.WriteFile(target, originalTarget, 0o600)
	if err != nil {
		t.Fatalf("write target: %v", err)
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
	if len(plan.ChangedFiles) != 0 || len(plan.Backups) != 0 {
		t.Fatalf("plan changed files/backups = %#v/%#v, want no-op", plan.ChangedFiles, plan.Backups)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != string(originalTarget) {
		t.Fatalf("target changed:\n%s", string(got))
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

func TestRunWriteRejectsInvalidExistingLazyConfig(t *testing.T) {
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
    args:
      - -y
      - filesystem
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
	if err == nil || !strings.Contains(err.Error(), "validate "+target) || !strings.Contains(err.Error(), `server "filesystem" command is required`) {
		t.Fatalf("error = %v", err)
	}
	if plan == nil {
		t.Fatalf("plan is nil")
	}
	if len(plan.ChangedFiles) != 0 || len(plan.Backups) != 0 {
		t.Fatalf("plan changed files/backups = %#v/%#v", plan.ChangedFiles, plan.Backups)
	}
}

func TestRunWriteIsIdempotentForAlreadyImportedServer(t *testing.T) {
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
    command: npx
    args:
      - -y
      - github
    namespace: github
    idle_timeout: 5m0s
    request_timeout: 10m0s
    tools:
      - name: search
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
	if len(plan.ChangedFiles) != 0 || len(plan.Backups) != 0 {
		t.Fatalf("plan changed files/backups = %#v/%#v, want no-op", plan.ChangedFiles, plan.Backups)
	}
	cfg, err := readLazyConfig(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if len(cfg.Servers["github"].Tools) != 1 {
		t.Fatalf("existing tools were not preserved: %#v", cfg.Servers["github"].Tools)
	}
}

func TestRunWriteRemovesLazyProxyFromTargetConfig(t *testing.T) {
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
	err = os.WriteFile(target, []byte(fmt.Sprintf(`
servers:
  lazymcp:
    command: lazymcp
    args:
      - serve
      - --config
      - %s
  filesystem:
    command: npx
    namespace: filesystem
`, target)), 0o600)
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
	cfg, err := readLazyConfig(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if _, ok := cfg.Servers["lazymcp"]; ok {
		t.Fatalf("target config still contains lazymcp proxy: %#v", cfg.Servers)
	}
	if _, ok := cfg.Servers["github"]; !ok {
		t.Fatalf("target config missing imported github server: %#v", cfg.Servers)
	}
}

func TestRunWriteDoesNotRemoveLazyProxyForAnotherConfig(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "config.toml")
	target := filepath.Join(dir, "lazymcp.yaml")
	otherTarget := filepath.Join(dir, "other-lazymcp.yaml")
	err := os.WriteFile(source, []byte(`
[mcp_servers.github]
command = "npx"
args = ["-y", "github"]
`), 0o600)
	if err != nil {
		t.Fatalf("write source: %v", err)
	}
	err = os.WriteFile(target, []byte(fmt.Sprintf(`
servers:
  nested_proxy:
    command: lazymcp
    args:
      - serve
      - --config
      - %s
`, otherTarget)), 0o600)
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
	cfg, err := readLazyConfig(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if _, ok := cfg.Servers["nested_proxy"]; !ok {
		t.Fatalf("non-self lazymcp proxy was removed: %#v", cfg.Servers)
	}
}

func TestRunWriteReplacesSameNameLazySelfProxyWithoutConflict(t *testing.T) {
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
	err = os.WriteFile(target, []byte(fmt.Sprintf(`
servers:
  github:
    command: lazymcp
    args:
      - serve
      - --config
      - %s
`, target)), 0o600)
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
	cfg, err := readLazyConfig(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if got := cfg.Servers["github"].Command; got != "npx" {
		t.Fatalf("github command = %q, want imported npx server", got)
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
	if _, err := config.Load(target); err != nil {
		t.Fatalf("written lazymcp config failed validation: %v", err)
	}
	if _, err := readCodexConfig(source); err != nil {
		t.Fatalf("written codex config failed validation: %v", err)
	}
}

func TestRunWriteRegistersProxyAndRemovesImportedCodexServersWhenTargetAlreadyHasSome(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "config.toml")
	target := filepath.Join(dir, "lazymcp.yaml")
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
	err = os.WriteFile(target, []byte(`
servers:
  github:
    command: npx
    args:
      - -y
      - github
    namespace: github
    idle_timeout: 5m0s
    request_timeout: 10m0s
    tools:
      - name: search
`), 0o600)
	if err != nil {
		t.Fatalf("write target: %v", err)
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
		t.Fatalf("changed files = %#v, want target and source", plan.ChangedFiles)
	}

	cfg, err := readLazyConfig(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if len(cfg.Servers["github"].Tools) != 1 {
		t.Fatalf("existing github tools were not preserved: %#v", cfg.Servers["github"].Tools)
	}
	if _, ok := cfg.Servers["filesystem"]; !ok {
		t.Fatalf("target config missing newly imported filesystem server: %#v", cfg.Servers)
	}

	codexConfig, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	got := string(codexConfig)
	if strings.Contains(got, "[mcp_servers.github]") || strings.Contains(got, "[mcp_servers.filesystem]") {
		t.Fatalf("codex config kept migrated direct MCP servers:\n%s", got)
	}
	if !strings.Contains(got, "[mcp_servers.lazymcp]") {
		t.Fatalf("codex config missing lazymcp proxy:\n%s", got)
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

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsSubstring(values []string, want string) bool {
	for _, value := range values {
		if strings.Contains(value, want) {
			return true
		}
	}
	return false
}
