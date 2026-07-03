package daemon

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/vitorfhc/whatsmeow-cli/internal/api"
	"github.com/vitorfhc/whatsmeow-cli/internal/ipc"
	"github.com/vitorfhc/whatsmeow-cli/internal/store"
	"github.com/vitorfhc/whatsmeow-cli/internal/wa"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// fakeClient is an in-memory wa.Client used to drive the daemon without a live
// WhatsApp connection. Its mutating methods are guarded so it is safe to use
// from concurrent requests in the server tests.
type fakeClient struct {
	mu              sync.Mutex
	handler         func(evt any)
	connected       bool
	paired          bool
	ownJID          types.JID
	pushName        string
	pairCode        string
	pairErr         error
	qrItems         []wa.QRItem
	qrErr           error
	qrChannelCalled bool
	qrBeforeConnect bool
	connectCalled   bool
	pairPhoneArg    string
	sentID          string
	sentTs          time.Time
	sendErr         error
	sentTo          types.JID
	sentText        string
	logoutErr       error
	logoutCalled    bool
	markReadIDs     []types.MessageID
	contactNames    map[string]string // jid.String() -> resolved contact name
	groupNames      map[string]string // jid.String() -> group subject
	contactCalls    int
	groupInfoCalls  int
}

func (f *fakeClient) AddEventHandler(h func(evt any)) { f.handler = h }
func (f *fakeClient) Connect(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connectCalled = true
	f.connected = true
	return nil
}
func (f *fakeClient) Disconnect() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connected = false
}
func (f *fakeClient) IsConnected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connected
}
func (f *fakeClient) OwnID() (types.JID, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ownJID, f.paired
}
func (f *fakeClient) PushName() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pushName
}
func (f *fakeClient) Logout(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logoutCalled = true
	f.paired = false
	return f.logoutErr
}
func (f *fakeClient) PairPhone(_ context.Context, phone string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pairPhoneArg = phone
	if f.pairErr != nil {
		return "", f.pairErr
	}
	return f.pairCode, nil
}
func (f *fakeClient) GetQRChannel(context.Context) (<-chan wa.QRItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.qrChannelCalled = true
	// Record the whatsmeow-mandated ordering: GetQRChannel must run before Connect.
	f.qrBeforeConnect = !f.connectCalled
	if f.qrErr != nil {
		return nil, f.qrErr
	}
	ch := make(chan wa.QRItem, len(f.qrItems))
	for _, it := range f.qrItems {
		ch <- it
	}
	close(ch)
	return ch, nil
}
func (f *fakeClient) SendText(_ context.Context, to types.JID, text string) (string, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sentTo = to
	f.sentText = text
	if f.sendErr != nil {
		return "", time.Time{}, f.sendErr
	}
	return f.sentID, f.sentTs, nil
}
func (f *fakeClient) MarkRead(_ context.Context, ids []types.MessageID, _, _ types.JID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markReadIDs = append(f.markReadIDs, ids...)
	return nil
}
func (f *fakeClient) ContactName(_ context.Context, jid types.JID) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.contactCalls++
	name, ok := f.contactNames[jid.String()]
	return name, ok
}
func (f *fakeClient) GroupName(_ context.Context, jid types.JID) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.groupInfoCalls++
	name, ok := f.groupNames[jid.String()]
	return name, ok
}
func (f *fakeClient) emit(evt any) {
	if f.handler != nil {
		f.handler(evt)
	}
}

func newTestDaemon(t *testing.T, fc *fakeClient) *Daemon {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "store.db")
	db, err := sql.Open("sqlite3", "file:"+dbPath+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st, err := store.Open(db)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(fc, st, logger)
}

func handle(t *testing.T, d *Daemon, cmd string, args any) ipc.Response {
	t.Helper()
	req, err := ipc.NewRequest(cmd, args)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	return d.Handle(context.Background(), req)
}

func incomingMessage(id, chat, text string) *events.Message {
	jid := types.NewJID(chat, types.DefaultUserServer)
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: jid, Sender: jid, IsFromMe: false, IsGroup: false},
			ID:            id,
			PushName:      "Tester",
			Timestamp:     time.Unix(1_700_000_000, 0).UTC(),
		},
		Message: &waE2E.Message{Conversation: proto.String(text)},
	}
}

func groupMessage(id, group, sender, pushName, text string) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     types.NewJID(group, types.GroupServer),
				Sender:   types.NewJID(sender, types.DefaultUserServer),
				IsGroup:  true,
				IsFromMe: false,
			},
			ID:        id,
			PushName:  pushName,
			Timestamp: time.Unix(1_700_000_000, 0).UTC(),
		},
		Message: &waE2E.Message{Conversation: proto.String(text)},
	}
}

