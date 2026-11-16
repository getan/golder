package mobilebridge

import "testing"

func TestSelectMobilePrompt(t *testing.T) {
	const old = "You are golder, a coding agent running inside an Android app. Old catalog without AndroidUse."
	const current = "You are golder, a coding agent running inside an Android app. AndroidUse is available."
	for _, tc := range []struct{ name, explicit, saved, want string }{
		{"new session", "", "", current},
		{"refresh old host prompt", "", old, current},
		{"preserve custom prompt", "", "custom instructions", "custom instructions"},
		{"explicit prompt wins", "new custom", old, "new custom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := selectMobilePrompt(tc.explicit, tc.saved, current); got != tc.want {
				t.Fatalf("prompt = %q, want %q", got, tc.want)
			}
		})
	}
}
