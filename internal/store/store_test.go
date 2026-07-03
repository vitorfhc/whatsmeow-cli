package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "store.db")
	db, err := sql.Open("sqlite3", "file:"+dbPath+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err := Open(db)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	return s
}

func msg(id, chat, sender string, fromMe bool, ts time.Time, text string) Message {
	return Message{
		ID: id, ChatJID: chat, SenderJID: sender, SenderName: sender,
		FromMe: fromMe, IsGroup: false, Timestamp: ts, Type: "text", Text: text,
	}
}

func TestUpsertAndList(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0).UTC()

	if err := s.Upsert(ctx, msg("a1", "alice@s.whatsapp.net", "alice@s.whatsapp.net", false, base, "hi")); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := s.List(ctx, Query{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Text != "hi" || got[0].ID != "a1" {
		t.Errorf("row = %+v", got[0])
	}
}

func TestUpsertDedupes(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0).UTC()
	m := msg("a1", "alice@s.whatsapp.net", "alice@s.whatsapp.net", false, base, "hi")
	if err := s.Upsert(ctx, m); err != nil {
		t.Fatal(err)
	}
	m.Text = "edited"
	if err := s.Upsert(ctx, m); err != nil {
		t.Fatal(err)
	}
	got, err := s.List(ctx, Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (dedupe on chat+id)", len(got))
	}
	if got[0].Text != "edited" {
		t.Errorf("text = %q, want edited", got[0].Text)
	}
}

func TestListUnreadExcludesSeenAndFromMe(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0).UTC()
	_ = s.Upsert(ctx, msg("in1", "alice@s.whatsapp.net", "alice@s.whatsapp.net", false, base, "unread"))
	_ = s.Upsert(ctx, msg("me1", "alice@s.whatsapp.net", "me@s.whatsapp.net", true, base.Add(time.Second), "mine"))

	got, err := s.List(ctx, Query{UnreadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "in1" {
		t.Fatalf("unread = %+v, want only in1", got)
	}
}

func TestMarkSeen(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0).UTC()
	m := msg("in1", "alice@s.whatsapp.net", "alice@s.whatsapp.net", false, base, "unread")
	_ = s.Upsert(ctx, m)

	if err := s.MarkSeen(ctx, []Message{m}); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	got, err := s.List(ctx, Query{UnreadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("after MarkSeen unread = %d, want 0", len(got))
	}
}

func TestListByChat(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0).UTC()
	_ = s.Upsert(ctx, msg("a", "alice@s.whatsapp.net", "alice@s.whatsapp.net", false, base, "from alice"))
	_ = s.Upsert(ctx, msg("b", "bob@s.whatsapp.net", "bob@s.whatsapp.net", false, base, "from bob"))

	got, err := s.List(ctx, Query{Chat: "bob@s.whatsapp.net", All: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Text != "from bob" {
		t.Fatalf("by chat = %+v", got)
	}
}

func TestListSinceAndOrder(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0).UTC()
	_ = s.Upsert(ctx, msg("old", "c@s.whatsapp.net", "c@s.whatsapp.net", false, base, "old"))
	_ = s.Upsert(ctx, msg("new", "c@s.whatsapp.net", "c@s.whatsapp.net", false, base.Add(time.Hour), "new"))

	got, err := s.List(ctx, Query{All: true, Since: base.Add(30 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Text != "new" {
		t.Fatalf("since = %+v, want only new", got)
	}
}

func TestListLimitReturnsMostRecentAscending(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0).UTC()
	for i, txt := range []string{"m0", "m1", "m2"} {
		_ = s.Upsert(ctx, msg(txt, "c@s.whatsapp.net", "c@s.whatsapp.net", false, base.Add(time.Duration(i)*time.Minute), txt))
	}
	got, err := s.List(ctx, Query{All: true, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	// Most recent two (m1, m2), presented oldest->newest.
	if got[0].Text != "m1" || got[1].Text != "m2" {
		t.Errorf("order = [%s %s], want [m1 m2]", got[0].Text, got[1].Text)
	}
}

func TestListLimitKeepsNewestArrivedOnSameSecond(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	ts := time.Unix(1_700_000_000, 0).UTC() // all same second
	// Arrival order zzz, yyy, aaa — aaa is newest but has the lowest lexical id.
	for _, id := range []string{"zzz", "yyy", "aaa"} {
		_ = s.Upsert(ctx, msg(id, "c@s.whatsapp.net", "c@s.whatsapp.net", false, ts, id))
	}
	got, err := s.List(ctx, Query{All: true, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	// The newest-arrived message must be retained (not dropped by the id-based
	// tie-break) and presented last.
	if got[1].Text != "aaa" {
		t.Errorf("order = [%s %s], want newest 'aaa' last", got[0].Text, got[1].Text)
	}
}

func TestChatsPrefersLastArrivedOnSameSecond(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	ts := time.Unix(1_700_000_000, 0).UTC()
	_ = s.Upsert(ctx, msg("first", "c@s.whatsapp.net", "c@s.whatsapp.net", false, ts, "first"))
	_ = s.Upsert(ctx, msg("second", "c@s.whatsapp.net", "c@s.whatsapp.net", false, ts, "second"))

	got, err := s.Chats(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	// Deterministic: the last-arrived message is the preview.
	if got[0].LastPreview != "second" {
		t.Errorf("preview = %q, want 'second'", got[0].LastPreview)
	}
}

func TestClear(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0).UTC()
	_ = s.Upsert(ctx, msg("a", "alice@s.whatsapp.net", "alice@s.whatsapp.net", false, base, "hi"))
	if err := s.Clear(ctx); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	got, err := s.List(ctx, Query{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("after Clear len = %d, want 0", len(got))
	}
}

func TestChats(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0).UTC()
	_ = s.Upsert(ctx, msg("a1", "alice@s.whatsapp.net", "alice@s.whatsapp.net", false, base, "first"))
	_ = s.Upsert(ctx, msg("a2", "alice@s.whatsapp.net", "alice@s.whatsapp.net", false, base.Add(time.Minute), "latest"))
	_ = s.Upsert(ctx, msg("b1", "bob@s.whatsapp.net", "bob@s.whatsapp.net", false, base.Add(2*time.Minute), "hey"))

	got, err := s.Chats(ctx, 10)
	if err != nil {
		t.Fatalf("Chats: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("chats = %d, want 2", len(got))
	}
	// bob is most recent.
	if got[0].JID != "bob@s.whatsapp.net" {
		t.Errorf("first chat = %q, want bob", got[0].JID)
	}
	// alice's preview is the latest message, unread count 2.
	var alice *ChatSummary
	for i := range got {
		if got[i].JID == "alice@s.whatsapp.net" {
			alice = &got[i]
		}
	}
	if alice == nil {
		t.Fatal("alice chat missing")
	}
	if alice.LastPreview != "latest" {
		t.Errorf("alice preview = %q, want latest", alice.LastPreview)
	}
	if alice.UnreadCount != 2 {
		t.Errorf("alice unread = %d, want 2", alice.UnreadCount)
	}
}
