# lazymcp 設計書

## 概要

`lazymcp` は MCP (Model Context Protocol) サーバー群を lazy/on-demand に起動するための MCP proxy/router である。

主目的は以下。

* Codex App / Codex CLI が全 MCP を毎回起動してしまう問題を回避
* 重い MCP による起動遅延・RAM消費を削減
* tool schema 汚染を最小化
* MCP 群を一元管理
* stdio MCP を常駐 proxy 経由で利用可能にする

`lazymcp` 自体は 1つの MCP server として Codex に登録される。

```text
Codex App
   ↓
lazymcp
   ├─ github MCP
   ├─ playwright MCP
   ├─ postgres MCP
   └─ notion MCP
```

backend MCP は必要時のみ起動される。

---

# 背景と課題

## 現状の Codex App の問題

Codex App / Codex CLI は現状:

1. 起動時に全 MCP server を spawn
2. tools/list を収集
3. tool schema を session context に投入

という動作をする。

そのため:

* 起動が遅い
* RAM 使用量が増える
* subprocess が大量に常駐
* Playwright など重い MCP が常時起動
* 不要 tool schema が context を圧迫
* subagent にも全 MCP が継承される

という問題がある。

また:

* session 開始後に MCP を動的追加できない
* `enabled=false` は単なる disable であり lazy load ではない

という制約がある。

---

# プロダクト目標

## MUST

* backend MCP を必要時のみ起動
* Codex 側からは単一 MCP として見える
* backend MCP の stdio を proxy
* backend process lifecycle 管理
* process pooling
* idle timeout shutdown
* backend crash recovery
* 単一 binary 配布
* macOS/Linux 対応

## SHOULD

* tool namespace routing
* backend capability filtering
* remote MCP support
* streamable HTTP support
* SSE support
* structured logging
* metrics

## MAY

* auth
* permissioning
* caching
* schema compression
* tracing
* GUI dashboard

---

# lazymcp の役割

`lazymcp` は:

* MCP server
* process manager
* router
* stdio multiplexer

を兼ねる。

backend MCP は lazymcp 管理下で動作する。

---

# システム構成

## 全体構成

```text
+----------------+
| Codex App      |
+--------+-------+
         |
         | MCP
         v
+----------------+
| lazymcp        |
|----------------|
| Router         |
| ProcessManager |
| ToolRegistry   |
| TransportLayer |
+---+--------+---+
    |        |
    |        |
    v        v

+--------+  +--------+
| GitHub |  | Playwr |
| MCP    |  | MCP    |
+--------+  +--------+
```

---

# アーキテクチャ

## Core Components

### Router

責務:

* tool call routing
* namespace 解決
* backend selection

例:

```text
github.search_repositories
```

↓

```text
github backend MCP
```

---

### ProcessManager

責務:

* backend process spawn
* lifecycle 管理
* idle shutdown
* restart
* healthcheck

---

### ToolRegistry

責務:

* backend tool schema cache
* namespace mapping
* lazy discovery

---

### TransportLayer

責務:

* stdio transport
* HTTP transport
* SSE transport
* stream forwarding

---

# Lazy Loading 戦略

## 方針

### backend process lazy

実装する。

### tool schema lazy

初期版では部分対応。

理由:

Codex 側が session 開始時に tools/list を要求するため。

---

# 起動フロー

## 初回 session

```text
Codex
  ↓ tools/list
lazymcp
  ↓
cached schema を返却
```

backend MCP はまだ起動しない。

---

## 初回 tool call

```text
tool: github.search_repositories
```

↓

```text
Router
  ↓
ProcessManager
  ↓
spawn github MCP
  ↓
tool call forward
```

---

# Process Lifecycle

## Spawn

初回 tool call 時に spawn。

## Keepalive

一定時間 idle なら保持。

例:

```yaml
idle_timeout: 300s
```

## Shutdown

idle timeout 超過で終了。

## Restart

backend crash 時は自動 restart。

---

# Tool Namespace

## 推奨

backend namespace を prefix 化。

例:

```text
github.*
playwright.*
postgres.*
```

理由:

* tool collision 回避
* routing 容易化
* observability 向上

---

# Config 設計

## config.yaml

```yaml
servers:
  github:
    command: "npx"
    args:
      - "-y"
      - "@modelcontextprotocol/server-github"

    namespace: "github"

    idle_timeout: 300s

  playwright:
    command: "npx"
    args:
      - "-y"
      - "@playwright/mcp"

    namespace: "playwright"

    idle_timeout: 60s
```

---

# Codex Integration

## Codex config.toml

```toml
[mcp_servers.lazymcp]
command = "lazymcp"
args = ["serve"]
```

Codex 側には lazymcp のみ登録する。

backend MCP を直接 Codex に登録しない。

---

# CLI 設計

## serve

```bash
lazymcp serve
```

MCP server 起動。

---

## inspect

```bash
lazymcp inspect
```

backend 状態表示。

---

## list

```bash
lazymcp list
```

tool 一覧。

---

## logs

```bash
lazymcp logs github
```

backend log 表示。

---

# 技術選定

## 言語

Go を採用。

理由:

* subprocess 管理が強い
* concurrency が容易
* single binary 配布
* stream forwarding に向く
* cross-platform
* long-running daemon に適している

---

# 推奨ライブラリ

## MCP

候補:

* mark3labs/mcp-go

## CLI

* cobra

## Config

* viper

## Logging

* zap

---

# パッケージ構成

```text
cmd/
  lazymcp/

internal/
  router/
  process/
  registry/
  transport/
  config/
  backend/
  logging/

pkg/
  protocol/
```

---

# MVP スコープ

## MVP で実装するもの

* stdio MCP backend
* lazy spawn
* idle timeout
* namespace routing
* schema cache
* process restart
* YAML config
* structured logging

---

# MVP でやらないもの

* GUI
* auth
* distributed mode
* schema compression
* permissioning
* advanced caching

---

# 将来拡張

## Remote MCP

```yaml
servers:
  deepwiki:
    url: https://mcp.deepwiki.com/mcp
```

---

## Streamable HTTP

stdio backend を HTTP bridge 化。

---

## Permission Layer

tool ごと ACL。

---

# OSS 方針

## 名前

推奨:

```text
lazymcp
```

理由:

* ecosystem naming に自然
* lazygit/lazydocker 系の文化に合う
* lazy loading の意味も含められる
* CLI 名として短い

---

# README 用の一文

```text
lazymcp is a lightweight MCP proxy/router
that lazily spawns backend MCP servers on demand.
```

---

# 成功条件

以下を満たしたら成功。

* Codex 起動時に heavy MCP が起動しない
* 初回 tool call 時のみ backend spawn
* idle backend が自動 shutdown
* Codex からは通常の MCP として利用可能
* Playwright 等 heavy MCP 利用時の UX 改善
* RAM/CPU usage 削減

---

# 非目標

以下は初期スコープ外。

* Codex 本体の tool schema lazy loading 改善
* MCP protocol 拡張
* LLM orchestration
* agent framework 化

---

# 実装優先順位

## Phase 1

* stdio proxy
* lazy spawn
* routing
* schema cache

## Phase 2

* idle shutdown
* restart
* metrics
* observability

## Phase 3

* HTTP/SSE support
* remote MCP
* auth
* permissioning

---

# 最終方針

`lazymcp` は Skill ではなく MCP/router として実装する。

Skill は optional companion として提供する。

```text
Core:
  Go binary MCP router

Optional:
  Codex Skill
```

これが最も実用的かつ継続運用しやすい構成である。
