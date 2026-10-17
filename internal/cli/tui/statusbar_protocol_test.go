package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/getan/golder/internal/provider"
)

// TestStatusBarProtocolSegment pins the segment's place and value: immediately
// after the model, carrying the wire the provider is actually using.
func TestStatusBarProtocolSegment(t *testing.T) {
	s := newStatusBar(DefaultTheme(), Options{Model: "mimo-v2.6-flash", ProviderName: "commandcode"}, "/tmp/x")
	wire := "responses"
	s.protocol = func() string { return wire }

	segs := s.segments()
	modelAt, protoAt := -1, -1
	for i, seg := range segs {
		switch {
		case strings.HasPrefix(seg.text, glyphModel):
			modelAt = i
		case strings.HasPrefix(seg.text, glyphProto):
			protoAt = i
		}
	}
	if modelAt < 0 || protoAt != modelAt+1 {
		t.Fatalf("segment order: model=%d protocol=%d, want the protocol immediately after the model (%v)", modelAt, protoAt, segs)
	}
	if !strings.Contains(stripANSI(s.Render(120)), glyphProto+" responses") {
		t.Errorf("protocol segment missing: %q", stripANSI(s.Render(120)))
	}

	// The adaptive fallback can move the model to another wire; the segment reads
	// the provider live, so the next render follows it.
	wire = "chat"
	out := stripANSI(s.Render(120))
	if !strings.Contains(out, glyphProto+" chat") {
		t.Errorf("protocol segment did not follow the fallback: %q", out)
	}
	if strings.Contains(out, glyphProto+" responses") {
		t.Errorf("the old protocol is still shown: %q", out)
	}
}

// TestStatusBarHidesUnknownProtocol: no selected protocol and a provider with no
// wire report hides the segment rather than guessing at one.
func TestStatusBarHidesUnknownProtocol(t *testing.T) {
	s := newStatusBar(DefaultTheme(), Options{Model: "m"}, "/tmp/x")
	if out := stripANSI(s.Render(120)); strings.Contains(out, glyphProto) {
		t.Errorf("unknown protocol should add no segment: %q", out)
	}
}

// wireProvider is a Provider that reports a mutable wire, standing in for the
// adaptive wrapper in the Model-level plumbing test.
type wireProvider struct{ wire provider.Wire }

func (p *wireProvider) Name() string               { return "commandcode" }
func (p *wireProvider) Models() []provider.Model   { return nil }
func (p *wireProvider) CurrentWire() provider.Wire { return p.wire }
func (p *wireProvider) StreamCompletion(_ context.Context, _ provider.CompletionRequest) (*provider.AssistantMessageEventStream, error) {
	return nil, nil
}

// TestSyncLiveDisplayPublishesTheWire: the Model republishes the protocol source
// whenever the live provider changes, so a /model or /provider switch reads the
// new provider's wire rather than the launch snapshot's.
func TestSyncLiveDisplayPublishesTheWire(t *testing.T) {
	m := NewModel(Options{Model: "m", ProviderName: "commandcode"})
	p := &wireProvider{wire: provider.WireResponses}
	m.live.Provider = p
	m.live.ProviderName = "commandcode"
	m.syncLiveDisplay()
	if m.statusBar.protocol == nil || m.statusBar.protocol() != "responses" {
		t.Fatalf("protocol = %q, want responses", m.statusBar.protocol())
	}
	p.wire = provider.WireChat
	if got := m.statusBar.protocol(); got != "chat" {
		t.Errorf("protocol after fallback = %q, want chat", got)
	}
}
