// Package store persists received WhatsApp messages in the shared SQLite
// database and provides the queries the CLI exposes (list, mark-seen, chats).
package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Message is one stored WhatsApp message.
type Message struct {
	ID         string    `json:"id"`
	ChatJID    string    `json:"chat"`
	ChatName   string    `json:"chat_name"`
	SenderJID  string    `json:"sender"`
	SenderName string    `json:"sender_name"`
	FromMe     bool      `json:"from_me"`
	IsGroup    bool      `json:"is_group"`
	Timestamp  time.Time `json:"timestamp"`
	Type       string    `json:"type"`
	Text       string    `json:"text"`
	RawJSON    string    `json:"-"`
}

// Query filters a List call.
type Query struct {
	Chat       string    // filter to one chat JID; empty = all chats
	UnreadOnly bool      // only unseen, inbound messages
	All        bool      // ignore the seen flag entirely
	Since      time.Time // only messages at/after this time (zero = no bound)
	Limit      int       // cap results (<=0 = default)
}

// ChatSummary is one row of the chats overview.
type ChatSummary struct {
	JID           string    `json:"jid"`
	Name          string    `json:"name"`
	IsGroup       bool      `json:"is_group"`
	LastTimestamp time.Time `json:"last_message_timestamp"`
	LastPreview   string    `json:"last_message_preview"`
	UnreadCount   int       `json:"unread_count"`
}

const defaultLimit = 50

// Store owns the messages table on a shared *sql.DB.
type Store struct {
	db *sql.DB
}

