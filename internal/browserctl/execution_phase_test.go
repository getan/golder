package browserctl

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestNotDispatchedRequiresBoundHostLocatorPhase(t *testing.T) {
	for _, tc := range []struct{ phase, browser, want string }{
		{"locator_resolution", "browser-1", "not_dispatched"},
		{"input_dispatch", "browser-1", "dispatched_unknown"},
		{"locator_resolution", "other-browser", "dispatched_unknown"},
	} {
		c := NewWithTransport("stub", roundTripFunc(func(*http.Request) (*http.Response, error) {
			return jsonReply(200, `{"ok":false,"error":{"code":"ambiguous_locator","message":"multiple matches","executionState":"not_dispatched","phase":"`+tc.phase+`"},"meta":{"browserId":"`+tc.browser+`","tabId":"tab-1","targetId":"target-1"}}`), nil
		}))
		_, _, err := c.Action(context.Background(), "click", map[string]any{}, Target{BrowserID: "browser-1", TabID: "tab-1", TargetID: "target-1"}, time.Second)
		var typed *Error
		if !errors.As(err, &typed) || typed.ExecutionState != tc.want {
			t.Fatalf("phase=%s browser=%s: %v", tc.phase, tc.browser, err)
		}
	}
}
