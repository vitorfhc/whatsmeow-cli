package daemon

import (
	"context"
	"testing"

	"github.com/busfactor/whatsmeow-cli/internal/api"
	"github.com/busfactor/whatsmeow-cli/internal/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func userJID(user string) string {
	return types.NewJID(user, types.DefaultUserServer).String()
}

func groupJID(id string) string {
	return types.NewJID(id, types.GroupServer).String()
}

func TestResolverContactName(t *testing.T) {
	alice := userJID("5511777777777")
	fc := &fakeClient{
		connected:    true,
		contactNames: map[string]string{alice: "Alice Cooper"},
	}
	r := newNameResolver(fc, newGroupCache())

	if got := r.contactName(context.Background(), alice, "fallback"); got != "Alice Cooper" {
		t.Errorf("contactName(known) = %q, want Alice Cooper", got)
	}
	if got := r.contactName(context.Background(), userJID("5511000000000"), "fallback"); got != "fallback" {
		t.Errorf("contactName(unknown) = %q, want fallback", got)
	}
}

func TestResolverContactMemoDedupes(t *testing.T) {
	alice := userJID("5511777777777")
	fc := &fakeClient{
		connected:    true,
		contactNames: map[string]string{alice: "Alice"},
	}
	r := newNameResolver(fc, newGroupCache())

	for i := 0; i < 3; i++ {
		r.contactName(context.Background(), alice, "fallback")
	}
	if fc.contactCalls != 1 {
		t.Errorf("contactCalls = %d, want 1 (per-request memo should dedupe)", fc.contactCalls)
	}
}

func TestResolverGroupName(t *testing.T) {
	g := groupJID("120363000000000000")
	fc := &fakeClient{
		connected:  true,
		groupNames: map[string]string{g: "Family"},
	}
	r := newNameResolver(fc, newGroupCache())

	if got := r.chatName(context.Background(), g, true, g); got != "Family" {
		t.Errorf("chatName(group) = %q, want Family", got)
	}
	// Unknown group falls back to the provided value (the JID).
	unknown := groupJID("120363999999999999")
	if got := r.chatName(context.Background(), unknown, true, unknown); got != unknown {
		t.Errorf("chatName(unknown group) = %q, want fallback %q", got, unknown)
	}
}

func TestResolverGroupCacheDedupes(t *testing.T) {
	g := groupJID("120363000000000000")
	fc := &fakeClient{
		connected:  true,
		groupNames: map[string]string{g: "Family"},
	}
	cache := newGroupCache()

	// A fresh resolver per "request" still shares the daemon-level cache, so the
	// network lookup happens only once across requests.
	for i := 0; i < 3; i++ {
		r := newNameResolver(fc, cache)
		r.chatName(context.Background(), g, true, "fallback")
	}
	if fc.groupInfoCalls != 1 {
		t.Errorf("groupInfoCalls = %d, want 1 (group cache should dedupe across requests)", fc.groupInfoCalls)
	}
}

func TestResolverGroupOfflineSkipsNetwork(t *testing.T) {
	g := groupJID("120363000000000000")
	fc := &fakeClient{
		connected:  false, // offline
		groupNames: map[string]string{g: "Family"},
	}
	r := newNameResolver(fc, newGroupCache())

	if got := r.chatName(context.Background(), g, true, "fallback"); got != "fallback" {
		t.Errorf("chatName(offline, uncached group) = %q, want fallback", got)
	}
	if fc.groupInfoCalls != 0 {
		t.Errorf("groupInfoCalls = %d, want 0 (must not hit network while offline)", fc.groupInfoCalls)
	}
}

func TestResolverGroupCacheHitWhileOffline(t *testing.T) {
	g := groupJID("120363000000000000")
	cache := newGroupCache()
	cache.setFresh(g, "Family", cache.generation()) // previously resolved while online

	fc := &fakeClient{connected: false}
	r := newNameResolver(fc, cache)

	if got := r.chatName(context.Background(), g, true, "fallback"); got != "Family" {
		t.Errorf("chatName(offline, cached group) = %q, want Family", got)
	}
	if fc.groupInfoCalls != 0 {
		t.Errorf("groupInfoCalls = %d, want 0 (cache hit must not hit network)", fc.groupInfoCalls)
	}
}

