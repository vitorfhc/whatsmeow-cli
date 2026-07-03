# Design: `whatsapp-cli` agent skill

Date: 2026-07-03
Status: approved

## 1. Goal

A skill that drives the `wa` WhatsApp CLI so any agent session can
send WhatsApp messages, read received messages, list chats, and link the
account on the user's behalf. This is the "separate deliverable" anticipated
by §12 of the MVP design spec
(`2026-07-02-whatsmeow-cli-mvp-design.md`) — the skill replaces what would
otherwise be an MCP server.

## 2. Decisions (from brainstorming)

| Decision | Choice |
|---|---|
| Location | `skills/whatsapp-cli/` at the repo root. Install with `npx --yes skills add vitorfhc/whatsmeow-cli --skill whatsapp-cli`. |
| Scope | Full lifecycle: locate/build the binary, daemon start/stop, account linking (pairing code + QR), send, read, chats, troubleshooting. |
| Binary resolution | `wa` on PATH first; fallback to building from the whatsmeow-cli checkout (`make build`); suggest a permanent install once, never block on it. |
| Send policy | Confirm-unless-explicit: if the human's request already contains the exact recipient and message text, send; otherwise show drafted recipient + text and get a yes first. |
| Read receipts | Never pass `--mark-read` unless the human explicitly wants the sender to see read receipts. |
| Structure | Single `SKILL.md`. No references/ split, no helper scripts — the CLI surface (9 commands, JSON out, documented exit codes) fits one file. |

## 3. Skill content

### 3.1 Frontmatter

- `name: whatsapp-cli`
- `description`: triggers on sending a WhatsApp message, reading/checking
  WhatsApp messages, listing WhatsApp chats/conversations, linking or logging
  in a WhatsApp account, and waiting for a reply. States that it drives the
  local `wa` CLI, so it does not trigger on general WhatsApp questions
  (protocol, encryption, the app itself).

### 3.2 Binary resolution

1. `command -v wa` → use it.
2. Else use the repo checkout (this skill ships inside the whatsmeow-cli
   repo; on this machine `~/Projects/whatsmeow-cli`): reuse `./wa` if built,
   else `make build`. Requires Go ≥ 1.25 + a C toolchain (CGO/SQLite).
3. If built from the repo, mention once that `wa` can be installed onto PATH;
   do not block on it.

### 3.3 Preflight (once per session, before any operation)

1. `wa status` → exit 3 (`daemon_not_running`) → `wa start`, then re-check.
2. `logged_in: false` → run the linking workflow before anything else.

### 3.4 Workflows

- **Send**: resolve recipient — phone/JID used as-is; a contact *name* is
  resolved via `wa chats` (and, if needed, the human confirms the match).
  Apply the send policy. `wa send <recipient> "<text>"` (text is one quoted
  argument). Report the returned `id`/`timestamp`.
- **Read**: default `wa messages --unread`; variants `--chat <r>`,
  `--since <RFC3339>`, `--all`, `--limit N`. Summarize for the human. Never
  `--mark-read` unless explicitly requested.
- **Chats**: `wa chats [--limit N]` for a conversation overview. `name`
  fields carry display names; `jid` stays canonical for addressing.
- **Wait for reply**: poll `wa messages --chat <jid>` on an interval
  (~10–30s), with a bounded timeout agreed with the human; report new
  messages when they land.
- **Link account**: prefer `wa login <phone>` → relay `pairing_code` +
  instructions to the human (phone → Settings → Linked Devices → Link a
  device → "Link with phone number instead"). Alternative `wa login-qr` →
  print the `qr` field raw (e.g. `jq -r .qr`) for scanning. Then poll
  `wa status` until `logged_in: true` (~30–60s; QR expires ~60s — on
  `login_failed`, rerun). Handle `already_logged_in` gracefully.
- **Logout / stop**: only on explicit request. `wa logout` unlinks the
  device; `--purge` additionally deletes stored messages (warn first).
  `wa stop` kills the connection — incoming messages are not received while
  the daemon is down.

### 3.5 Error handling

Exit-code → action table:

| Exit | Code | Action |
|---|---|---|
| 2 | `usage` | Fix the invocation. |
| 3 | `daemon_not_running` | `wa start`, retry once. |
| 4 | `not_logged_in` | Run the linking workflow. |
| 5 | `already_logged_in` | Skip login; proceed. |
| 6 | `invalid_recipient` | Re-resolve with the human. |
| 7 | `send_failed` | Check `wa status`; report; don't blind-retry. |
| 8 | `login_failed` | Pairing/QR expired or rejected; rerun login. |
| 1 | `generic` | Read stderr `message`; check `~/.wa-cli/daemon.log`. |

Errors arrive as `{"error":"<code>","message":"..."}` on stderr; success JSON
on stdout. `daemon.log` in the data dir (`$WA_CLI_HOME` or `~/.wa-cli`) is
the place to look when the daemon itself misbehaves.

### 3.6 Output discipline

Parse stdout JSON only; never rely on `--pretty`; message text passed as a
single argument; no interactive prompts exist in the CLI.

## 4. Out of scope

Media, groups management, reactions, presence (the CLI doesn't support them
yet — see the MVP spec roadmap). Multi-account. Modifying the CLI itself.

## 5. Testing

After writing the skill: verify frontmatter loads (skill appears and
triggers on a natural request), then execute a read-only pass against the
real daemon — binary resolution, `wa status`, `wa chats` — without sending
anything. Send/login flows are exercised only when the human asks for them.
