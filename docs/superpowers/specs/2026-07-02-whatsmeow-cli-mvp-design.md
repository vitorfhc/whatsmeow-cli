# whatsmeow CLI — MVP Design Specification

**Status:** Draft for review
**Date:** 2026-07-02
**Author:** Vitor Falcão (busfactor) + Claude
**Purpose of this document:** A self-contained build specification for an MVP command-line tool that wraps the [`whatsmeow`](https://github.com/tulir/whatsmeow) Go library. This document is written to be handed to another AI/engineer to implement from scratch, so it embeds the specific library API calls, engineering standards, and gotchas needed to build it without re-reading the whatsmeow source.

---

## 1. Goal & Context

Build a WhatsApp CLI, `wa`, that a **Claude skill** can drive to send and receive WhatsApp messages. It **replaces an MCP server**: instead of a long-running MCP process exposing tools, we expose a small local **daemon** (the long-lived piece that holds the WhatsApp connection) plus a thin **CLI** (the request/response surface the skill calls). Commands emit **JSON** so an AI agent can parse results deterministically.

### Design principles
- **KISS / MVP first.** Ship the smallest thing that is genuinely useful, then scale. Anything not on the MVP list in §3 is explicitly out of scope for v1.
- **Agent-friendly.** Predictable subcommands, JSON on stdout, machine-readable errors, meaningful exit codes.
- **One account, one host.** Single linked WhatsApp number, single local user. Multi-account and remote access are later work.

### Why a daemon (the core architectural fact)
WhatsApp's multi-device protocol requires a **persistent, authenticated websocket** to stay linked and to receive messages. A CLI invocation is short-lived, so the CLI alone cannot receive messages that arrive while it isn't running. The daemon owns the connection and the message store; the CLI is a thin client that talks to it over a local Unix socket. This is the same shape as the MCP server it replaces.

---

## 2. High-Level Architecture

```
┌─────────────────┐        Unix domain socket        ┌──────────────────────────────┐
│  wa (CLI)       │  ───── JSON request/response ───▶ │  wa daemon                    │
│  short-lived    │  ◀──────────────────────────────  │  long-lived process           │
│                 │                                    │                               │
│  send/messages/ │                                    │  ┌─────────────────────────┐ │
│  status/login/… │                                    │  │ whatsmeow.Client        │ │
└─────────────────┘                                    │  │  - persistent websocket │ │
                                                        │  │  - event handler        │ │
   Claude skill calls the CLI ─────────────────────────┤  │  - auto-reconnect       │ │
                                                        │  └─────────────────────────┘ │
                                                        │  ┌─────────────────────────┐ │
                                                        │  │ SQLite (single file)    │ │
                                                        │  │  - whatsmeow session    │ │
                                                        │  │    (sqlstore tables)    │ │
                                                        │  │  - messages table (ours)│ │
                                                        │  └─────────────────────────┘ │
                                                        └──────────────────────────────┘
```

- **Language:** Go (required — whatsmeow is a Go library linked directly into the daemon).
- **Transport (CLI ↔ daemon):** Unix domain socket, local-only. Newline-delimited JSON, one request/response per connection.
- **Persistence:** one SQLite file holding both whatsmeow's own session tables (managed by its `sqlstore`) and our `messages` table.
- **Receiving:** the daemon registers a whatsmeow event handler; incoming messages are written to the `messages` table. The CLI (`wa messages`) reads from that table — a **poll** model. No streaming/push in the MVP.

### Files & paths
All under a data directory, default `~/.wa-cli/` (override with `--data-dir` flag or `WA_CLI_HOME` env var):

| File | Purpose |
|---|---|
| `store.db` | SQLite database (whatsmeow session + our messages) |
| `daemon.sock` | Unix domain socket the daemon listens on |
| `daemon.pid` | PID of the running daemon |
| `daemon.log` | Daemon log output (whatsmeow logs + our logs) |

---

## 3. MVP Scope

### In scope (v1)
1. **Daemon lifecycle:** `wa start`, `wa stop`, `wa status`.
2. **Link account** via pairing code: `wa login <phone>`, and `wa logout`.
3. **Send text messages** to a person or group: `wa send <recipient> <text>`.
4. **Read received messages** from the store: `wa messages [filters]`.
5. **List recent chats** (cheap, derived from the messages table): `wa chats`.
6. Incoming messages of **all types** are stored; text is extracted, non-text stored as a typed placeholder (`[image]`, `[audio]`, `[video]`, `[document]`, `[sticker]`, `[location]`, `[contact]`, `[other]`). **No media bytes are downloaded** in v1.

### Explicitly out of scope (deferred to fast-follow — see §13)
Media send/download; QR login; reactions, edits, delete/revoke, replies, mentions; group creation/administration; contact/user lookup (`IsOnWhatsApp`, profile pics, business profiles); presence/typing send & subscribe; privacy settings; chat management (mute/pin/archive/star/block); newsletters/channels; calls; multi-account; remote/networked access; streaming/push delivery of new messages; history-sync backfill exposure.

---

## 4. Engineering Standards & Implementation Details

These are **mandatory** and apply to all code. They are duplicated into the project's `CLAUDE.md` (see §4.7) so the builder and future agents follow them.

### 4.1 Language & dependency management
- **Go** (single module). Initialize with `go mod init`. Target **Go ≥1.25** (see §11 for the version gotcha).
- **Add every dependency via the Go CLI** — `go get <module>@latest` — to guarantee the latest compatible version and correct checksums. **Never hand-edit versions in `go.mod`.** Run `go mod tidy` after dependency changes.
- Core dependencies (each added with `go get`): `github.com/spf13/cobra` (CLI), `go.mau.fi/whatsmeow` (WhatsApp), the SQLite driver (§11), `google.golang.org/protobuf` (for `proto.String` etc.).

### 4.2 CLI framework & best practices
- Use **`github.com/spf13/cobra`** for command routing, flags, help, and usage. One root command `wa` with subcommands.
- CLI best practices:
  - Every command has a clear `--help`; usage/argument errors exit non-zero (§10).
  - Config precedence: flag > env > default (`--data-dir` > `WA_CLI_HOME` > `~/.wa-cli`).
  - **stdout carries machine-readable results only**; all diagnostics/logs go to **stderr** (and `daemon.log`).
  - Output is **deterministic and stable** (stable JSON key names; stable ordering).
  - **Non-interactive**: never prompt. An AI agent drives the tool; there is no human at the terminal mid-command.
  - Never print stack traces or Go panics to stdout.

### 4.3 Output hygiene (token-frugal for AI agents)
- **No emojis, no ANSI color, no decorative formatting, no spinners/progress bars.** They waste agent tokens and can corrupt JSON parsing.
- **Concise messages.** `message` fields are short, factual strings. No banners, no ASCII art, no verbose prose.
- JSON is **compact** by default (no pretty-printing). A `--pretty` flag may exist but defaults off.

### 4.4 Test-Driven Development (required, always)
- **Write a failing test first, then the implementation, then refactor.** No production code lands without a test that drove it.
- Idiomatic **table-driven tests**. Unit-test all pure logic: recipient normalization / JID parsing, text-and-type extraction, store queries, IPC encode/decode, argument parsing, error mapping.
- **Wrap the whatsmeow client behind a small interface** so the daemon logic is testable with a mock/fake; **tests must never hit real WhatsApp servers**. Integration-test the CLI↔daemon socket round-trip against the fake.
- `go test ./...` must pass. Optimize for meaningful coverage of logic and edge cases, not a coverage percentage.

### 4.5 Robustness, fail-fast, no silent errors
- **Fail fast.** Validate inputs at the boundary and return early. Avoid deep nesting.
- **No silent error swallowing.** Every fallible call's `error` is checked and either handled or wrapped with `fmt.Errorf("context: %w", err)` and propagated. **Blank-identifier discards of errors (`_ = fallibleCall()`) are prohibited.**
- **Enforce with static analysis** (must pass; CI/pre-commit fails otherwise):
  - `go vet ./...`
  - `errcheck ./...` (unchecked errors)
  - `golangci-lint run` (bundles `errcheck`, `govet`, `staticcheck`, `ineffassign`, `unused`, etc.)
- At the CLI boundary, present a clean `{"error","message"}` to the user while logging the full wrapped chain to `daemon.log`.

### 4.6 Concurrency & race safety (single daemon, multiple goroutines)
The daemon is **one process** that concurrently: serves socket connections, runs whatsmeow's event-handler goroutines, and manages the connection. Shared state is touched from multiple goroutines — design for it:
- Guard in-memory daemon state (connected / logged-in / own JID / push name) with a `sync.RWMutex`, or own it in a single goroutine reached via channels. No unsynchronized shared mutable state.
- **SQLite:** use a single shared `*sql.DB` (it is safe for concurrent use and pools connections). Open with `?_foreign_keys=on`; add `_journal_mode=WAL` and `_busy_timeout=5000` to avoid `database is locked` under concurrent read/write. whatsmeow's `sqlstore` and our `messages` writes share the file — serialize writers if lock contention appears.
- Keep the event handler's work short (extract + insert); it must not block the socket server, and vice-versa.
- Prefer passing immutable request/response structs by value across the socket boundary.
- **All tests run with the race detector: `go test -race ./...`** in CI. Fix every reported race.

### 4.7 Required project files (deliverables)
- **`CLAUDE.md`** (repo root) — the project's agent-guidance file, read first by any agent (including the builder). It MUST contain: a one-paragraph project overview (what `wa` is; daemon+CLI architecture); **all standards in this §4** (Go; `go get`-only dependency management; cobra; TDD; no-emoji/color, concise output; fail-fast; no silent error swallowing + the exact `go vet` / `errcheck` / `golangci-lint` commands; race-safety + `go test -race`); the data-dir/paths (§2); the build/test/lint commands (§11); and the whatsmeow Go-version gotcha (§11).
- **`README.md`** (repo root) — human-facing: what the tool does; install/build; quick start (`wa start` → `wa login <phone>` → `wa send` / `wa messages`); a condensed command reference (§5); and a pointer to the fast-follow roadmap (§13). No emojis/color.
- **Recommended:** a `Makefile` (or `Taskfile`) with `build`, `test` (`go test -race ./...`), `lint` (`golangci-lint run`), and `tidy` targets, so the standards are one command away.

---

## 5. Command Reference

Global conventions:
- **Success:** JSON object/array on **stdout**, exit code `0`.
- **Failure:** JSON error object `{"error":"<code>","message":"<human text>"}` on **stderr**, non-zero exit code (see §10).
- **Global flags:** `--data-dir DIR` (default `~/.wa-cli`), `--pretty` (default off; pretty-print JSON).
- Any command that needs the daemon and finds it not running returns error code `daemon_not_running` so the skill knows to run `wa start`.

### `wa start`
Starts the daemon **detached** (background). Idempotent.
- Implementation: re-exec the binary's hidden foreground server subcommand (`wa __daemon__`) with stdout/stderr redirected to `daemon.log`, detached from the terminal (new session). Write `daemon.pid`. Wait until `daemon.sock` accepts a connection (or timeout ~10s) before returning.
- On start, if a session already exists in `store.db` (device has an ID), the daemon calls `Connect()` and comes up logged-in.
- Output: `{"status":"started","pid":12345,"socket":"/Users/x/.wa-cli/daemon.sock"}` or `{"status":"already_running","pid":12345}`.

### `wa stop`
Stops the daemon (graceful: disconnect client, close socket, remove pid file).
- Output: `{"status":"stopped"}` or `{"status":"not_running"}`.

### `wa status`
Reports daemon + connection state.
- Output:
```json
{
  "daemon": "running",
  "connected": true,
  "logged_in": true,
  "jid": "5511999999999:12@s.whatsapp.net",
  "phone": "+5511999999999",
  "push_name": "Vitor"
}
```
- If daemon not running: `{"daemon":"stopped","connected":false,"logged_in":false}` (exit 0 — status is a query, "stopped" is a valid answer).

### `wa login <phone>`
Links this device to the given number using a **pairing code**. Requires the daemon running and **not** already logged in.
- `<phone>`: international number; accept with or without `+`, spaces, or dashes. Normalize to digits only, no leading `0`, must be >6 digits (whatsmeow validates: `ErrPhoneNumberTooShort`, `ErrPhoneNumberIsNotInternational`).
- Behavior: daemon calls `Connect()`, waits for the socket to establish (first QR event or ~1s), then calls `PairPhone(...)` and returns the code.
- Output:
```json
{
  "pairing_code": "ABCD-1234",
  "expires_in_seconds": 160,
  "instructions": "On the phone: WhatsApp -> Settings -> Linked Devices -> Link a device -> 'Link with phone number instead' -> enter this code."
}
```
- Completion is asynchronous: when the user enters the code, the daemon receives `events.PairSuccess`, whatsmeow saves the session, and the client reconnects logged-in. The skill confirms by polling `wa status` until `logged_in: true` (poll a few times over ~30s).
- If already logged in: error `already_logged_in`.

### `wa logout`
Logs out (unlinks the device server-side and deletes the local session), so a fresh `wa login` is possible.
- Output: `{"status":"logged_out"}`.
- Note: the messages table is **not** cleared by logout (keep history) unless `--purge` is passed.

### `wa send <recipient> <text>`
Sends a plain text message. Requires logged-in.
- `<recipient>`: a phone number (normalized like `login`) **or** a full JID (`...@s.whatsapp.net` for a user, `...@g.us` for a group). If it contains `@`, parse as JID; otherwise treat as a user phone number.
- `<text>`: the message body (single argument; the skill quotes it).
- Output:
```json
{
  "id": "3EB0XXXXXXXXXXXXXXXX",
  "chat": "5511999999999@s.whatsapp.net",
  "timestamp": "2026-07-02T14:03:11Z"
}
```
- Errors: `not_logged_in`, `invalid_recipient`, `send_failed` (with message).

### `wa messages [flags]`
Lists received messages from the store. With no flags, it returns **unseen** messages (equivalent to `--unread`), oldest→newest. Returning them marks the matching rows locally "seen".
- Flags:
  - `--chat <recipient>` — filter to one chat (phone or JID).
  - `--unread` — only messages not yet locally seen (default when no other filter is given).
  - `--all` — ignore the seen flag; return regardless.
  - `--since <RFC3339>` — only messages at/after this timestamp.
  - `--limit <N>` — cap results (default 50).
  - `--mark-read` — additionally send a WhatsApp **read receipt** for the returned messages (`MarkRead`). Off by default (reading via the agent should not necessarily notify the sender).
- After a successful read, matching rows get `seen = 1` (local flag) so the next `--unread` call doesn't repeat them.
- Output: JSON array, chronological (oldest→newest) within the selection:
```json
[
  {
    "id": "3EB0...",
    "chat": "5511999999999@s.whatsapp.net",
    "chat_name": "Alice",
    "sender": "5511999999999@s.whatsapp.net",
    "sender_name": "Alice",
    "from_me": false,
    "is_group": false,
    "timestamp": "2026-07-02T14:02:59Z",
    "type": "text",
    "text": "hey, are we still on for tomorrow?"
  },
  {
    "id": "3EB1...",
    "chat": "120363000000000000@g.us",
    "chat_name": "120363000000000000@g.us",
    "sender": "5511888888888@s.whatsapp.net",
    "sender_name": "Bob",
    "from_me": false,
    "is_group": true,
    "timestamp": "2026-07-02T14:03:40Z",
    "type": "image",
    "text": "[image] check this out"
  }
]
```
- `type` is one of: `text`, `image`, `audio`, `video`, `document`, `sticker`, `location`, `contact`, `other`. For non-text types, `text` contains a placeholder plus any caption (e.g. `[image] check this out`).

### `wa chats [flags]`
Lists recent chats derived from the messages table (a convenience view so the agent can see conversations at a glance).
- Flags: `--limit <N>` (default 20).
- Output: JSON array, most-recent activity first:
```json
[
  {
    "jid": "5511999999999@s.whatsapp.net",
    "name": "Alice",
    "is_group": false,
    "last_message_timestamp": "2026-07-02T14:02:59Z",
    "last_message_preview": "hey, are we still on for tomorrow?",
    "unread_count": 1
  }
]
```

---

## 6. Daemon Design

### Startup sequence (`wa __daemon__`, the foreground server)
1. Resolve data dir; ensure it exists.
2. Set up logging to `daemon.log` (whatsmeow `waLog` logger + our logs).
3. Open the SQLite container (whatsmeow `sqlstore`, see §8). Run migrations (`Upgrade`).
4. Get the single device: `container.GetFirstDevice(ctx)` (returns a fresh unsaved device if none exists yet).
5. Create the client: `whatsmeow.NewClient(device, logger)`. Enable auto-reconnect (default on).
6. Register the event handler (§7).
7. If `client.Store.ID != nil` (already paired), call `client.Connect()`.
   If not paired, stay up but disconnected/awaiting `login`.
8. Bind and listen on `daemon.sock`. Accept connections; for each, read one JSON request, dispatch, write one JSON response, close.
9. On `SIGINT`/`SIGTERM` or a `stop` command: disconnect the client, close the socket, remove pid file, exit.

### Request dispatch (socket protocol)
- **Request:** `{"cmd":"send","args":{"recipient":"...","text":"..."}}\n`
- **Response (ok):** `{"ok":true,"data":{...}}\n`
- **Response (err):** `{"ok":false,"error":"<code>","message":"..."}\n`
- Commands routed over the socket: `status`, `login`, `logout`, `send`, `messages`, `chats`, `stop`. (`start` is handled by the CLI process itself: it spawns the daemon; it is not a socket command.)
- The CLI is a thin translator: parse argv → build request JSON → connect to socket → send → read response → print `data` (or `error`) → set exit code.

### Login flow inside the daemon
```
on "login" request with phone P:
  if client.Store.ID != nil: return error already_logged_in
  if !client.IsConnected(): client.Connect()
  wait for first events.QR (or ~1s) so the noise handshake completed
  code, err := client.PairPhone(ctx, P, true, whatsmeow.PairClientChrome, "Chrome (macOS)")
  return { pairing_code: code, expires_in_seconds: 160, instructions: ... }
# Meanwhile, when the user enters the code on their phone:
#   events.PairSuccess fires -> whatsmeow saves the device -> client reconnects logged-in.
#   The handler updates daemon state; `wa status` will report logged_in:true.
```
Notes:
- `clientDisplayName` **must** be formatted exactly as `"Browser (OS)"` — e.g. `"Chrome (macOS)"`. The server rejects malformed names with a 400. Pick a fixed valid value.
- `showPushNotification` = `true` (second arg) shows the standard "linking" notification on the phone.

### Reconnect & resilience
- Rely on whatsmeow's built-in auto-reconnect (`EnableAutoReconnect`, default true). The daemon does not need custom reconnect logic for the MVP.
- Handle `events.LoggedOut` (device removed from the phone side): mark state logged-out; stop trying to reconnect; surface via `wa status` (`logged_in:false`). Do **not** delete the messages table.
- Handle `events.Disconnected`/`events.Connected` to keep `wa status` accurate.

---

## 7. Event Handling (receiving)

Register with `client.AddEventHandler(func(evt any){ ... })` and type-switch. MVP handles:

| Event | Action |
|---|---|
| `*events.Message` | Extract text/type (below), upsert into `messages`. |
| `*events.Connected` | Set `connected=true`. On first connect after login, `client.SendPresence(ctx, types.PresenceAvailable)` is **not** called for MVP (stay "invisible"; note this affects delivery receipts — acceptable for MVP, see §14). |
| `*events.Disconnected` | Set `connected=false`. |
| `*events.LoggedOut` | Set `logged_in=false`, `connected=false`. |
| `*events.PairSuccess` | Log; state becomes logged-in on the following reconnect. |

All other events are ignored in the MVP. Guard the state writes per §4.6.

### Text/type extraction from `*events.Message`
`evt.Info` (`types.MessageInfo`) gives the metadata; `evt.Message` (`*waE2E.Message`) gives content. Extraction logic:
```
m := evt.Message
switch {
case m.GetConversation() != "":
    type="text";     text=m.GetConversation()
case m.GetExtendedTextMessage() != nil:
    type="text";     text=m.GetExtendedTextMessage().GetText()
case m.GetImageMessage() != nil:
    type="image";    text="[image] "  + m.GetImageMessage().GetCaption()
case m.GetVideoMessage() != nil:
    type="video";    text="[video] "  + m.GetVideoMessage().GetCaption()
case m.GetAudioMessage() != nil:
    type="audio";    text="[audio]"   // voice note if GetPTT()
case m.GetDocumentMessage() != nil:
    type="document"; text="[document] " + m.GetDocumentMessage().GetFileName()
case m.GetStickerMessage() != nil:
    type="sticker";  text="[sticker]"
case m.GetLocationMessage() != nil:
    type="location"; text="[location] " + m.GetLocationMessage().GetName()
case m.GetContactMessage() != nil:
    type="contact";  text="[contact] " + m.GetContactMessage().GetDisplayName()
default:
    type="other";    text="[other]"
}
```
Metadata to persist (from `evt.Info`):
- `id` = `evt.Info.ID`
- `chat` = `evt.Info.Chat.String()`
- `sender` = `evt.Info.Sender.String()`
- `sender_name` = `evt.Info.PushName`
- `from_me` = `evt.Info.IsFromMe`
- `is_group` = `evt.Info.IsGroup`
- `timestamp` = `evt.Info.Timestamp` (store as UTC RFC3339 / unix)
- `raw_json` = optional: JSON of the message for debugging/future use.

`chat_name`/`sender_name` for the MVP come from `PushName` (best-effort). A proper contact-name lookup is a fast-follow.

---

## 8. Data Model (SQLite)

Two logical groups in one `store.db`:

1. **whatsmeow session tables** — created/managed entirely by `sqlstore.Container.Upgrade()`. We do not touch these directly. They hold the device identity, Signal keys, sessions, app-state keys, contacts, chat settings, etc.

2. **Our application table(s):**

```sql
CREATE TABLE IF NOT EXISTS messages (
    id            TEXT NOT NULL,          -- WhatsApp message ID
    chat_jid      TEXT NOT NULL,          -- e.g. 5511...@s.whatsapp.net or ...@g.us
    sender_jid    TEXT NOT NULL,
    sender_name   TEXT,                   -- push name (best-effort)
    from_me       INTEGER NOT NULL,       -- 0/1
    is_group      INTEGER NOT NULL,       -- 0/1
    timestamp     INTEGER NOT NULL,       -- unix seconds (UTC)
    type          TEXT NOT NULL,          -- text|image|audio|video|document|sticker|location|contact|other
    text          TEXT,                   -- extracted text or "[type] caption"
    seen          INTEGER NOT NULL DEFAULT 0,  -- local: has the agent read it via `wa messages`
    raw_json      TEXT,                   -- optional raw message dump
    PRIMARY KEY (chat_jid, id)
);
CREATE INDEX IF NOT EXISTS idx_messages_ts   ON messages(timestamp);
CREATE INDEX IF NOT EXISTS idx_messages_seen ON messages(seen);
```

Notes:
- Upsert on `(chat_jid, id)` to dedupe (retries/edits could re-deliver).
- `wa chats` is a `GROUP BY chat_jid` query over `messages` (latest timestamp, latest text, count where `seen=0 AND from_me=0`).
- Keep our tables namespaced/clearly named so they never collide with whatsmeow's.

---

## 9. whatsmeow Integration Reference (for the implementer)

Module: **`go.mau.fi/whatsmeow`**. Key sub-packages: `go.mau.fi/whatsmeow/store/sqlstore`, `go.mau.fi/whatsmeow/types`, `go.mau.fi/whatsmeow/types/events`, `go.mau.fi/whatsmeow/proto/waE2E`, `go.mau.fi/whatsmeow/util/log` (waLog). Text messages also need `google.golang.org/protobuf/proto`. Add all with `go get` (§4.1).

### Store / client bootstrap
```go
import (
    "context"
    "go.mau.fi/whatsmeow"
    "go.mau.fi/whatsmeow/store/sqlstore"
    "go.mau.fi/whatsmeow/types"
    "go.mau.fi/whatsmeow/types/events"
    waE2E "go.mau.fi/whatsmeow/proto/waE2E"
    waLog "go.mau.fi/whatsmeow/util/log"
    "google.golang.org/protobuf/proto"
    _ "github.com/mattn/go-sqlite3"   // registers the "sqlite3" driver (CGO)
)

logger := waLog.Stdout("wa", "INFO", true) // or a file logger to daemon.log
container, err := sqlstore.New(ctx, "sqlite3",
    "file:"+dbPath+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000", logger)
// container.Upgrade(ctx) is run by New(); if using NewWithDB, call Upgrade yourself.

device, err := container.GetFirstDevice(ctx) // fresh NewDevice() if none stored
client := whatsmeow.NewClient(device, logger)
client.AddEventHandler(handleEvent)

if client.Store.ID != nil {
    err = client.Connect()   // already paired -> connect & authenticate
}
```

### Pairing (phone code)
```go
// Preconditions: not paired (client.Store.ID == nil), connected (Connect() called),
// and the initial handshake done (first events.QR seen, or ~1s after Connect()).
code, err := client.PairPhone(ctx, phoneDigits, true,
    whatsmeow.PairClientChrome, "Chrome (macOS)")
// return `code` (format "XXXX-XXXX"); completion arrives as events.PairSuccess.
```
`PairClientType` constants include `PairClientChrome`, `PairClientFirefox`, `PairClientSafari`, `PairClientElectron`, etc. Any valid one works; `clientDisplayName` must match `"Browser (OS)"`.

### Sending text
```go
func recipientToJID(s string) (types.JID, error) {
    if strings.Contains(s, "@") {
        return types.ParseJID(s)                       // full JID (user or group)
    }
    return types.NewJID(normalizeDigits(s), types.DefaultUserServer), nil // phone -> user JID
}

jid, err := recipientToJID(recipient)
resp, err := client.SendMessage(ctx, jid, &waE2E.Message{
    Conversation: proto.String(text),
})
// resp.ID (types.MessageID), resp.Timestamp (time.Time)
```
Routing is automatic by `jid.Server`: `s.whatsapp.net` → DM, `g.us` → group. No extra handling needed for the MVP.

### Receiving (see §7 for extraction)
```go
func handleEvent(evt any) {
    switch v := evt.(type) {
    case *events.Message:     storeMessage(v)     // v.Info, v.Message
    case *events.Connected:   setConnected(true)
    case *events.Disconnected: setConnected(false)
    case *events.LoggedOut:   setLoggedOut()
    case *events.PairSuccess: /* log; reconnect handles the rest */
    }
}
```

### Optional: read receipts (only if `--mark-read`)
```go
// ids: []types.MessageID for one chat/sender; chat & sender JIDs from the stored rows.
err := client.MarkRead(ctx, ids, time.Now(), chatJID, senderJID) // default type = read
```

### Logout
```go
err := client.Logout(ctx) // unlinks server-side and deletes the local device store
```

---

## 10. Errors & Exit Codes

| Exit | Error code | Meaning |
|---|---|---|
| 0 | — | Success (including `status` reporting "stopped") |
| 1 | `generic` | Unclassified failure |
| 2 | `usage` | Bad arguments / missing required arg |
| 3 | `daemon_not_running` | A command needing the daemon found it down → run `wa start` |
| 4 | `not_logged_in` | Needs a linked account → run `wa login` |
| 5 | `already_logged_in` | `login` called while already linked |
| 6 | `invalid_recipient` | Could not parse/normalize the recipient |
| 7 | `send_failed` | whatsmeow returned an error sending |
| 8 | `login_failed` | Pairing could not start (bad phone, etc.) |

Error object shape on stderr: `{"error":"send_failed","message":"context deadline exceeded"}`. Messages are concise (§4.3); the full wrapped error chain is logged to `daemon.log`, never printed to stdout.

---

## 11. Build & Run

- **Go version:** whatsmeow's current `go.mod` requires **Go 1.25** (`toolchain go1.26.x`). A machine on Go 1.23 is **too old** to build against `whatsmeow@latest`. Options:
  1. **Install Go ≥1.25** (recommended) and build against `whatsmeow@latest`.
  2. **Pin whatsmeow** to an older tagged release whose `go.mod` supports the installed Go (still add it via `go get <module>@<version>`, never by hand-editing `go.mod`).
  Flag this at project start; it otherwise fails the first `go build`.
- **SQLite driver:** the whatsmeow example uses `github.com/mattn/go-sqlite3` (CGO — needs a C toolchain). For a pure-Go, CGO-free build (easier cross-compilation), use `modernc.org/sqlite` and register it under the name `sqlite3` (or use the appropriate `dbutil` helper). Pick one; MVP default = `mattn/go-sqlite3` to match upstream examples. Add via `go get`.
- **Single binary** named `wa` with cobra subcommands (`start`, `stop`, `status`, `login`, `logout`, `send`, `messages`, `chats`, and the hidden `__daemon__`).
- **Tooling / standards commands** (also in `CLAUDE.md` and the `Makefile`, §4.7):
  - `go build ./...`
  - `go test -race ./...`
  - `go vet ./...` · `errcheck ./...` · `golangci-lint run`
  - `go mod tidy`
- Suggested layout:
  ```
  cmd/wa/main.go        # cobra root + argv parsing; dispatch to CLI client or daemon
  internal/daemon/      # server: whatsmeow client wrapper, event handler, socket server
  internal/ipc/         # request/response types + socket read/write helpers
  internal/store/       # messages table access
  internal/cli/         # thin client: build request, print response
  internal/wa/          # small interface wrapping whatsmeow.Client (mockable for tests)
  ```

---

## 12. Claude Skill Integration (how the CLI gets used)

The end goal is a Claude skill that wraps `wa`. The skill's operating model:
1. **Ensure daemon:** run `wa status`; if `daemon_not_running`, run `wa start`.
2. **Ensure linked:** if `logged_in:false`, run `wa login <phone>`, relay the `pairing_code` + `instructions` to the human, then poll `wa status` until `logged_in:true`.
3. **Send:** `wa send <recipient> "<text>"` → report the returned `id`/`timestamp`.
4. **Read:** `wa messages --unread` (or `--chat <r>`) → summarize the JSON for the human. Use `--mark-read` only when the human wants the sender to see read receipts.
5. **Overview:** `wa chats` for a conversation list.

Because every command emits JSON and clear exit codes, the skill can branch deterministically on results. (The skill itself is a separate deliverable authored after the CLI exists; this section defines the contract it depends on.)

---

## 13. Fast-Follow Roadmap (post-MVP, not built in v1)

Ordered roughly by likely value:
1. **Media send** (image/audio/video/document/sticker) via `Upload` + building the media `waE2E.Message` struct.
2. **Media download** for received messages (`Download`/`DownloadToFile`), exposed as `wa download <chat> <message-id>`.
3. ~~**QR login** as an alternative to pairing code (`GetQRChannel` + terminal QR rendering).~~ **Done** — `wa login-qr` returns the first QR code rendered as a half-block terminal string in the `qr` field (single-shot; caller polls `wa status` for completion).
4. **Reactions, edits, delete/revoke, replies, @mentions** (`BuildReaction`/`BuildEdit`/`BuildRevoke`, `ContextInfo`).
5. **Contact/user lookup:** `IsOnWhatsApp`, `GetUserInfo`, profile pictures, business profiles; proper contact-name resolution for output.
6. **Group management:** create, info, participants add/remove/promote, invite links.
7. **Presence & typing** send/subscribe; **read-receipt** and **online** behavior toggles.
8. **Chat management** (mute/pin/archive/mark-read/star/block) and **privacy settings**.
9. **Streaming/push** delivery of new messages (e.g. `wa listen` or a socket subscription) instead of polling.
10. **Multi-account** (multiple devices in one daemon; account selector per command).
11. **History-sync backfill** exposure (`events.HistorySync` + `ParseWebMessage`).
12. **Newsletters/channels** and **call events** (reject).

---

## 14. Open Questions / Assumptions to Confirm

These were decided by best-judgment defaults while finalizing the MVP; confirm or override:
- **Login method:** pairing code for v1 (`wa login`); QR login added as a fast-follow (`wa login-qr`, see roadmap item 3). *Rationale: pairing code is simplest and easiest to relay through a Claude chat; QR is offered for users who prefer scanning.*
- **Receive scope:** store all message types with placeholders; no media download in v1.
- **Accounts:** single account only.
- **Presence:** daemon stays "invisible" (does not send `PresenceAvailable`) in v1. Trade-off: default delivery receipts may be sent as `inactive` (no gray ticks) — acceptable for the MVP. Revisit with the presence fast-follow.
- **Data dir:** `~/.wa-cli/`. Confirm you're happy with the location/name.
- **Daemon start model:** `wa start` spawns a detached background process; commands fail fast with `daemon_not_running` rather than auto-spawning. (Alternative: auto-spawn on first command — rejected for the MVP to avoid surprise processes.)