func TestStatusNotLoggedIn(t *testing.T) {
	d := newTestDaemon(t, &fakeClient{})
	resp := handle(t, d, "status", nil)
	if !resp.OK {
		t.Fatalf("status err: %+v", resp)
	}
	var s api.StatusResult
	if err := resp.Bind(&s); err != nil {
		t.Fatal(err)
	}
	if s.Daemon != "running" || s.LoggedIn || s.Connected {
		t.Errorf("status = %+v", s)
	}
}

func TestStatusLoggedIn(t *testing.T) {
	fc := &fakeClient{
		connected: true, paired: true,
		ownJID:   types.NewJID("5511999999999", types.DefaultUserServer),
		pushName: "Vitor",
	}
	d := newTestDaemon(t, fc)
	resp := handle(t, d, "status", nil)
	var s api.StatusResult
	if err := resp.Bind(&s); err != nil {
		t.Fatal(err)
	}
	if !s.LoggedIn || !s.Connected {
		t.Errorf("expected logged in & connected: %+v", s)
	}
	if s.Phone != "+5511999999999" || s.PushName != "Vitor" {
		t.Errorf("status = %+v", s)
	}
}

func TestSendNotLoggedIn(t *testing.T) {
	d := newTestDaemon(t, &fakeClient{paired: false})
	resp := handle(t, d, "send", api.SendArgs{Recipient: "5511999999999", Text: "hi"})
	if resp.OK || resp.Error != api.ErrNotLoggedIn {
		t.Fatalf("resp = %+v, want not_logged_in", resp)
	}
}

func TestSendSuccess(t *testing.T) {
	ts := time.Unix(1_700_000_500, 0).UTC()
	fc := &fakeClient{paired: true, sentID: "3EB0ABC", sentTs: ts}
	d := newTestDaemon(t, fc)
	resp := handle(t, d, "send", api.SendArgs{Recipient: "+55 11 99999-9999", Text: "hello"})
	if !resp.OK {
		t.Fatalf("send err: %+v", resp)
	}
	var r api.SendResult
	if err := resp.Bind(&r); err != nil {
		t.Fatal(err)
	}
	if r.ID != "3EB0ABC" {
		t.Errorf("id = %q", r.ID)
	}
	if r.Chat != "5511999999999@s.whatsapp.net" {
		t.Errorf("chat = %q", r.Chat)
	}
	if fc.sentText != "hello" || fc.sentTo.User != "5511999999999" {
		t.Errorf("client got to=%s text=%q", fc.sentTo, fc.sentText)
	}
}

func TestSendInvalidRecipient(t *testing.T) {
	d := newTestDaemon(t, &fakeClient{paired: true})
	resp := handle(t, d, "send", api.SendArgs{Recipient: "abc", Text: "hi"})
	if resp.OK || resp.Error != api.ErrInvalidRecipient {
		t.Fatalf("resp = %+v, want invalid_recipient", resp)
	}
}

func TestSendFailure(t *testing.T) {
	fc := &fakeClient{paired: true, sendErr: io.ErrUnexpectedEOF}
	d := newTestDaemon(t, fc)
	resp := handle(t, d, "send", api.SendArgs{Recipient: "5511999999999", Text: "hi"})
	if resp.OK || resp.Error != api.ErrSendFailed {
		t.Fatalf("resp = %+v, want send_failed", resp)
	}
}

func TestLoginReturnsCode(t *testing.T) {
	fc := &fakeClient{paired: false, pairCode: "ABCD-1234"}
	d := newTestDaemon(t, fc)
	resp := handle(t, d, "login", api.LoginArgs{Phone: "+55 11 99999-9999"})
	if !resp.OK {
		t.Fatalf("login err: %+v", resp)
	}
	var r api.LoginResult
	if err := resp.Bind(&r); err != nil {
		t.Fatal(err)
	}
	if r.PairingCode != "ABCD-1234" || r.ExpiresInSeconds != 160 {
		t.Errorf("result = %+v", r)
	}
	if !fc.connectCalled {
		t.Error("expected Connect to be called")
	}
	if fc.pairPhoneArg != "5511999999999" {
		t.Errorf("PairPhone got %q, want normalized digits", fc.pairPhoneArg)
	}
}

func TestLoginAlreadyLoggedIn(t *testing.T) {
	d := newTestDaemon(t, &fakeClient{paired: true})
	resp := handle(t, d, "login", api.LoginArgs{Phone: "5511999999999"})
	if resp.OK || resp.Error != api.ErrAlreadyLoggedIn {
		t.Fatalf("resp = %+v, want already_logged_in", resp)
	}
}

