// Package daemon implements the long-lived server: it holds the whatsmeow
// client, records incoming messages, and answers CLI requests over a Unix
// socket.
package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/vitorfhc/whatsmeow-cli/internal/api"
	"github.com/vitorfhc/whatsmeow-cli/internal/ipc"
	"github.com/vitorfhc/whatsmeow-cli/internal/store"
	"github.com/vitorfhc/whatsmeow-cli/internal/wa"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

const (
	pairingExpirySeconds = 160
	loginInstructions    = "On the phone: WhatsApp -> Settings -> Linked Devices -> Link a device -> 'Link with phone number instead' -> enter this code."

	qrFirstCodeTimeout      = 15 * time.Second
	qrExpiryFallbackSeconds = 60
	qrLoginInstructions     = "On the phone: WhatsApp -> Settings -> Linked Devices -> Link a device -> scan this QR code."
)

// Daemon answers CLI requests and stores incoming messages. Its shared mutable
// state is the store (a concurrency-safe *sql.DB), the whatsmeow client (safe
// for concurrent use), and the qrActive flag guarded by qrMu. baseCtx is a
// daemon-lifetime context used for background work (the QR login session) that
// must outlive a single request; it is canceled by Close.
type Daemon struct {
	client  wa.Client
	store   *store.Store
	log     *slog.Logger
	baseCtx context.Context
	cancel  context.CancelFunc

	// groups caches resolved group subjects across requests (GetGroupInfo is a
	// network call). It is safe for concurrent use.
	groups *groupCache

	qrMu     sync.Mutex
	qrActive bool
}

// New builds a Daemon and registers its event handler on the client.
func New(client wa.Client, st *store.Store, logger *slog.Logger) *Daemon {
	ctx, cancel := context.WithCancel(context.Background())
	d := &Daemon{client: client, store: st, log: logger, baseCtx: ctx, cancel: cancel, groups: newGroupCache()}
	client.AddEventHandler(d.handleEvent)
	return d
}

// Handle dispatches a single request to the matching command handler.
func (d *Daemon) Handle(ctx context.Context, req ipc.Request) ipc.Response {
	switch req.Cmd {
	case "status":
		return d.status()
	case "login":
		var args api.LoginArgs
		if err := req.Bind(&args); err != nil {
			return ipc.Err(api.ErrUsage, err.Error())
		}
		return d.login(ctx, args)
	case "login-qr":
		return d.loginQR()
	case "logout":
		var args api.LogoutArgs
		if err := req.Bind(&args); err != nil {
			return ipc.Err(api.ErrUsage, err.Error())
		}
		return d.logout(ctx, args)
	case "send":
		var args api.SendArgs
		if err := req.Bind(&args); err != nil {
			return ipc.Err(api.ErrUsage, err.Error())
		}
		return d.send(ctx, args)
	case "messages":
		var args api.MessagesArgs
		if err := req.Bind(&args); err != nil {
			return ipc.Err(api.ErrUsage, err.Error())
		}
		return d.messages(ctx, args)
	case "chats":
		var args api.ChatsArgs
		if err := req.Bind(&args); err != nil {
			return ipc.Err(api.ErrUsage, err.Error())
		}
		return d.chats(ctx, args)
	default:
		return ipc.Err(api.ErrUsage, fmt.Sprintf("unknown command %q", req.Cmd))
	}
}

func (d *Daemon) status() ipc.Response {
	jid, paired := d.client.OwnID()
	res := api.StatusResult{
		Daemon:    "running",
		Connected: d.client.IsConnected(),
		LoggedIn:  paired,
	}
	if paired {
		res.JID = jid.String()
		res.Phone = "+" + jid.User
		res.PushName = d.client.PushName()
	}
	return ok(res)
}

func (d *Daemon) login(ctx context.Context, args api.LoginArgs) ipc.Response {
	if _, paired := d.client.OwnID(); paired {
		return ipc.Err(api.ErrAlreadyLoggedIn, "device is already linked")
	}
	phone, err := wa.NormalizePhone(args.Phone)
	if err != nil {
		return ipc.Err(api.ErrLoginFailed, err.Error())
	}
	if !d.client.IsConnected() {
		// Connect on the daemon-lifetime context, not the per-request ctx:
		// whatsmeow ties the connection's lifetime to the ctx passed to Connect,
		// so a request-scoped ctx would tear the socket down (and stop event
		// dispatch) as soon as this handler returns — before the user enters the
		// pairing code and PairSuccess can fire.
		if err := d.client.Connect(d.baseCtx); err != nil {
			return ipc.Err(api.ErrLoginFailed, err.Error())
		}
	}
	code, err := d.client.PairPhone(ctx, phone)
	if err != nil {
		return ipc.Err(api.ErrLoginFailed, err.Error())
	}
	return ok(api.LoginResult{
		PairingCode:      code,
		ExpiresInSeconds: pairingExpirySeconds,
		Instructions:     loginInstructions,
	})
}