func TestResolverBadJIDReturnsFallback(t *testing.T) {
	fc := &fakeClient{connected: true}
	r := newNameResolver(fc, newGroupCache())

	if got := r.contactName(context.Background(), "not-a-jid", "fallback"); got != "fallback" {
		t.Errorf("contactName(bad jid) = %q, want fallback", got)
	}
}

func TestResolverNormalizesDeviceJID(t *testing.T) {
	// whatsmeow stores incoming sender JIDs with a device component (e.g. from a
	// linked device), but keys the contact store by the ADless identity. The
	// resolver must normalize before lookup or address-book names never surface.
	adless := userJID("5511777777777") // 5511777777777@s.whatsapp.net
	fc := &fakeClient{connected: true, contactNames: map[string]string{adless: "Alice Real"}}
	r := newNameResolver(fc, newGroupCache())

	withDevice := "5511777777777:23@s.whatsapp.net"
	if got := r.contactName(context.Background(), withDevice, "fallback"); got != "Alice Real" {
		t.Errorf("contactName(device jid) = %q, want Alice Real (must normalize to non-AD)", got)
	}
}

func TestResolverGroupMissMemoizedPerRequest(t *testing.T) {
	// An unresolvable group (empty subject / left group / transient error) must
	// be fetched at most once per request, not once per row.
	g := groupJID("120363000000000000")
	fc := &fakeClient{connected: true} // groupNames empty -> GroupName returns ok=false
	r := newNameResolver(fc, newGroupCache())

	for i := 0; i < 5; i++ {
		r.chatName(context.Background(), g, true, "fallback")
	}
	if fc.groupInfoCalls != 1 {
		t.Errorf("groupInfoCalls = %d, want 1 (unresolved group must be memoized per request)", fc.groupInfoCalls)
	}
}

func TestGroupCacheSetFreshCachesWhenUnchanged(t *testing.T) {
	c := newGroupCache()
	gen := c.generation()
	c.setFresh("g", "Family", gen)
	if name, ok := c.get("g"); !ok || name != "Family" {
		t.Errorf("get = (%q,%v), want (Family,true)", name, ok)
	}
}

func TestGroupCacheRenameRejectsStaleFetch(t *testing.T) {
	// A rename that lands while a fetch is in flight must win: the in-flight
	// fetch (which captured the old generation) must not overwrite it.
	c := newGroupCache()
	gen := c.generation()
	c.rename("g", "New") // rename lands during the fetch, bumping the generation
	c.setFresh("g", "Old", gen)
	if name, ok := c.get("g"); !ok || name != "New" {
		t.Errorf("get = (%q,%v), want (New,true): stale fetch must not clobber a rename", name, ok)
	}
}

func TestGroupCacheRenameToEmptyDropsEntry(t *testing.T) {
	c := newGroupCache()
	c.setFresh("g", "Family", c.generation())
	c.rename("g", "") // rename to empty subject clears the entry
	if _, ok := c.get("g"); ok {
		t.Errorf("get(g) still present after rename to empty; want dropped")
	}
}

func TestChatsEnrichesContactName(t *testing.T) {
	alice := userJID("5511777777777")
	fc := &fakeClient{paired: true, connected: true, contactNames: map[string]string{alice: "Alice Real"}}
	d := newTestDaemon(t, fc)
	fc.emit(incomingMessage("m1", "5511777777777", "hi"))

	resp := handle(t, d, "chats", api.ChatsArgs{})
	var chats []store.ChatSummary
	if err := resp.Bind(&chats); err != nil {
		t.Fatal(err)
	}
	if len(chats) != 1 {
		t.Fatalf("chats = %d, want 1", len(chats))
	}
	if chats[0].Name != "Alice Real" {
		t.Errorf("chat name = %q, want Alice Real (contact store should override push name)", chats[0].Name)
	}
	if chats[0].JID != alice {
		t.Errorf("chat jid = %q, want %q (JID must be preserved for addressing)", chats[0].JID, alice)
	}
}

