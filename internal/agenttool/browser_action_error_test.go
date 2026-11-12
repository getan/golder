package agenttool

import (
	"io"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/browserctl"
)

func TestBrowserMutationErrorDoesNotAuthorizeReplay(t *testing.T) {
	for _, err := range []error{io.EOF, &browserctl.Error{Code: "timeout", Message: "late", Retryable: true}} {
		result := browserActionError("click", err)
		text := agentcore.ContentToText(result.Content)
		if !result.IsError || !strings.Contains(text, "may have been dispatched") || strings.Contains(text, "(retryable)") {
			t.Fatalf("unsafe mutation error: %q", text)
		}
		details, ok := result.Details.(map[string]any)
		if !ok || details["execution_state"] != "unknown" {
			t.Fatalf("unknown outcome was not recorded structurally: %+v", result.Details)
		}
	}
}

func TestBrowserReadErrorRetainsRetryHint(t *testing.T) {
	result := browserActionError("snapshot", &browserctl.Error{Code: "busy", Message: "later", Retryable: true})
	if text := agentcore.ContentToText(result.Content); !strings.Contains(text, "(retryable)") {
		t.Fatalf("read-only error lost retry hint: %q", text)
	}
}

func TestBrowserPausedRemainsExplicitlyNonRetryable(t *testing.T) {
	result := browserActionError("click", &browserctl.Error{Code: "browser_paused", Message: "operator required"})
	text := agentcore.ContentToText(result.Content)
	if !result.IsError || !strings.Contains(text, "browser_paused") || strings.Contains(text, "(retryable)") {
		t.Fatalf("pause result = %q", text)
	}
}
