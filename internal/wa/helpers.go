// Package wa contains WhatsApp-facing logic: pure helpers for recipient and
// message handling, plus a small interface wrapping the whatsmeow client so
// the daemon can be tested without a live connection.
package wa

import (
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

// minPhoneDigits is the minimum number of digits a phone recipient must have.
// whatsmeow itself requires more than 6 digits for pairing.
const minPhoneDigits = 7

// NormalizeDigits reduces a user-typed phone number to digits only and strips
// leading zeros, producing the international form WhatsApp expects
// (e.g. "+55 11 99999-9999" -> "5511999999999").
func NormalizeDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return strings.TrimLeft(b.String(), "0")
}

// NormalizePhone normalizes a phone number to digits and validates its length.
func NormalizePhone(s string) (string, error) {
	digits := NormalizeDigits(s)
	if len(digits) < minPhoneDigits {
		return "", fmt.Errorf("invalid phone number %q", s)
	}
	return digits, nil
}

// ParseRecipient converts a CLI recipient argument into a JID. A value
// containing "@" is parsed as a full JID (user or group); otherwise it is
// treated as a phone number and normalized.
func ParseRecipient(s string) (types.JID, error) {
	if strings.Contains(s, "@") {
		jid, err := types.ParseJID(s)
		if err != nil {
			return types.JID{}, fmt.Errorf("parse jid %q: %w", s, err)
		}
		return jid, nil
	}
	digits, err := NormalizePhone(s)
	if err != nil {
		return types.JID{}, err
	}
	return types.NewJID(digits, types.DefaultUserServer), nil
}

// ExtractContent returns a coarse message type and a display text for a
// received message. Non-text messages yield a typed placeholder plus any
// caption or label (e.g. "[image] look"). No media is downloaded.
func ExtractContent(m *waE2E.Message) (msgType, text string) {
	if m == nil {
		return "other", "[other]"
	}
	switch {
	case m.GetConversation() != "":
		return "text", m.GetConversation()
	case m.GetExtendedTextMessage() != nil:
		return "text", m.GetExtendedTextMessage().GetText()
	case m.GetImageMessage() != nil:
		return "image", placeholder("image", m.GetImageMessage().GetCaption())
	case m.GetVideoMessage() != nil:
		return "video", placeholder("video", m.GetVideoMessage().GetCaption())
	case m.GetAudioMessage() != nil:
		return "audio", placeholder("audio", "")
	case m.GetDocumentMessage() != nil:
		return "document", placeholder("document", m.GetDocumentMessage().GetFileName())
	case m.GetStickerMessage() != nil:
		return "sticker", placeholder("sticker", "")
	case m.GetLocationMessage() != nil:
		return "location", placeholder("location", m.GetLocationMessage().GetName())
	case m.GetContactMessage() != nil:
		return "contact", placeholder("contact", m.GetContactMessage().GetDisplayName())
	default:
		return "other", placeholder("other", "")
	}
}

// placeholder builds "[kind]" optionally followed by a label.
func placeholder(kind, label string) string {
	base := "[" + kind + "]"
	if label != "" {
		return base + " " + label
	}
	return base
}
