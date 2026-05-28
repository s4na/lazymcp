# lazymcp

`lazymcp` is a lightweight MCP proxy/router that lazily spawns backend MCP servers on demand.

## Install

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
