package setup

import (
	"strings"
	"testing"
)

func TestAuthorizationOriginBoundary(t *testing.T) {
	origins := []string{"https://accounts.feishu.cn"}
	if err := AuthorizationURL("https://accounts.feishu.cn/device?user_code=x", origins); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"http://accounts.feishu.cn/device", "file:///secret", "javascript:alert(1)", "https://accounts.feishu.cn.evil.example/device", "https://user@accounts.feishu.cn/device", "https://127.0.0.1/device", "https://localhost/device", "https://accounts.feishu.cn/device#fragment"} {
		if err := AuthorizationURL(raw, origins); err == nil {
			t.Fatalf("allowed %q", raw)
		}
	}
}

func TestAuthorizationUserCodeIsBoundedAndPrintable(t *testing.T) {
	for _, code := range []string{"ABCD-1234", "ABCD 1234"} {
		if !AuthorizationCode(code) {
			t.Fatalf("rejected user code %q", code)
		}
	}
	for _, code := range []string{"", "   ", "ABC\n123", strings.Repeat("A", 65), "<secret>"} {
		if AuthorizationCode(code) {
			t.Fatalf("accepted invalid user code %q", code)
		}
	}
}

func TestFailureEventUsesClosedSafeVocabulary(t *testing.T) {
	event, err := SanitizeFailure(Event{
		Type: "failure", Phase: PhaseVerifyAuthorization, Code: CodeAuthVerifyNetworkFailed,
		Message: "access_token=secret\nhttps://private.example/device", VerificationURL: "https://private.example/device",
	})
	if err != nil {
		t.Fatal(err)
	}
	if event.Message == "" || !event.Retryable || event.VerificationURL != "" || strings.Contains(event.Message, "secret") || strings.Contains(event.Message, "https://") {
		t.Fatalf("unsafe failure event: %+v", event)
	}
}

func TestFailureEventRejectsUnknownCodeAndWrongPhase(t *testing.T) {
	for _, event := range []Event{
		{Type: "failure", Phase: PhaseVerifyAuthorization, Code: "UNKNOWN"},
		{Type: "failure", Phase: PhaseWaitForAuthorization, Code: CodeAuthVerifyNetworkFailed},
	} {
		if _, err := SanitizeFailure(event); err == nil {
			t.Fatalf("accepted invalid failure: %+v", event)
		}
	}
}
