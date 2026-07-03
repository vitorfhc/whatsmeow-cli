# CLAUDE.md — whatsmeow-cli

Agent guidance for this repository. Read this first.

## Project

`wa` is a WhatsApp CLI backed by a local daemon. It wraps the
[`whatsmeow`](https://github.com/tulir/whatsmeow) Go library (WhatsApp Web
multi-device protocol) and is designed to be driven by a Claude skill — it
replaces an MCP server. WhatsApp needs a persistent connection to stay linked
and receive messages, so a **background daemon** holds the connection and
stores incoming messages, while the **thin CLI** sends one request per
invocation over a local Unix socket and prints JSON. Design spec:
`docs/superpowers/specs/2026-07-02-whatsmeow-cli-mvp-design.md`.

MVP scope: link account (pairing code), send text, read received messages,
list chats. Media, groups, reactions, presence, etc. are deferred (see the
spec's roadmap).

## Architecture

```
wa (CLI)  --unix socket, newline JSON-->  wa daemon (whatsmeow client + SQLite)
```

Packages:
- `cmd/wa` — cobra commands; composition root; start/stop/detach; `__daemon__`.
- `internal/daemon` — request dispatch, event handling, socket server.
- `internal/wa` — pure helpers (phone/JID, message extraction) + the `Client`
  interface wrapping whatsmeow (mockable) and the real adapter.
- `internal/store` — SQLite `messages` table (shares the DB with whatsmeow's
  `sqlstore`).
- `internal/ipc` — request/response types + newline-JSON framing (unescaped).
- `internal/client` — thin CLI: dial socket, print response, map exit code.
- `internal/api` — DTOs + error-code→exit-code map (no whatsmeow dependency).
- `internal/config` — data-dir/path resolution.

## Paths

Data directory precedence: `--data-dir` flag > `$WA_CLI_HOME` > `~/.wa-cli`.
Files inside it: `store.db`, `daemon.sock`, `daemon.pid`, `daemon.log`.

## Engineering standards (mandatory)

- **Go**, single module. Add dependencies **only** via `go get <module>@latest`;
  never hand-edit versions in `go.mod`. Run `go mod tidy` after changes.
- **CLI**: `github.com/spf13/cobra`. stdout carries machine-readable JSON only;
  logs/diagnostics go to stderr and `daemon.log`. Non-interactive (no prompts).
- **Output hygiene**: no emojis, no ANSI color, no decoration, no progress
  bars. Compact JSON by default (`--pretty` opts in). Concise, factual error
  messages. JSON is emitted without HTML-escaping (`ipc.Marshal`) so message
  text stays readable.
- **TDD, always**: failing test first, then minimal code, then refactor. Pure
  logic is table-driven and fully unit-tested. whatsmeow is behind the
  `wa.Client` interface so the daemon is tested with a fake — tests never hit
  real WhatsApp servers.
- **Fail fast, no silent errors**: validate at boundaries, return early. Wrap
  errors with `fmt.Errorf("...: %w", err)` and propagate. Meaningful error
  returns must be checked. Deferred/best-effort resource cleanup
  (`Close`, `Remove`) may use an explicit `_ =` to signal intent.
- **Concurrency**: the daemon serves connections on goroutines. It keeps no
  unsynchronized shared mutable state — status is read from the (thread-safe)
  whatsmeow client, messages from the (thread-safe) `*sql.DB`. SQLite is opened
  once and shared (WAL + `busy_timeout`). Any new shared state must be guarded.
  All tests run under `-race`.

## Commands

```
make build   # go build -o wa ./cmd/wa
make test    # go test -race ./...
make lint    # go vet + errcheck + golangci-lint
make fmt     # gofmt -w .
make tidy    # go mod tidy
```

Lint tools live in `$(go env GOPATH)/bin`. If `golangci-lint` reports it was
built with an older Go than the module targets, reinstall it forcing the
toolchain: `GOTOOLCHAIN=go1.25.11 go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest`.

## whatsmeow Go-version gotcha

`whatsmeow@latest` requires **Go ≥ 1.25** (its `go.mod` sets a `go1.26`
toolchain). With `GOTOOLCHAIN=auto` (the default) the Go command downloads the
needed toolchain automatically, so a machine on Go 1.23 still builds. If
toolchain download is unavailable, pin whatsmeow to a release compatible with
the installed Go — still via `go get`, never by editing `go.mod`.

SQLite uses `github.com/mattn/go-sqlite3` (CGO); a C toolchain is required to
build. Under the race detector on recent macOS you may see a benign
`ld: warning: malformed LC_DYSYMTAB` — it is a linker warning, not a failure.