func TestLoginFailure(t *testing.T) {
	fc := &fakeClient{paired: false, pairErr: io.ErrUnexpectedEOF}
	d := newTestDaemon(t, fc)
	resp := handle(t, d, "login", api.LoginArgs{Phone: "5511999999999"})
	if resp.OK || resp.Error != api.ErrLoginFailed {
		t.Fatalf("resp = %+v, want login_failed", resp)
	}
}

func TestLoginQRReturnsCode(t *testing.T) {
	// Timeout is deliberately not 60s (== qrExpiryFallbackSeconds) so the
	// assertion distinguishes a real echo of item.Timeout from the fallback.
	fc := &fakeClient{paired: false, qrItems: []wa.QRItem{
		{Event: "code", Code: "2@ABC,DEF,GHI", Timeout: 45 * time.Second},
		{Event: "success"},
	}}
	d := newTestDaemon(t, fc)
	resp := handle(t, d, "login-qr", nil)
	if !resp.OK {
		t.Fatalf("login-qr err: %+v", resp)
	}
	var r api.LoginQRResult
	if err := resp.Bind(&r); err != nil {
		t.Fatal(err)
	}
	if r.QR == "" {
		t.Error("expected a rendered QR block")
	}
	if r.ExpiresInSeconds != 45 {
		t.Errorf("ExpiresInSeconds = %d, want 45 (echo of item.Timeout)", r.ExpiresInSeconds)
	}
	if r.Instructions == "" {
		t.Error("expected instructions")
	}
	if !fc.qrChannelCalled {
		t.Error("expected GetQRChannel to be called")
	}
	if !fc.connectCalled {
		t.Error("expected Connect to be called")
	}
	if !fc.qrBeforeConnect {
		t.Error("GetQRChannel must be called before Connect (whatsmeow requirement)")
	}
}

func TestLoginQRAlreadyLoggedIn(t *testing.T) {
	d := newTestDaemon(t, &fakeClient{paired: true})
	resp := handle(t, d, "login-qr", nil)
	if resp.OK || resp.Error != api.ErrAlreadyLoggedIn {
		t.Fatalf("resp = %+v, want already_logged_in", resp)
	}
}

func TestLoginQRAlreadyConnected(t *testing.T) {
	d := newTestDaemon(t, &fakeClient{paired: false, connected: true})
	resp := handle(t, d, "login-qr", nil)
	if resp.OK || resp.Error != api.ErrLoginFailed {
		t.Fatalf("resp = %+v, want login_failed (already connected)", resp)
	}
}

func TestLoginQRAlreadyInProgress(t *testing.T) {
	d := newTestDaemon(t, &fakeClient{paired: false})
	// Simulate a live QR session already holding the guard.
	d.qrMu.Lock()
	d.qrActive = true
	d.qrMu.Unlock()
	resp := handle(t, d, "login-qr", nil)
	if resp.OK || resp.Error != api.ErrLoginFailed {
		t.Fatalf("resp = %+v, want login_failed (in progress)", resp)
	}
}

func TestLoginQRChannelError(t *testing.T) {
	fc := &fakeClient{paired: false, qrErr: io.ErrUnexpectedEOF}
	d := newTestDaemon(t, fc)
	resp := handle(t, d, "login-qr", nil)
	if resp.OK || resp.Error != api.ErrLoginFailed {
		t.Fatalf("resp = %+v, want login_failed", resp)
	}
}

func TestLoginQRUnexpectedFirstEvent(t *testing.T) {
	// First event is a terminal error rather than a "code".
	fc := &fakeClient{paired: false, qrItems: []wa.QRItem{
		{Event: "error", Err: io.ErrUnexpectedEOF},
	}}
	d := newTestDaemon(t, fc)
	resp := handle(t, d, "login-qr", nil)
	if resp.OK || resp.Error != api.ErrLoginFailed {
		t.Fatalf("resp = %+v, want login_failed (unexpected event)", resp)
	}
}

func TestLoginQRNoCode(t *testing.T) {
	// Channel closes before any "code" item is emitted.
	fc := &fakeClient{paired: false, qrItems: nil}
	d := newTestDaemon(t, fc)
	resp := handle(t, d, "login-qr", nil)
	if resp.OK || resp.Error != api.ErrLoginFailed {
		t.Fatalf("resp = %+v, want login_failed", resp)
	}
}

