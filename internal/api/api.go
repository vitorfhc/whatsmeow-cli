// Package api defines the command argument/result DTOs exchanged between the
// CLI and daemon, plus the error-code-to-exit-code mapping. It has no
// dependency on whatsmeow so the thin CLI can import it freely.
package api

import "time"

// Error codes returned in ipc responses.
const (
	ErrGeneric          = "generic"
	ErrUsage            = "usage"
	ErrDaemonNotRunning = "daemon_not_running"
	ErrNotLoggedIn      = "not_logged_in"
	ErrAlreadyLoggedIn  = "already_logged_in"
	ErrInvalidRecipient = "invalid_recipient"
	ErrSendFailed       = "send_failed"
	ErrLoginFailed      = "login_failed"
)

// ExitCode maps an error code to the process exit code (see spec §10). An
// empty code means success (0); an unrecognized code maps to 1.
func ExitCode(code string) int {
	switch code {
	case "":
		return 0
	case ErrGeneric:
		return 1
	case ErrUsage:
		return 2
	case ErrDaemonNotRunning:
		return 3
	case ErrNotLoggedIn:
		return 4
	case ErrAlreadyLoggedIn:
		return 5
	case ErrInvalidRecipient:
		return 6
	case ErrSendFailed:
		return 7
	case ErrLoginFailed:
		return 8
	default:
		return 1
	}
}

// StatusResult is the payload of `wa status`.
type StatusResult struct {
	Daemon    string `json:"daemon"` // "running" | "stopped"
	Connected bool   `json:"connected"`
	LoggedIn  bool   `json:"logged_in"`
	JID       string `json:"jid,omitempty"`
	Phone     string `json:"phone,omitempty"`
	PushName  string `json:"push_name,omitempty"`
}

// LoginArgs is the payload of `wa login <phone>`.
type LoginArgs struct {
	Phone string `json:"phone"`
}

// LoginResult carries the pairing code back to the user.
type LoginResult struct {
	PairingCode      string `json:"pairing_code"`
	ExpiresInSeconds int    `json:"expires_in_seconds"`
	Instructions     string `json:"instructions"`
}

// LoginQRResult carries a scannable QR code (rendered as a terminal block) for
// linking by phone camera. `wa login-qr` takes no arguments.
type LoginQRResult struct {
	QR               string `json:"qr"`
	ExpiresInSeconds int    `json:"expires_in_seconds"`
	Instructions     string `json:"instructions"`
}

// LogoutArgs is the payload of `wa logout`.
type LogoutArgs struct {
	Purge bool `json:"purge,omitempty"`
}

// LogoutResult is the payload returned by `wa logout`.
type LogoutResult struct {
	Status string `json:"status"`
}

// SendArgs is the payload of `wa send <recipient> <text>`.
type SendArgs struct {
	Recipient string `json:"recipient"`
	Text      string `json:"text"`
}

// SendResult is returned after a successful send.
type SendResult struct {
	ID        string    `json:"id"`
	Chat      string    `json:"chat"`
	Timestamp time.Time `json:"timestamp"`
}

// MessagesArgs is the payload of `wa messages`.
type MessagesArgs struct {
	Chat      string `json:"chat,omitempty"`
	Unread    bool   `json:"unread,omitempty"`
	All       bool   `json:"all,omitempty"`
	SinceUnix int64  `json:"since_unix,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	MarkRead  bool   `json:"mark_read,omitempty"`
}

// ChatsArgs is the payload of `wa chats`.
type ChatsArgs struct {
	Limit int `json:"limit,omitempty"`
}

// StartResult is the payload returned by `wa start`.
type StartResult struct {
	Status string `json:"status"` // "started" | "already_running"
	PID    int    `json:"pid"`
	Socket string `json:"socket"`
}

// StopResult is the payload returned by `wa stop`.
type StopResult struct {
	Status string `json:"status"`
}
