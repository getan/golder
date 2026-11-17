package provider

import "testing"

// TestSingleWireProviderReportsItsWire pins the status bar contract for a
// provider that has one declared wire: withWireFallback adds no wrapper, so the
// driver itself must answer CurrentWire. Without it the stream-retry wrapper's
// initial guess (Chat) was displayed for an auto-resolved DeepSeek run.
func TestSingleWireProviderReportsItsWire(t *testing.T) {
	env := func(string) string { return "" }
	p, name, err := ResolveProvider("deepseek-v4-flash", "", "", "deepseek", env)
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	if name != "deepseek" {
		t.Fatalf("name = %q", name)
	}
	wr, ok := p.(WireReporter)
	if !ok {
		t.Fatal("resolved provider does not report its wire")
	}
	if got := wr.CurrentWire(); got != WireResponses {
		t.Fatalf("CurrentWire = %q, want %q", got, WireResponses)
	}
}

// TestAzureEndpointIsPinnedToChat pins azure-openai-responses to the wire its
// composed endpoint can actually serve: the shared driver appends
// /chat/completions, so the banner, the status bar and the request must not
// disagree by advertising the Responses API for it.
func TestAzureEndpointIsPinnedToChat(t *testing.T) {
	spec, ok := LookupProviderSpec("azure-openai-responses")
	if !ok {
		t.Fatal("azure spec missing")
	}
	if got := SelectWire(spec, "gpt-6.1-sol"); got != WireChat {
		t.Fatalf("SelectWire = %q, want %q", got, WireChat)
	}
	if got := EffectiveProtocol(spec.Name, "gpt-6.1-sol", ""); got != ProtocolOpenAI {
		t.Fatalf("EffectiveProtocol = %q, want %q", got, ProtocolOpenAI)
	}
	env := func(k string) string {
		if k == "AZURE_OPENAI_API_KEY" || k == "AZURE_OPENAI_BASE_URL" {
			return "dummy"
		}
		return ""
	}
	p, _, err := ResolveProvider("gpt-6.1-sol", "", "", spec.Name, env)
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	wr, ok := p.(WireReporter)
	if !ok {
		t.Fatal("azure provider does not report its wire")
	}
	if got := wr.CurrentWire(); got != WireChat {
		t.Fatalf("CurrentWire = %q, want %q", got, WireChat)
	}
}