func TestEventStoresMessageAndMarksSeenOnRead(t *testing.T) {
	fc := &fakeClient{paired: true}
	d := newTestDaemon(t, fc)
	fc.emit(incomingMessage("m1", "5511777777777", "hey there"))

	resp := handle(t, d, "messages", api.MessagesArgs{})
	if !resp.OK {
		t.Fatalf("messages err: %+v", resp)
	}
	var msgs []store.Message
	if err := resp.Bind(&msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Text != "hey there" {
		t.Fatalf("messages = %+v", msgs)
	}

	// Reading marked it seen; a second unread read returns nothing.
	resp2 := handle(t, d, "messages", api.MessagesArgs{})
	var msgs2 []store.Message
	if err := resp2.Bind(&msgs2); err != nil {
		t.Fatal(err)
	}
	if len(msgs2) != 0 {
		t.Errorf("second read = %d, want 0 (already seen)", len(msgs2))
	}
}

func TestMessagesMarkReadSendsReceipt(t *testing.T) {
	fc := &fakeClient{paired: true}
	d := newTestDaemon(t, fc)
	fc.emit(incomingMessage("m1", "5511777777777", "hey"))

	resp := handle(t, d, "messages", api.MessagesArgs{MarkRead: true})
	if !resp.OK {
		t.Fatalf("messages err: %+v", resp)
	}
	if len(fc.markReadIDs) != 1 || string(fc.markReadIDs[0]) != "m1" {
		t.Errorf("markRead ids = %v, want [m1]", fc.markReadIDs)
	}
}

func TestChats(t *testing.T) {
	fc := &fakeClient{paired: true}
	d := newTestDaemon(t, fc)
	fc.emit(incomingMessage("a", "5511777777777", "from a"))
	fc.emit(incomingMessage("b", "5511888888888", "from b"))

	resp := handle(t, d, "chats", api.ChatsArgs{})
	if !resp.OK {
		t.Fatalf("chats err: %+v", resp)
	}
	var chats []store.ChatSummary
	if err := resp.Bind(&chats); err != nil {
		t.Fatal(err)
	}
	if len(chats) != 2 {
		t.Errorf("chats = %d, want 2", len(chats))
	}
}

func TestUnknownCommand(t *testing.T) {
	d := newTestDaemon(t, &fakeClient{})
	resp := handle(t, d, "bogus", nil)
	if resp.OK || resp.Error != api.ErrUsage {
		t.Fatalf("resp = %+v, want usage", resp)
	}
}

func TestLogout(t *testing.T) {
	fc := &fakeClient{paired: true}
	d := newTestDaemon(t, fc)
	resp := handle(t, d, "logout", api.LogoutArgs{})
	if !resp.OK {
		t.Fatalf("logout err: %+v", resp)
	}
	if !fc.logoutCalled {
		t.Error("expected Logout to be called")
	}
}

func TestLogoutPurgeClearsEvenWhenLogoutErrors(t *testing.T) {
	fc := &fakeClient{paired: true, logoutErr: io.ErrUnexpectedEOF}
	d := newTestDaemon(t, fc)
	fc.emit(incomingMessage("m1", "5511777777777", "hi"))

	resp := handle(t, d, "logout", api.LogoutArgs{Purge: true})
	if resp.OK {
		t.Fatalf("expected the logout error to surface, got ok")
	}
	// Despite the logout error, the explicit purge must have cleared local data.
	got := handle(t, d, "messages", api.MessagesArgs{All: true})
	var msgs []store.Message
	if err := got.Bind(&msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Errorf("purge did not clear store: %d messages remain", len(msgs))
	}
}

func TestMessagesAllDoesNotMarkSeen(t *testing.T) {
	fc := &fakeClient{paired: true}
	d := newTestDaemon(t, fc)
	fc.emit(incomingMessage("m1", "5511777777777", "hi"))

	// Browsing everything must not consume unread state.
	handle(t, d, "messages", api.MessagesArgs{All: true})

	resp := handle(t, d, "messages", api.MessagesArgs{Unread: true})
	var msgs []store.Message
	if err := resp.Bind(&msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Errorf("--all marked messages seen; unread=%d, want 1", len(msgs))
	}
}

func TestLoginRejectsTooShortPhone(t *testing.T) {
	fc := &fakeClient{pairCode: "ABCD-1234"}
	d := newTestDaemon(t, fc)
	resp := handle(t, d, "login", api.LoginArgs{Phone: "12"})
	if resp.OK || resp.Error != api.ErrLoginFailed {
		t.Fatalf("resp = %+v, want login_failed", resp)
	}
	if fc.pairPhoneArg != "" {
		t.Errorf("PairPhone should not be called for a too-short number, got %q", fc.pairPhoneArg)
	}
}

func TestGroupMessageHasNoChatName(t *testing.T) {
	// Offline with no contact/group data: enrichment must fall back to stored
	// values (empty group chat_name, sender push name), never blocking on a
	// network lookup.
	fc := &fakeClient{paired: true}
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
	if msgs[0].ChatName != "" {
		t.Errorf("group chat_name = %q, want empty (sender name is not the group name)", msgs[0].ChatName)
	}
	if msgs[0].SenderName != "Alice" {
		t.Errorf("sender_name = %q, want Alice", msgs[0].SenderName)
	}
}
