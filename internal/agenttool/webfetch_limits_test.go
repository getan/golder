package agenttool

import (
	"net/http"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

func TestWebFetchCapsBodyBeforeConversion(t *testing.T) {
	body := strings.Repeat("x", webFetchMaxBytes+100)
	res := execWebFetch(t, func(r *http.Request) (*http.Response, error) {
		return makeResp(200, "text/plain", body), nil
	}, `{"url":"https://example.com/big"}`)
	details, ok := res.Details.(map[string]any)
	if !ok || details["bodyTruncated"] != true || details["bodyBytes"] != webFetchMaxBytes {
		t.Fatalf("details = %#v, want a bounded body read", res.Details)
	}
	if got := len(agentcore.ContentToText(res.Content)); got > webFetchMaxMarkdown+256 {
		t.Fatalf("result text = %d bytes, want <= markdown budget", got)
	}
	if !strings.Contains(agentcore.ContentToText(res.Content), "content truncated") {
		t.Fatal("body truncation was not disclosed")
	}
}
