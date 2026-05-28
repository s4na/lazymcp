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

### Backend commands

`command` is the executable to spawn, and `args` are passed to it directly.
This means package runners such as `npx`, `uvx`, `uv run`, and `mise exec` can be used as backend launchers:

```yaml
servers:
  filesystem:
    command: npx
    args:
      - -y
      - "@modelcontextprotocol/server-filesystem"
      - /path/to/root
```

```yaml
servers:
  python_tools:
    command: uvx
    args:
      - example-mcp-server
```

```yaml
servers:
  local_python:
    command: uv
    args:
      - run
      - python
      - -m
      - my_mcp_server
```

```yaml
servers:
  mise_managed:
    command: mise
    args:
      - exec
      - --
      - uvx
      - example-mcp-server
```

`lazymcp` does not run backend commands through a shell, so shell syntax is not expanded.
Write the executable and each argument separately instead of putting the whole command line in `command`:

```yaml
# Good
command: uv
args: ["run", "python", "-m", "my_mcp_server"]

# Not supported
command: "uv run python -m my_mcp_server"
```

## Backend lifecycle

`lazymcp serve` manages backend MCP processes inside the current stdio session.
When the client closes stdio and the `lazymcp serve` process exits, running backends are shut down.

Each backend is spawned lazily on the first matching tool call. If the backend was stopped by
`idle_timeout`, a later tool call starts a fresh backend process before forwarding the request.
Backends may keep state only in memory, so state from the previous process can be lost after an
idle stop, crash, request timeout, or session shutdown. Persistent state must be owned by the
backend server itself.

`lazymcp` tracks the current process-local lifecycle state for each backend:

- `running`: the backend process is active.
- `stopped`: the backend is not running after an explicit shutdown or before it has been started.
- `idle-stopped`: the backend was stopped because `idle_timeout` elapsed.
- `crashed`: the backend exited unexpectedly, failed to start, or was stopped after a request timeout or transport error.

Lifecycle metadata includes last started time, last stopped time, stop reason, and last error.

```bash
lazymcp inspect --config config.yaml
```

Standalone `inspect` prints the configured backends with lifecycle columns and their initial
process-local state. Because it runs as a separate CLI process, it cannot read live in-memory state
from an already-running `lazymcp serve` session; its configured backends are shown as `stopped`.

## Migrate Codex MCP Settings

Preview MCP servers already configured in Codex:

```bash
lazymcp migrate codex --dry-run
```

Preview is the default unless `--write` is set; `--dry-run` is accepted to make the intent explicit.

Write imported servers into the lazymcp config:

```bash
lazymcp migrate codex --write --config ~/.config/lazymcp/config.yaml
```

`--write` safely merges server entries and creates a timestamped backup before replacing an existing lazymcp config. If a server name or namespace already exists, the migration stops with a deterministic conflict report. Use `--overwrite` only when you intentionally want the imported entry to replace an existing server with the same name; namespace conflicts with other servers still stop the migration.

The migration reads Codex's `~/.codex/config.toml` and validates that it contains `[mcp_servers.<name>]` tables with a string `command`, optional string-array `args`, and optional string-value `env` table before importing anything.

Dry-run reports mask environment values so tokens and secrets are not printed. Imported servers do not include tool schemas because Codex MCP settings only contain backend launch commands; add `tools:` entries after migration or discover them with your usual MCP tooling.

After migration, remove direct backend MCP servers from Codex and leave only the lazymcp proxy entry.
