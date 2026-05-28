# Repository Guidelines

## Test Strategy

Prioritize tests by behavioral risk, not by package names alone. Trace the values passed through each path before deciding what a test should assert.

1. Cover protocol boundaries first: `internal/mcp` and `internal/server` should have table-driven tests for valid JSON-RPC messages, malformed headers or params, unknown methods, unknown tools, and response/error shapes.
2. Cover configuration routing next: `internal/config` tests should exercise default values, namespace expansion, duplicate detection, duration parsing, stable tool ordering, and config read or parse failures.
3. Cover backend process behavior with fakes before real processes: prefer small test helper commands or in-memory codecs to verify initialize, tools/call, retry after backend `-32000`, shutdown, request timeout, and idle timeout behavior.
4. Keep CLI tests thin: test command construction and user-visible output for `list` and `inspect`; leave long-running `serve` behavior to server-level tests.
5. Add regression tests with the bug fix. Each bug fix should include the smallest test that fails before the fix and passes after it.
6. Avoid coverage-only tests. Coverage is checked in GitHub Actions to reveal gaps, but new tests should assert useful behavior and edge cases.
7. Run `go test ./... -covermode=atomic -coverpkg=./... -coverprofile=coverage.out` before opening or updating a pull request when the change affects runtime behavior.
