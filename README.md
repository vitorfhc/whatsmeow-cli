# wa — WhatsApp CLI

`wa` is a command-line interface to WhatsApp, built on the
[whatsmeow](https://github.com/tulir/whatsmeow) library. A background daemon
holds the WhatsApp connection and stores incoming messages; the CLI talks to it
over a local Unix socket and prints JSON. It is designed to be driven by an AI
agent (it replaces an MCP server), so every command emits machine-readable JSON
and a meaningful exit code.

## Requirements

- **Go ≥ 1.25** (whatsmeow requires it; with the default `GOTOOLCHAIN=auto` the
  Go command downloads the toolchain automatically).
- A C toolchain (the SQLite driver uses CGO).

## Build

```
make build        # produces ./wa
# or: go build -o wa ./cmd/wa
```

## Quick start

```
./wa start                          # start the background daemon
./wa login 5511999999999            # returns a pairing code to enter on your phone
# or, to link by scanning instead of typing a code:
./wa login-qr | jq -r .qr           # prints a scannable QR block; scan it with your phone
./wa status                         # poll until "logged_in": true
./wa send 5511999999999 "hello"     # send a text message
./wa messages                       # list new (unseen) received messages
./wa chats                          # list recent conversations
./wa stop                           # stop the daemon
```

Linking (login): open WhatsApp on your phone → Settings → Linked Devices →
Link a device → "Link with phone number instead" → enter the pairing code.

QR login (`wa login-qr`) is an alternative: it returns a QR code rendered as a
terminal block in the `qr` JSON field (stdout stays pure JSON, so view it with
`wa login-qr | jq -r .qr` or let the agent print it), which you scan from the
same Linked Devices screen. The code is single-shot and expires (~60s); if it
lapses, wait for the session to time out and rerun `wa login-qr`.

## Commands

| Command | Description |
|---|---|
| `wa start` | Start the background daemon (idempotent). |
| `wa stop` | Stop the daemon. |
| `wa status` | Daemon/connection state (JSON). |
| `wa login <phone>` | Link an account via pairing code. |
| `wa login-qr` | Link an account by scanning a QR code (returned in the `qr` field). |
| `wa logout [--purge]` | Unlink the account (`--purge` also clears stored messages). |
| `wa send <recipient> <text>` | Send a text message (recipient = phone or JID). |
| `wa messages [flags]` | List received messages. Flags: `--chat`, `--unread`, `--all`, `--since <RFC3339>`, `--limit`, `--mark-read`. |
| `wa chats [--limit N]` | List recent chats. |

Global flags: `--data-dir DIR` (default `$WA_CLI_HOME` or `~/.wa-cli`),
`--pretty` (indent JSON).

## Output & exit codes

Success prints JSON to stdout and exits 0. Failure prints
`{"error":"<code>","message":"..."}` to stderr with a non-zero exit code:

| Exit | Code |
|---|---|
| 2 | `usage` |
| 3 | `daemon_not_running` |
| 4 | `not_logged_in` |
| 5 | `already_logged_in` |
| 6 | `invalid_recipient` |
| 7 | `send_failed` |
| 8 | `login_failed` |
| 1 | `generic` |

(`wa status` reports `{"daemon":"stopped"}` with exit 0 when the daemon is down.)

## Development

```
make test   # go test -race ./...
make lint   # go vet + errcheck + golangci-lint
make fmt    # gofmt -w .
```

See `CLAUDE.md` for engineering standards and `docs/superpowers/specs/` for the
design. Beyond the MVP (media, groups, reactions, presence, newsletters, …) is
tracked in the spec's fast-follow roadmap.