// loginQR starts a QR login and returns the first scannable code. whatsmeow
// requires GetQRChannel *before* Connect. Connect runs on the daemon-lifetime
// baseCtx because whatsmeow ties the connection's lifetime to that ctx — a
// per-request ctx would tear the socket down once this response is written,
// before the user can scan. The QR channel runs on a per-session context so an
// aborted attempt can be torn down without affecting the connection; on success
// the session is left running in a background goroutine so the scan can complete
// asynchronously via the shared *events.PairSuccess handler, and the caller
// polls `wa status` for completion.
func (d *Daemon) loginQR() ipc.Response {
	if _, paired := d.client.OwnID(); paired {
		return ipc.Err(api.ErrAlreadyLoggedIn, "device is already linked")
	}
	// whatsmeow's GetQRChannel requires the client to be disconnected; surface a
	// clear error instead of its internal "must be called before connecting".
	if d.client.IsConnected() {
		return ipc.Err(api.ErrLoginFailed, "client is already connected; run 'wa logout' or restart the daemon before qr login")
	}

	d.qrMu.Lock()
	if d.qrActive {
		d.qrMu.Unlock()
		return ipc.Err(api.ErrLoginFailed, "qr login already in progress; wait for it to expire or restart the daemon")
	}
	d.qrActive = true
	d.qrMu.Unlock()

	// sessionCtx bounds only the QR channel (whatsmeow's QR emitter), not the
	// connection. Canceling it closes the channel and unwinds the translation
	// goroutine even on an expected Disconnect, which whatsmeow does not use to
	// close the channel.
	sessionCtx, sessionCancel := context.WithCancel(d.baseCtx)

	// abort tears down a failed attempt: cancel the QR session (closing the
	// channel), drop the connection, and release the guard.
	abort := func(msg string) ipc.Response {
		sessionCancel()
		d.client.Disconnect()
		d.clearQR()
		return ipc.Err(api.ErrLoginFailed, msg)
	}

	ch, err := d.client.GetQRChannel(sessionCtx)
	if err != nil {
		sessionCancel()
		d.clearQR()
		return ipc.Err(api.ErrLoginFailed, err.Error())
	}
	if err := d.client.Connect(d.baseCtx); err != nil {
		return abort(err.Error())
	}

	timer := time.NewTimer(qrFirstCodeTimeout)
	defer timer.Stop()
	select {
	case item, more := <-ch:
		if !more {
			return abort("qr channel closed before a code was issued")
		}
		if item.Event != "code" {
			return abort("unexpected qr event: " + item.Event)
		}
		rendered, err := wa.RenderQR(item.Code)
		if err != nil {
			return abort(err.Error())
		}
		// Keep consuming the channel so the session stays alive until the user
		// scans (PairSuccess) or it times out.
		go d.drainQR(ch, sessionCancel)
		expires := int(item.Timeout.Seconds())
		if expires <= 0 {
			expires = qrExpiryFallbackSeconds
		}
		return ok(api.LoginQRResult{
			QR:               rendered,
			ExpiresInSeconds: expires,
			Instructions:     qrLoginInstructions,
		})
	case <-timer.C:
		return abort("timed out waiting for qr code")
	}
}

// drainQR consumes the remaining QR events after the first code was returned,
// logging the terminal outcome, then cancels the session context (releasing the
// translation goroutine) and the qrActive guard.
func (d *Daemon) drainQR(ch <-chan wa.QRItem, sessionCancel context.CancelFunc) {
	for item := range ch {
		switch item.Event {
		case "success":
			d.log.Info("qr pair success")
		case "timeout":
			d.log.Info("qr login timed out")
		case "error":
			d.log.Warn("qr login error", "err", item.Err)
		}
	}
	sessionCancel()
	d.clearQR()
}

func (d *Daemon) clearQR() {
	d.qrMu.Lock()
	d.qrActive = false
	d.qrMu.Unlock()
}

func (d *Daemon) logout(ctx context.Context, args api.LogoutArgs) ipc.Response {
	logoutErr := d.client.Logout(ctx)
	// Purge is honored even if the remote unlink failed (e.g. already
	// unlinked): the user explicitly asked for local data to be deleted.
	if args.Purge {
		if err := d.store.Clear(ctx); err != nil {
			return ipc.Err(api.ErrGeneric, err.Error())
		}
	}
	if logoutErr != nil {
		return ipc.Err(api.ErrGeneric, logoutErr.Error())
	}
	return ok(api.LogoutResult{Status: "logged_out"})
}

func (d *Daemon) send(ctx context.Context, args api.SendArgs) ipc.Response {
	if _, paired := d.client.OwnID(); !paired {
		return ipc.Err(api.ErrNotLoggedIn, "no linked account; run: wa login <phone>")
	}
	jid, err := wa.ParseRecipient(args.Recipient)
	if err != nil {
		return ipc.Err(api.ErrInvalidRecipient, err.Error())
	}
	id, ts, err := d.client.SendText(ctx, jid, args.Text)
	if err != nil {
		return ipc.Err(api.ErrSendFailed, err.Error())
	}
	return ok(api.SendResult{ID: id, Chat: jid.String(), Timestamp: ts})
}

