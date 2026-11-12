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

func TestBrowserLocatorRecoveryRequiresKnownNoDispatch(t *testing.T) {
	for _, code := range []string{"browser_target_context_unavailable", "browser_ref_not_in_snapshot", "unsupported_selector_syntax", "ambiguous_locator", "browser_target_changed"} {
		t.Run(code, func(t *testing.T) {
			result := browserError(browserNotDispatched(code, "fixture", nil))
			text := agentcore.ContentToText(result.Content)
			details, _ := result.Details.(map[string]any)
			if !strings.Contains(text, "not a browser-wide stop") || !strings.Contains(text, "snapshot, find, get or is") || details["recovery"] != "inspect_and_retarget" || details["execution_state"] != "not_dispatched" {
				t.Fatalf("missing autonomous recovery for %s: %q %#v", code, text, details)
			}
			unknown := browserActionError("click", &browserctl.Error{Code: code, Message: "unknown"})
			unknownDetails, _ := unknown.Details.(map[string]any)
			if unknownDetails["recovery"] == "inspect_and_retarget" || unknownDetails["execution_state"] != "unknown" || !strings.Contains(agentcore.ContentToText(unknown.Content), "may have been dispatched") {
				t.Fatalf("unknown dispatch was treated as safe retargeting: %#v %s", unknownDetails, agentcore.ContentToText(unknown.Content))
			}
		})
	}
	paused := browserError(browserNotDispatched("browser_paused", "verification", nil))
	if details, _ := paused.Details.(map[string]any); details["recovery"] == "inspect_and_retarget" {
		t.Fatal("verification pause was treated as a locator error")
	}
}

func TestBrowserGuidancePrefersAutonomousExploration(t *testing.T) {
	for _, want := range []string{
		"Do not interrupt the person merely because a locator or target is initially unclear",
		"change approach; report a genuine dead end",
		"Ask before an irreversible destructive action",
		"inspect the current page and business state automatically and continue other appropriate work",
		"do not replay the unknown action",
		"On browser_paused, stop",
	} {
		if !strings.Contains(browserToolUseHint, want) {
			t.Errorf("browser guidance missing %q", want)
		}
	}
}
