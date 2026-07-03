package api

import "testing"

func TestExitCode(t *testing.T) {
	cases := map[string]int{
		"":                   0,
		ErrGeneric:           1,
		ErrUsage:             2,
		ErrDaemonNotRunning:  3,
		ErrNotLoggedIn:       4,
		ErrAlreadyLoggedIn:   5,
		ErrInvalidRecipient:  6,
		ErrSendFailed:        7,
		ErrLoginFailed:       8,
		"something_unmapped": 1,
	}
	for code, want := range cases {
		if got := ExitCode(code); got != want {
			t.Errorf("ExitCode(%q) = %d, want %d", code, got, want)
		}
	}
}
