# lazymcp

`lazymcp` は、必要になったときだけバックエンドの MCP サーバーを起動する、軽量な MCP プロキシ / ルーターです。

## インストール

```bash
brew tap s4na/lazymcp https://github.com/s4na/lazymcp
brew install lazymcp
```

Go でもインストールできます。

```bash
go install github.com/s4na/lazymcp/cmd/lazymcp@latest
```

## 使い方

Codex にすでに設定されている MCP サーバーをインポートします。
対話可能な端末で `--write` を指定すると、インポート後に `lazymcp` だけを Codex の MCP サーバーとして登録するか確認します。

```bash
lazymcp migrate codex --write
```

既存の Codex MCP サーバーをインポートした後、`lazymcp` は Codex の直接的な MCP サーバー登録を単一の `lazymcp` プロキシエントリに置き換えるか確認します。
Codex CLI と Codex app の両方で MCP に `lazymcp` だけを使うには、`yes` と答えてください。

```bash
Register lazymcp as the only MCP server in the source client? [y/N] yes
```

非対話でセットアップする場合は `-y` を使うと、確認なしで lazymcp 設定を書き込み、Codex 登録も自動承認します。

```bash
lazymcp migrate codex -y
```

バックエンドのツールは、`github.search_repositories` のように名前空間のプレフィックス付きで公開されます。
設定に `tools:` が書かれているバックエンドプロセスは、対応する最初の `tools/call` で起動し、アイドルタイムアウト後に停止します。
Codex から移行した直後のように `tools:` が空のバックエンドは、クライアントからの最初の `tools/list` で起動してツール一覧を検出し、そのセッション内でキャッシュします。

`lazymcp migrate codex` は Codex の `~/.codex/config.toml` を読み取り、直接登録されている各 `[mcp_servers.<name>]` エントリを `~/.config/lazymcp/config.yaml` に移します。
既存ファイルを置き換える前には、タイムスタンプ付きのバックアップを作成します。
変換後の Codex MCP 設定には次のエントリだけが残ります。

```toml
[mcp_servers.lazymcp]
command = "lazymcp"
args = ["serve", "--config", "/path/to/config.yaml"]
```

Codex にまだ登録されていないサーバーを追加したい場合だけ、lazymcp 設定を手動で作成または編集してください。

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

### バックエンドコマンド

`command` は起動する実行ファイルで、`args` はそのまま引数として渡されます。
そのため、`npx`、`uvx`、`uv run`、`mise exec` のようなパッケージランナーをバックエンドの起動に使えます。

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

`lazymcp` はバックエンドコマンドをシェル経由では実行しないため、シェル構文は展開されません。
コマンド行全体を `command` に入れるのではなく、実行ファイルと各引数を分けて書いてください。

```yaml
# Good
command: uv
args: ["run", "python", "-m", "my_mcp_server"]

# Not supported
command: "uv run python -m my_mcp_server"
```

## バックエンドのライフサイクル

`lazymcp serve` は、現在の stdio セッション内でバックエンド MCP プロセスを管理します。
クライアントが stdio を閉じて `lazymcp serve` プロセスが終了すると、実行中のバックエンドもシャットダウンされます。

`tools:` が設定されているバックエンドは、対応する最初のツール呼び出しで遅延起動されます。
`tools:` が空のバックエンドは、現在のセッションで最初に受け取る `tools/list` で一度だけツール検出を試みます。
`idle_timeout` によってバックエンドが停止していた場合、後続のツール呼び出しでは、リクエストを転送する前に新しいバックエンドプロセスを起動します。
バックエンドが保持する状態はメモリ上だけの場合があります。そのため、アイドル停止、クラッシュ、リクエストタイムアウト、セッション終了の後は、前回のプロセスの状態が失われることがあります。
永続化が必要な状態は、バックエンドサーバー自身で管理してください。

`lazymcp` は、各バックエンドについて現在のプロセス内のライフサイクル状態を追跡します。

- `running`: バックエンドプロセスが実行中です。
- `stopped`: 明示的なシャットダウン後、または起動前のため、バックエンドは実行されていません。
- `idle-stopped`: `idle_timeout` の経過によりバックエンドが停止しました。
- `crashed`: バックエンドが予期せず終了した、起動に失敗した、またはリクエストタイムアウトやトランスポートエラーの後に停止されました。

ライフサイクルのメタデータには、最終起動時刻、最終停止時刻、停止理由、最後のエラーが含まれます。

```bash
lazymcp inspect --config config.yaml
```

単独で実行する `inspect` は、設定済みバックエンドをライフサイクル列と初期のプロセスローカル状態付きで表示します。
別の CLI プロセスとして実行されるため、すでに実行中の `lazymcp serve` セッションが持つメモリ上の状態は読み取れません。そのため、設定済みバックエンドは `stopped` として表示されます。

## Codex MCP 設定の移行

Codex にすでに設定されている MCP サーバーをプレビューします。

```bash
lazymcp migrate codex --dry-run
```

`--write` を指定しない限り、プレビューがデフォルトです。意図を明示するために `--dry-run` も指定できます。

インポートしたサーバーを lazymcp の設定に書き込みます。

```bash
lazymcp migrate codex --write --config ~/.config/lazymcp/config.yaml
```

`--write` を指定すると、対話可能な端末では Codex の直接的な MCP サーバーエントリを単一の `lazymcp` プロキシエントリに置き換えるか確認します。
明示的に置き換える場合は `--register-client` を使ってください。
`-y` を使うと、lazymcp 設定を書き込み、確認なしで登録も自動承認します。

```bash
lazymcp migrate codex -y
```

`--write` はサーバーエントリを安全にマージし、既存の lazymcp 設定を置き換える前にタイムスタンプ付きのバックアップを作成します。
サーバー名または名前空間がすでに存在する場合、移行は決定的な競合レポートを出して停止します。
同じ名前の既存サーバーをインポートしたエントリで置き換えたいことが明確な場合だけ、`--overwrite` を使ってください。
別のサーバーとの名前空間の競合がある場合は、引き続き移行を停止します。

移行処理は Codex の `~/.codex/config.toml` を読み取り、インポート前に `[mcp_servers.<name>]` テーブルが含まれていることを検証します。
各テーブルには、文字列の `command`、任意の文字列配列 `args`、任意の文字列値テーブル `env` を指定できます。

ドライランのレポートでは、トークンやシークレットが出力されないように環境変数の値をマスクします。
インポートしたサーバーにはツールスキーマは含まれません。Codex の MCP 設定にはバックエンドの起動コマンドしか含まれていないためです。
移行後の `lazymcp serve` は、`tools:` が空のバックエンドについて最初の `tools/list` でバックエンドからツール一覧を検出します。
検出した一覧は現在の stdio セッション内で保持されます。ツールを完全に遅延起動したい場合や、起動できない環境でも一覧を表示したい場合は、`tools:` エントリを設定ファイルに追加してください。

Codex 登録が有効な場合、元の Codex 設定はバックアップされ、`[mcp_servers]` には `lazymcp` だけが残るように書き換えられます。
その他の Codex 設定は保持されます。

## ライセンス

MIT License です。詳細は [LICENSE](LICENSE) を参照してください。