// Open ensures the messages schema exists and returns a Store bound to db.
// The db handle is shared with whatsmeow's sqlstore; the caller owns its
// lifecycle.
func Open(db *sql.DB) (*Store, error) {
	const schema = `
CREATE TABLE IF NOT EXISTS messages (
    id            TEXT NOT NULL,
    chat_jid      TEXT NOT NULL,
    chat_name     TEXT,
    sender_jid    TEXT NOT NULL,
    sender_name   TEXT,
    from_me       INTEGER NOT NULL,
    is_group      INTEGER NOT NULL,
    timestamp     INTEGER NOT NULL,
    type          TEXT NOT NULL,
    text          TEXT,
    seen          INTEGER NOT NULL DEFAULT 0,
    raw_json      TEXT,
    PRIMARY KEY (chat_jid, id)
);
CREATE INDEX IF NOT EXISTS idx_messages_ts   ON messages(timestamp);
CREATE INDEX IF NOT EXISTS idx_messages_seen ON messages(seen);`
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("create messages schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Upsert inserts or replaces a message keyed by (chat_jid, id). The seen flag
// is preserved across updates.
func (s *Store) Upsert(ctx context.Context, m Message) error {
	const q = `
INSERT INTO messages (id, chat_jid, chat_name, sender_jid, sender_name, from_me, is_group, timestamp, type, text, raw_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(chat_jid, id) DO UPDATE SET
    chat_name=excluded.chat_name,
    sender_jid=excluded.sender_jid,
    sender_name=excluded.sender_name,
    from_me=excluded.from_me,
    is_group=excluded.is_group,
    timestamp=excluded.timestamp,
    type=excluded.type,
    text=excluded.text,
    raw_json=excluded.raw_json`
	_, err := s.db.ExecContext(ctx, q,
		m.ID, m.ChatJID, m.ChatName, m.SenderJID, m.SenderName,
		boolToInt(m.FromMe), boolToInt(m.IsGroup), m.Timestamp.Unix(), m.Type, m.Text, m.RawJSON)
	if err != nil {
		return fmt.Errorf("upsert message %s/%s: %w", m.ChatJID, m.ID, err)
	}
	return nil
}

// List returns messages matching q, ordered oldest->newest. When Limit is set,
// the most recent matching messages are returned (still oldest->newest).
func (s *Store) List(ctx context.Context, q Query) ([]Message, error) {
	var where []string
	var args []any
	if q.Chat != "" {
		where = append(where, "chat_jid = ?")
		args = append(args, q.Chat)
	}
	if q.UnreadOnly && !q.All {
		where = append(where, "seen = 0", "from_me = 0")
	}
	if !q.Since.IsZero() {
		where = append(where, "timestamp >= ?")
		args = append(args, q.Since.Unix())
	}
	clause := ""
	if len(where) > 0 {
		clause = "WHERE " + strings.Join(where, " AND ")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	// Select the most recent `limit` rows, then re-order ascending for output.
	// Ties on the (second-granularity) timestamp are broken by rowid, which is
	// insertion/arrival order — the WhatsApp message ID is random and would
	// order same-second messages arbitrarily (and drop the true newest at the
	// LIMIT boundary).
	query := fmt.Sprintf(`
SELECT id, chat_jid, chat_name, sender_jid, sender_name, from_me, is_group, timestamp, type, text FROM (
    SELECT id, chat_jid, chat_name, sender_jid, sender_name, from_me, is_group, timestamp, type, text, rowid AS rid
    FROM messages %s ORDER BY timestamp DESC, rowid DESC LIMIT ?
) ORDER BY timestamp ASC, rid ASC`, clause)
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate messages: %w", err)
	}
	return out, nil
}

// MarkSeen sets seen=1 for the given messages (by chat_jid + id).
func (s *Store) MarkSeen(ctx context.Context, msgs []Message) error {
	if len(msgs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, "UPDATE messages SET seen = 1 WHERE chat_jid = ? AND id = ?")
	if err != nil {
		return fmt.Errorf("prepare mark seen: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, m := range msgs {
		if _, err := stmt.ExecContext(ctx, m.ChatJID, m.ID); err != nil {
			return fmt.Errorf("mark seen %s/%s: %w", m.ChatJID, m.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit mark seen: %w", err)
	}
	return nil
}

// Chats returns a per-chat overview ordered by most recent activity.
func (s *Store) Chats(ctx context.Context, limit int) ([]ChatSummary, error) {
	if limit <= 0 {
		limit = 20
	}
	// Pick exactly the latest row per chat (tie-broken by rowid so same-second
	// messages resolve deterministically to the last-arrived one). A GROUP BY
	// with a MAX-timestamp join would leave the previewed row arbitrary on ties.
	const q = `
SELECT m.chat_jid, m.is_group, m.text, m.timestamp, m.from_me, m.sender_name,
    (SELECT COUNT(*) FROM messages u WHERE u.chat_jid = m.chat_jid AND u.seen = 0 AND u.from_me = 0) AS unread
FROM messages m
WHERE m.rowid = (
    SELECT v.rowid FROM messages v WHERE v.chat_jid = m.chat_jid
    ORDER BY v.timestamp DESC, v.rowid DESC LIMIT 1
)
ORDER BY m.timestamp DESC, m.rowid DESC
LIMIT ?`
	rows, err := s.db.QueryContext(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("list chats: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []ChatSummary
	for rows.Next() {
		var (
			c          ChatSummary
			isGroup    int
			ts         int64
			fromMe     int
			senderName sql.NullString
		)
		if err := rows.Scan(&c.JID, &isGroup, &c.LastPreview, &ts, &fromMe, &senderName, &c.UnreadCount); err != nil {
			return nil, fmt.Errorf("scan chat: %w", err)
		}
		c.IsGroup = isGroup != 0
		c.LastTimestamp = time.Unix(ts, 0).UTC()
		c.Name = chatName(c.JID, c.IsGroup, fromMe != 0, senderName.String)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chats: %w", err)
	}
	return out, nil
}

// chatName is a best-effort display name: for a 1:1 chat, the other party's
// push name when known; otherwise the JID.
func chatName(jid string, isGroup, lastFromMe bool, senderName string) string {
	if !isGroup && !lastFromMe && senderName != "" {
		return senderName
	}
	return jid
}

func scanMessage(rows *sql.Rows) (Message, error) {
	var (
		m          Message
		fromMe     int
		isGroup    int
		ts         int64
		chatName   sql.NullString
		senderName sql.NullString
		text       sql.NullString
	)
	if err := rows.Scan(&m.ID, &m.ChatJID, &chatName, &m.SenderJID, &senderName, &fromMe, &isGroup, &ts, &m.Type, &text); err != nil {
		return Message{}, fmt.Errorf("scan message: %w", err)
	}
	m.ChatName = chatName.String
	m.SenderName = senderName.String
	m.Text = text.String
	m.FromMe = fromMe != 0
	m.IsGroup = isGroup != 0
	m.Timestamp = time.Unix(ts, 0).UTC()
	return m, nil
}

// Clear removes all stored messages (used by `logout --purge`).
func (s *Store) Clear(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM messages"); err != nil {
		return fmt.Errorf("clear messages: %w", err)
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
