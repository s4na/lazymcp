# lazymcp

`lazymcp` is a lightweight MCP proxy/router that lazily spawns backend MCP servers on demand.

## Install

```bash
brew tap s4na/lazymcp https://github.com/s4na/lazymcp
brew install lazymcp
```

Or install with Go:

```bash
go install github.com/s4na/lazymcp/cmd/lazymcp@latest
```

## Usage

Create a config file:

```yaml
servers:
  github:
    command: npx
    args:
      - -y
      - "@modelcontextprotocol/server-github"
    namespace: github
    idle_timeout: 300s
    request_timeout: 10m
    tools:
      - name: search_repositories
        description: Search GitHub repositories.
        input_schema:
          type: object
          properties:
            query:
              type: string
          required:
            - query
```

Run the proxy as a stdio MCP server:

```bash
lazymcp serve --config config.yaml
```

Register only `lazymcp` in Codex:

```toml
[mcp_servers.lazymcp]
command = "lazymcp"
args = ["serve", "--config", "/path/to/config.yaml"]
```

Backend tools are exposed with their namespace prefix, such as `github.search_repositories`.
The backend process is started on the first matching `tools/call` and stopped after its idle timeout.

## Migrate Codex MCP Settings

Preview MCP servers already configured in Codex:

```bash
lazymcp migrate codex --dry-run
```

Write imported servers into the lazymcp config:

```bash
lazymcp migrate codex --write --config ~/.config/lazymcp/config.yaml
```

`--write` safely merges server entries and creates a timestamped backup before replacing an existing lazymcp config. If a server name or namespace already exists, the migration stops with a deterministic conflict report. Use `--overwrite` only when you intentionally want the imported entry to replace an existing server with the same name; namespace conflicts with other servers still stop the migration.

The migration reads Codex's `~/.codex/config.toml` and validates that it contains `[mcp_servers.<name>]` tables with a string `command`, optional string-array `args`, and optional string-value `env` table before importing anything.

Dry-run reports mask environment values so tokens and secrets are not printed. Imported servers do not include tool schemas because Codex MCP settings only contain backend launch commands; add `tools:` entries after migration or discover them with your usual MCP tooling.

After migration, remove direct backend MCP servers from Codex and leave only the lazymcp proxy entry.