func TestChatsEnrichesGroupName(t *testing.T) {
	g := groupJID("120363000000000000")
	fc := &fakeClient{paired: true, connected: true, groupNames: map[string]string{g: "The Squad"}}
	d := newTestDaemon(t, fc)
	fc.emit(groupMessage("g1", "120363000000000000", "5511777777777", "Alice", "hello group"))

	resp := handle(t, d, "chats", api.ChatsArgs{})
	var chats []store.ChatSummary
	if err := resp.Bind(&chats); err != nil {
		t.Fatal(err)
	}
	if len(chats) != 1 {
		t.Fatalf("chats = %d, want 1", len(chats))
	}
	if chats[0].Name != "The Squad" {
		t.Errorf("group chat name = %q, want The Squad", chats[0].Name)
	}
}

func TestChatsFallsBackToPushNameWhenNoContact(t *testing.T) {
	fc := &fakeClient{paired: true, connected: true} // no contact data
	d := newTestDaemon(t, fc)
	fc.emit(incomingMessage("m1", "5511777777777", "hi"))

	resp := handle(t, d, "chats", api.ChatsArgs{})
	var chats []store.ChatSummary
	if err := resp.Bind(&chats); err != nil {
		t.Fatal(err)
	}
	if chats[0].Name != "Tester" {
		t.Errorf("chat name = %q, want Tester (fallback to stored push name)", chats[0].Name)
	}
}

func TestMessagesEnrichesSenderAndGroupName(t *testing.T) {
	g := groupJID("120363000000000000")
	sender := userJID("5511777777777")
	fc := &fakeClient{
		paired: true, connected: true,
		contactNames: map[string]string{sender: "Bob Real"},
		groupNames:   map[string]string{g: "The Squad"},
	}
	d := newTestDaemon(t, fc)
	fc.emit(groupMessage("g1", "120363000000000000", "5511777777777", "Alice", "hello group"))

	resp := handle(t, d, "messages", api.MessagesArgs{All: true})
	var msgs []store.Message
	if err := resp.Bind(&msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("len = %d, want 1", len(msgs))
	}
	if msgs[0].SenderName != "Bob Real" {
		t.Errorf("sender_name = %q, want Bob Real (contact store should override push name)", msgs[0].SenderName)
	}
	if msgs[0].ChatName != "The Squad" {
		t.Errorf("chat_name = %q, want The Squad (group subject)", msgs[0].ChatName)
	}
	if msgs[0].SenderJID != sender {
		t.Errorf("sender jid = %q, want %q (JID must be preserved)", msgs[0].SenderJID, sender)
	}
}

func TestGroupRenameInvalidatesCache(t *testing.T) {
	gid := "120363000000000000"
	g := groupJID(gid)
	fc := &fakeClient{paired: true, connected: true, groupNames: map[string]string{g: "Old Name"}}
	d := newTestDaemon(t, fc)
	fc.emit(groupMessage("g1", gid, "5511777777777", "Alice", "hi"))

	// First read caches the subject.
	resp := handle(t, d, "chats", api.ChatsArgs{})
	var chats []store.ChatSummary
	if err := resp.Bind(&chats); err != nil {
		t.Fatal(err)
	}
	if chats[0].Name != "Old Name" {
		t.Fatalf("first read name = %q, want Old Name", chats[0].Name)
	}

	// The group is renamed on the server; whatsmeow delivers a GroupInfo event.
	fc.mu.Lock()
	fc.groupNames[g] = "New Name"
	fc.mu.Unlock()
	fc.emit(&events.GroupInfo{
		JID:  types.NewJID(gid, types.GroupServer),
		Name: &types.GroupName{Name: "New Name"},
	})

	callsAfterRename := fc.groupInfoCalls

	// The next read must reflect the new subject, served from the event without
	// a fresh network fetch (the event already carried the new name).
	resp2 := handle(t, d, "chats", api.ChatsArgs{})
	var chats2 []store.ChatSummary
	if err := resp2.Bind(&chats2); err != nil {
		t.Fatal(err)
	}
	if chats2[0].Name != "New Name" {
		t.Errorf("after rename name = %q, want New Name (event should update the cache)", chats2[0].Name)
	}
	if fc.groupInfoCalls != callsAfterRename {
		t.Errorf("group re-fetched after rename (%d -> %d); the event should populate the cache directly", callsAfterRename, fc.groupInfoCalls)
	}
}
