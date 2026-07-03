package wa

import (
	"context"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

const (
	pairClientDisplayName = "Chrome (macOS)"
	pairConnectTimeout    = 5 * time.Second
	pairSettleDelay       = time.Second
)

// Client is the minimal WhatsApp surface the daemon depends on. It is an
// interface so the daemon can be tested with a fake instead of a live
// connection.
type Client interface {
	AddEventHandler(handler func(evt any))
	Connect(ctx context.Context) error
	Disconnect()
	IsConnected() bool
	Logout(ctx context.Context) error
	PairPhone(ctx context.Context, phone string) (code string, err error)
	SendText(ctx context.Context, to types.JID, text string) (id string, ts time.Time, err error)
	MarkRead(ctx context.Context, ids []types.MessageID, chat, sender types.JID) error
	OwnID() (jid types.JID, paired bool)
	PushName() string
}

// RealClient adapts *whatsmeow.Client to the Client interface.
type RealClient struct {
	cli *whatsmeow.Client
}

// NewRealClient wraps a constructed whatsmeow client.
func NewRealClient(cli *whatsmeow.Client) *RealClient { return &RealClient{cli: cli} }

var _ Client = (*RealClient)(nil)

// AddEventHandler registers an event handler on the underlying client.
func (r *RealClient) AddEventHandler(h func(evt any)) { r.cli.AddEventHandler(h) }

// Connect opens the websocket and authenticates if a session exists.
func (r *RealClient) Connect(ctx context.Context) error {
	if err := r.cli.ConnectContext(ctx); err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	return nil
}

// Disconnect closes the websocket.
func (r *RealClient) Disconnect() { r.cli.Disconnect() }

// IsConnected reports websocket connectivity.
func (r *RealClient) IsConnected() bool { return r.cli.IsConnected() }

// Logout unlinks the device and clears the local session.
func (r *RealClient) Logout(ctx context.Context) error {
	if err := r.cli.Logout(ctx); err != nil {
		return fmt.Errorf("logout: %w", err)
	}
	return nil
}

// PairPhone waits for the handshake to settle, then requests a pairing code.
func (r *RealClient) PairPhone(ctx context.Context, phone string) (string, error) {
	deadline := time.Now().Add(pairConnectTimeout)
	for !r.cli.IsConnected() && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("wait for connection: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	if !r.cli.IsConnected() {
		return "", fmt.Errorf("not connected after %s", pairConnectTimeout)
	}
	select {
	case <-ctx.Done():
		return "", fmt.Errorf("wait for handshake: %w", ctx.Err())
	case <-time.After(pairSettleDelay):
	}
	code, err := r.cli.PairPhone(ctx, phone, true, whatsmeow.PairClientChrome, pairClientDisplayName)
	if err != nil {
		return "", fmt.Errorf("pair phone: %w", err)
	}
	return code, nil
}

// SendText sends a plain text message and returns the server ID and timestamp.
func (r *RealClient) SendText(ctx context.Context, to types.JID, text string) (string, time.Time, error) {
	resp, err := r.cli.SendMessage(ctx, to, &waE2E.Message{Conversation: proto.String(text)})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("send message: %w", err)
	}
	return resp.ID, resp.Timestamp, nil
}

// MarkRead sends a read receipt for the given message IDs.
func (r *RealClient) MarkRead(ctx context.Context, ids []types.MessageID, chat, sender types.JID) error {
	if err := r.cli.MarkRead(ctx, ids, time.Now(), chat, sender); err != nil {
		return fmt.Errorf("mark read: %w", err)
	}
	return nil
}

// OwnID returns the linked account's JID and whether the device is paired.
func (r *RealClient) OwnID() (types.JID, bool) {
	if r.cli.Store == nil || r.cli.Store.ID == nil {
		return types.JID{}, false
	}
	return *r.cli.Store.ID, true
}

// PushName returns the account's push name, if known.
func (r *RealClient) PushName() string {
	if r.cli.Store == nil {
		return ""
	}
	return r.cli.Store.PushName
}
