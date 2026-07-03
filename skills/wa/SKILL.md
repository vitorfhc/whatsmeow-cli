---
name: wa
description: Use when the user wants to send a WhatsApp message, read or check WhatsApp messages, list WhatsApp chats or conversations, wait for a WhatsApp reply, or link/unlink their WhatsApp account from this machine — anything driving the local `wa` CLI (whatsmeow daemon). Not for general questions about the WhatsApp app, protocol, or encryption.
---

# wa — WhatsApp CLI

## Overview

`wa` is a non-interactive CLI backed by a local daemon that holds the
WhatsApp connection. Success prints compact JSON to stdout (exit 0); failure
prints `{"error":"<code>","message":"..."}` to stderr with a non-zero exit
code. Branch on exit codes, not error-string matching. `--pretty` is never
needed. This is the user's real personal account — sends and read receipts
are visible to other people.

## Find the binary

1. `command -v wa` → use it.
2. Else use the whatsmeow-cli checkout this skill ships in (on this machine
   `~/Projects/whatsmeow-cli`): run `./wa`; if missing, `make build`
   (Go ≥ 1.25 + C toolchain). Mention once that installing `wa` on PATH
   avoids this; don't block on it.

## Preflight (once per session)

1. `wa status` — `{"daemon":"stopped"}` with exit 0 means not running (not
   an error): run `wa start`, then re-check.
2. If `logged_in: false` → do the Link account workflow before anything
   else. Never link or logout without the user asking.

## Quick reference

| Task | Command |
|---|---|
| Send text | `wa send <phone\|jid> "<text>"` — exactly 2 args, text is ONE quoted arg |
| New (unseen) messages | `wa messages` (`--limit` default 50) |
| One conversation | `wa messages --chat <phone\|jid> [--all]` |
| History incl. seen | `wa messages --all [--since <RFC3339>] [--limit N]` |
| Recent chats | `wa chats [--limit N]` (default 20) |
| Link account | `wa login <phone>` or `wa login-qr` |
| Unlink / daemon | `wa logout [--purge]`, `wa start`, `wa stop` |

Global flags: `--data-dir DIR` (default `$WA_CLI_HOME` or `~/.wa-cli`).

This table is the complete, current command surface — go straight from it to
execution; `--help` adds nothing.

## Sending

- **When to confirm:** if the user's request already contains the exact
  recipient (phone/JID) and the exact text, send immediately. If you
  resolved a name or drafted/paraphrased the text, show the resolved
  recipient + final text and get a yes first.
- **Name → recipient:** `wa chats --limit 50`, match the `name` field, use
  the returned `jid` verbatim. Zero or multiple matches → ask the user;
  never guess or synthesize a phone number.
- Report the returned `id`/`timestamp` after sending.
- On `send_failed`: check `wa status`, retry at most once (double-send risk).

## Reading

- **Not personal messages:** chats ending `@newsletter` are channel posts
  and `status@broadcast` is status stories. Exclude both from "your
  messages" summaries (mention them separately only if relevant).
- `from_me: true` = the user's own outbound message. `is_group` marks group
  chats. `@lid` and `@s.whatsapp.net` are both valid JIDs — reuse whatever
  `jid`/`chat` value the CLI returned, verbatim.
- `name`/`chat_name`/`sender_name` may be empty (falls back to raw JID),
  especially right after linking, before contact sync completes.
- **Never pass `--mark-read` unless the user explicitly wants read receipts
  sent** — it makes messages show as read (blue ticks) on senders' phones.
- **Wait for a reply:** poll `wa messages --chat <jid>` every 10–30s with a
  timeout agreed with the user; report new messages as they land.

## Linking (needs the user's phone in hand)

- `wa login <phone>` (digits, country code, no `+`) → relay `pairing_code` +
  `instructions` to the user (phone → Settings → Linked Devices → Link a
  device → "Link with phone number instead").
- Or `wa login-qr` → print the `qr` field raw (`wa login-qr | jq -r .qr`)
  for scanning from the same screen. QR expires ~60s; on `login_failed`,
  rerun.
- Then poll `wa status` every few seconds until `logged_in: true` (~30–60s).

## Exit codes → action

| Exit | Code | Action |
|---|---|---|
| 2 | `usage` | Fix the invocation (check `--since` is RFC3339). |
| 3 | `daemon_not_running` | `wa start`, retry once. |
| 4 | `not_logged_in` | Link account workflow. |
| 5 | `already_logged_in` | Skip login, proceed. |
| 6 | `invalid_recipient` | Re-resolve with the user. |
| 7 | `send_failed` | See Sending; don't blind-retry. |
| 8 | `login_failed` | Pairing/QR expired or rejected; rerun login. |
| 1 | `generic` | Read stderr `message`; check `<data-dir>/daemon.log`. |

## Common mistakes

- Unquoted message text — `send` takes exactly 2 args.
- Summarizing `@newsletter` / `status@broadcast` items as personal messages.
- Treating `{"daemon":"stopped"}` (exit 0) as a failure instead of running
  `wa start`.
- Adding `--mark-read` "to be tidy" — it messages state to other people.
- Guessing a number for a contact name that didn't resolve.
- Running `wa stop` or `wa logout` unprompted — stop kills message receipt;
  `logout --purge` also deletes stored messages (warn first).
