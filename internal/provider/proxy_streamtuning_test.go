package provider

import (
	"net/http"
	"testing"
)

func TestCommandCodeStreamTransportTuning(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	t.Setenv("GOLDER_PROXY", "")

	c := clientForURL("commandcode", "https://api.commandcode.ai/provider/v1")
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("commandcode transport = %T, want *http.Transport", c.Transport)
	}
	if !tr.ForceAttemptHTTP2 {
		t.Error("commandcode must allow HTTP/2; the endpoint serves h2")
	}
	if !tr.DisableCompression {
		t.Error("commandcode must not buffer a compressed stream")
	}
	if !tr.DisableKeepAlives {
		t.Error("commandcode must not reuse a connection the proxy may have closed")
	}

	other := clientForURL("openai", "https://api.openai.com/v1")
	otr, ok := other.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("openai transport = %T, want *http.Transport", other.Transport)
	}
	if otr.DisableKeepAlives || otr.DisableCompression {
		t.Error("other providers must keep the shared transport behavior")
	}
}
