package browserstartup

import "testing"

func TestMissingCrashpadFileIsNotMissingBrowser(t *testing.T) {
	text := "stat /private/profile/Crashpad: No such file or directory\nReceived signal 11"
	if got := FailureKind("starting_chromium", text); got != "browser_crash" {
		t.Fatalf("classified installed browser crash as %q", got)
	}
	if got := FailureKind("starting_chromium", "fork/exec /missing/chromium: no such file or directory"); got != "browser_not_installed" {
		t.Fatalf("missing executable=%q", got)
	}
}
