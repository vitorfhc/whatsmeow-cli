package wa

import (
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

func TestNormalizeDigits(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"+55 11 99999-9999", "5511999999999"},
		{"5511999999999", "5511999999999"},
		{"(11) 99999 9999", "11999999999"},
		{"0011999", "11999"}, // strip leading zeros
		{"+1-202-555-0100", "12025550100"},
	}
	for _, c := range cases {
		if got := NormalizeDigits(c.in); got != c.want {
			t.Errorf("NormalizeDigits(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseRecipientPhone(t *testing.T) {
	jid, err := ParseRecipient("+55 11 99999-9999")
	if err != nil {
		t.Fatalf("ParseRecipient: %v", err)
	}
	if jid.Server != types.DefaultUserServer {
		t.Errorf("Server = %q, want %q", jid.Server, types.DefaultUserServer)
	}
	if jid.User != "5511999999999" {
		t.Errorf("User = %q, want 5511999999999", jid.User)
	}
}

func TestParseRecipientFullJID(t *testing.T) {
	jid, err := ParseRecipient("120363000000000000@g.us")
	if err != nil {
		t.Fatalf("ParseRecipient: %v", err)
	}
	if jid.Server != types.GroupServer {
		t.Errorf("Server = %q, want %q", jid.Server, types.GroupServer)
	}
}

func TestParseRecipientInvalid(t *testing.T) {
	for _, in := range []string{"", "abc", "12"} {
		if _, err := ParseRecipient(in); err == nil {
			t.Errorf("ParseRecipient(%q) expected error, got nil", in)
		}
	}
}

func TestPickContactName(t *testing.T) {
	cases := []struct {
		name     string
		info     types.ContactInfo
		wantName string
		wantOK   bool
	}{
		{
			name:   "all empty",
			info:   types.ContactInfo{},
			wantOK: false,
		},
		{
			name:     "full name wins over everything",
			info:     types.ContactInfo{FullName: "Full", FirstName: "First", BusinessName: "Biz", PushName: "Push"},
			wantName: "Full", wantOK: true,
		},
		{
			name:     "first name when no full name",
			info:     types.ContactInfo{FirstName: "First", BusinessName: "Biz", PushName: "Push"},
			wantName: "First", wantOK: true,
		},
		{
			name:     "business name when no full or first",
			info:     types.ContactInfo{BusinessName: "Biz", PushName: "Push"},
			wantName: "Biz", wantOK: true,
		},
		{
			name:     "push name as last resort",
			info:     types.ContactInfo{PushName: "Push"},
			wantName: "Push", wantOK: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotName, gotOK := PickContactName(c.info)
			if gotName != c.wantName || gotOK != c.wantOK {
				t.Errorf("PickContactName(%+v) = (%q, %v), want (%q, %v)", c.info, gotName, gotOK, c.wantName, c.wantOK)
			}
		})
	}
}

func TestExtractContent(t *testing.T) {
	cases := []struct {
		name     string
		msg      *waE2E.Message
		wantType string
		wantText string
	}{
		{
			name:     "conversation",
			msg:      &waE2E.Message{Conversation: proto.String("hi there")},
			wantType: "text", wantText: "hi there",
		},
		{
			name:     "extended text",
			msg:      &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("hello")}},
			wantType: "text", wantText: "hello",
		},
		{
			name:     "image with caption",
			msg:      &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("look")}},
			wantType: "image", wantText: "[image] look",
		},
		{
			name:     "image no caption",
			msg:      &waE2E.Message{ImageMessage: &waE2E.ImageMessage{}},
			wantType: "image", wantText: "[image]",
		},
		{
			name:     "audio",
			msg:      &waE2E.Message{AudioMessage: &waE2E.AudioMessage{}},
			wantType: "audio", wantText: "[audio]",
		},
		{
			name:     "document",
			msg:      &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("report.pdf")}},
			wantType: "document", wantText: "[document] report.pdf",
		},
		{
			name:     "location",
			msg:      &waE2E.Message{LocationMessage: &waE2E.LocationMessage{Name: proto.String("Park")}},
			wantType: "location", wantText: "[location] Park",
		},
		{
			name:     "contact",
			msg:      &waE2E.Message{ContactMessage: &waE2E.ContactMessage{DisplayName: proto.String("Bob")}},
			wantType: "contact", wantText: "[contact] Bob",
		},
		{
			name:     "unknown",
			msg:      &waE2E.Message{},
			wantType: "other", wantText: "[other]",
		},
		{
			name:     "nil",
			msg:      nil,
			wantType: "other", wantText: "[other]",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotType, gotText := ExtractContent(c.msg)
			if gotType != c.wantType {
				t.Errorf("type = %q, want %q", gotType, c.wantType)
			}
			if gotText != c.wantText {
				t.Errorf("text = %q, want %q", gotText, c.wantText)
			}
		})
	}
}
