// Package daemon implements the long-lived server: it holds the whatsmeow
// client, records incoming messages, and answers CLI requests over a Unix
// socket.
package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/busfactor/whatsmeow-cli/internal/api"
	"github.com/busfactor/whatsmeow-cli/internal/ipc"
	"github.com/busfactor/whatsmeow-cli/internal/store"
	"github.com/busfactor/whatsmeow-cli/internal/wa"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

const (
	pairingExpirySeconds = 160
	loginInstructions    = "On the phone: WhatsApp -> Settings -> Linked Devices -> Link a device -> 'Link with phone number instead' -> enter this code."
)

// Daemon answers CLI requests and stores incoming messages. Its only shared
// mutable state is the store (a concurrency-safe *sql.DB) and the whatsmeow
// client (safe for concurrent use); no additional locking is required here.
type Daemon struct {
	client wa.Client
	store  *store.Store
	log    *slog.Logger
}

// New builds a Daemon and registers its event handler on the client.
func New(client wa.Client, st *store.Store, logger *slog.Logger) *Daemon {
	d := &Daemon{client: client, store: st, log: logger}
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
		if err := d.client.Connect(ctx); err != nil {
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
