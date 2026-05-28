# リポジトリガイドライン

## テスト戦略

テストの優先順位はpackage名だけで判断せず、振る舞いのリスクで決める。何をassertすべきか決める前に、その経路で実際に渡る値を追跡する。

1. まずprotocol境界をカバーする。`internal/mcp` と `internal/server` では、正常なJSON-RPCメッセージ、不正なheaderやparams、未知method、未知tool、response/errorの形をtable-driven testで確認する。
2. 次にconfiguration routingをカバーする。`internal/config` ではdefault値、namespace展開、重複検出、duration parsing、tool順序の安定性、configのread/parse失敗を確認する。
3. backend processの振る舞いは実processよりfakeを優先する。小さなtest helper commandやin-memory codecを使い、initialize、tools/call、backendの `-32000` 後のretry、shutdown、request timeout、idle timeoutを検証する。
4. CLIテストは薄く保つ。`list` と `inspect` のcommand構築とuser-visible outputを確認し、長時間動く `serve` の振る舞いはserver-level testに任せる。
5. bug fixにはregression testを追加する。修正前に失敗し、修正後に通る最小のtestを書く。
6. coverageのためだけのtestは避ける。coverageはGitHub Actionsでgapを見つけるために確認するが、新しいtestでは有用な振る舞いとedge caseをassertする。
7. runtime behaviorに影響する変更では、PR作成または更新前に `go test ./... -covermode=atomic -coverpkg=./... -coverprofile=coverage.out` を実行する。
8. PRを作成または更新した後、そのPRのGitHub Actions実行結果を確認し、CIとsecurity workflowが成功していることを確かめる。