func (d *Daemon) messages(ctx context.Context, args api.MessagesArgs) ipc.Response {
	q := store.Query{
		UnreadOnly: args.Unread,
		All:        args.All,
		Limit:      args.Limit,
	}
	if args.Chat != "" {
		jid, err := wa.ParseRecipient(args.Chat)
		if err != nil {
			return ipc.Err(api.ErrInvalidRecipient, err.Error())
		}
		q.Chat = jid.String()
	}
	if args.SinceUnix != 0 {
		q.Since = time.Unix(args.SinceUnix, 0).UTC()
	}
	// Default to unread when the caller gave no explicit selector.
	if !args.All && !args.Unread && q.Chat == "" && args.SinceUnix == 0 {
		q.UnreadOnly = true
	}
	msgs, err := d.store.List(ctx, q)
	if err != nil {
		return ipc.Err(api.ErrGeneric, err.Error())
	}
	// `--all` is a non-consuming browse of everything, so it must not clear
	// unread state; every other read marks the returned rows seen.
	if !args.All {
		if err := d.store.MarkSeen(ctx, msgs); err != nil {
			d.log.Error("mark seen", "err", err)
		}
	}
	if args.MarkRead {
		d.sendReadReceipts(ctx, msgs)
	}
	if len(msgs) > 0 {
		r := newNameResolver(d.client, d.groups)
		for i := range msgs {
			msgs[i].ChatName = r.chatName(ctx, msgs[i].ChatJID, msgs[i].IsGroup, msgs[i].ChatName)
			msgs[i].SenderName = r.contactName(ctx, msgs[i].SenderJID, msgs[i].SenderName)
		}
	}
	if msgs == nil {
		msgs = []store.Message{}
	}
	return ok(msgs)
}

func (d *Daemon) chats(ctx context.Context, args api.ChatsArgs) ipc.Response {
	chats, err := d.store.Chats(ctx, args.Limit)
	if err != nil {
		return ipc.Err(api.ErrGeneric, err.Error())
	}
	if len(chats) > 0 {
		r := newNameResolver(d.client, d.groups)
		for i := range chats {
			chats[i].Name = r.chatName(ctx, chats[i].JID, chats[i].IsGroup, chats[i].Name)
		}
	}
	if chats == nil {
		chats = []store.ChatSummary{}
	}
	return ok(chats)
}

// sendReadReceipts groups inbound messages by (chat, sender) and sends read
// receipts. Failures are logged, not surfaced to the caller.
func (d *Daemon) sendReadReceipts(ctx context.Context, msgs []store.Message) {
	type key struct{ chat, sender string }
	groups := map[key][]types.MessageID{}
	for _, m := range msgs {
		if m.FromMe {
			continue
		}
		k := key{m.ChatJID, m.SenderJID}
		groups[k] = append(groups[k], types.MessageID(m.ID))
	}
	for k, ids := range groups {
		chat, err := types.ParseJID(k.chat)
		if err != nil {
			d.log.Error("parse chat jid for receipt", "jid", k.chat, "err", err)
			continue
		}
		sender, err := types.ParseJID(k.sender)
		if err != nil {
			d.log.Error("parse sender jid for receipt", "jid", k.sender, "err", err)
			continue
		}
		if err := d.client.MarkRead(ctx, ids, chat, sender); err != nil {
			d.log.Error("mark read", "chat", k.chat, "err", err)
		}
	}
}

func (d *Daemon) handleEvent(evt any) {
	switch v := evt.(type) {
	case *events.Message:
		d.storeMessage(v)
	case *events.Connected:
		d.log.Info("connected")
	case *events.Disconnected:
		d.log.Info("disconnected")
	case *events.LoggedOut:
		d.log.Warn("logged out", "reason", v.Reason.String())
	case *events.PairSuccess:
		d.log.Info("pair success", "jid", v.ID.String())
	case *events.GroupInfo:
		// A rename event already carries the new subject; apply it directly so
		// the next read serves it without another fetch (and so it wins over any
		// in-flight fetch of the old name).
		if v.Name != nil {
			d.groups.rename(v.JID.String(), v.Name.Name)
		}
	}
}

func (d *Daemon) storeMessage(v *events.Message) {
	msgType, text := wa.ExtractContent(v.Message)
	// PushName is the sender's name, so it is only a valid chat name for an
	// inbound 1:1 chat. For groups and outbound messages we have no name yet.
	chatName := ""
	if !v.Info.IsGroup && !v.Info.IsFromMe {
		chatName = v.Info.PushName
	}
	m := store.Message{
		ID:         v.Info.ID,
		ChatJID:    v.Info.Chat.String(),
		ChatName:   chatName,
		SenderJID:  v.Info.Sender.String(),
		SenderName: v.Info.PushName,
		FromMe:     v.Info.IsFromMe,
		IsGroup:    v.Info.IsGroup,
		Timestamp:  v.Info.Timestamp,
		Type:       msgType,
		Text:       text,
	}
	if err := d.store.Upsert(context.Background(), m); err != nil {
		d.log.Error("store message", "id", m.ID, "err", err)
	}
}

func ok(data any) ipc.Response {
	return ipc.MustOK(data)
}
