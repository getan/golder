package provider

// This file implements the adaptive wire fallback. The lazy probe answers the
// question in the background for the NEXT resolution; a run still has to make
// its first request with the static guess. When that guess is wrong, a gateway
// answers 400 ModelProtocolUnsupported (opencode-go does for a domestic model
// with no Responses endpoint). The wrapper below catches that answer before any
// content reaches the caller and retries the same request down the provider's
// declared wires in WirePriority order: Responses, Messages, Chat, Gemini. The
// successful wire is stored in the same persistent cache the probe uses, so
// every later resolution selects it directly.

import (
	"context"
	"fmt"

	"github.com/getan/golder/internal/agentcore"
)

// withWireFallback wraps an auto-resolved provider with the rest of its declared
// wires. Explicit --protocol selections and special-auth providers are left
// alone: the user asked for a wire, or the endpoint's endpoint/auth composition
// is not interchangeable with another driver.
func withWireFallback(spec ProviderSpec, model, baseURL string, primaryWire Wire, primary Provider, env func(string) string) Provider {
	fallbacks := fallbackWires(spec, primaryWire)
	if len(fallbacks) == 0 {
		return primary
	}
	return &wireFallbackProvider{
		spec:          spec,
		model:         model,
		baseURL:       baseURL,
		env:           env,
		primaryWire:   primaryWire,
		primary:       primary,
		fallbackWires: fallbacks,
	}
}

// fallbackWires returns the declared wires after primary in WirePriority order.
// A primary that the provider does not declare yields no fallback: the provider
// contract is the list of wires it can actually speak.
func fallbackWires(spec ProviderSpec, primary Wire) []Wire {
	allowed := ProviderWires(spec)
	for i, w := range allowed {
		if w == primary {
			return allowed[i+1:]
		}
	}
	return nil
}

type wireFallbackProvider struct {
	spec          ProviderSpec
	model         string
	baseURL       string
	env           func(string) string
	primaryWire   Wire
	primary       Provider
	fallbackWires []Wire
}

func (p *wireFallbackProvider) Name() string    { return p.primary.Name() }
func (p *wireFallbackProvider) Models() []Model { return p.primary.Models() }

// UnwrapProvider returns the concrete driver behind an adaptive wire wrapper.
// Resolution tests and diagnostics that need the driver's own type can use it;
// runtime code should drive the Provider interface.
func UnwrapProvider(p Provider) Provider {
	for {
		w, ok := p.(*wireFallbackProvider)
		if !ok {
			return p
		}
		p = w.primary
	}
}

// StreamCompletion starts the fallback pump and returns the one stream the
// caller consumes. The pump emits the successful attempt's start event; a
// protocol-unsupported error from an earlier attempt never reaches the caller.
func (p *wireFallbackProvider) StreamCompletion(ctx context.Context, req CompletionRequest) (*AssistantMessageEventStream, error) {
	out := NewAssistantMessageEventStream(0)
	go p.pump(ctx, req, out)
	return out, nil
}

func (p *wireFallbackProvider) pump(ctx context.Context, req CompletionRequest, out *AssistantMessageEventStream) {
	defer out.Close()

	type attempt struct {
		wire Wire
		prov Provider
	}
	attempts := []attempt{{p.primaryWire, p.primary}}
	for _, w := range p.fallbackWires {
		protocol, ok := protocolForWire(w)
		if !ok {
			continue
		}
		prov, err := buildNamedProvider(p.spec, p.model, p.baseURL, protocol, p.env)
		if err != nil {
			continue
		}
		attempts = append(attempts, attempt{wire: w, prov: prov})
	}

	var lastErr error
	for _, a := range attempts {
		in, err := a.prov.StreamCompletion(ctx, req)
		if err != nil {
			lastErr = err
			continue
		}
		accepted, err := p.forwardAccepted(ctx, out, in)
		if accepted {
			storeWire(p.Name(), p.model, a.wire)
			if fam := ModelFamily(p.model); fam != "" {
				storeWire(p.Name(), "family:"+fam, a.wire)
			}
			return
		}
		lastErr = err
	}

	// Every candidate failed. Emit one start and the last failure so the caller
	// sees a normal terminal error instead of an empty stream.
	partial := agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		API:       "openai",
		Provider:  p.Name(),
		Model:     p.model,
	}
	_ = out.Emit(ctx, StreamStartEvent{Partial: partial})
	if lastErr == nil {
		lastErr = fmt.Errorf("%s: no declared wire answered for %q", p.Name(), p.model)
	}
	_ = out.Emit(ctx, StreamErrorEvent{
		Message: agentcore.AssistantMessage{
			RoleField:    agentcore.RoleAssistant,
			API:          "openai",
			Provider:     p.Name(),
			Model:        p.model,
			StopReason:   agentcore.StopReasonError,
			ErrorMessage: lastErr.Error(),
		},
		Err: lastErr,
	})
}

// forwardAccepted relays the first viable attempt. It holds the start event
// until it knows the attempt is not a protocol rejection; a protocol-unsupported
// error before any content is discarded so the next wire can be tried. Once a
// non-start event is accepted, the held start and every remaining event go to
// the caller.
func (p *wireFallbackProvider) forwardAccepted(ctx context.Context, out *AssistantMessageEventStream, in *AssistantMessageEventStream) (bool, error) {
	var start *StreamStartEvent
	accepted := false
	for ev := range in.Events() {
		if !accepted {
			if se, ok := ev.(StreamStartEvent); ok && start == nil {
				start = &se
				continue
			}
			if se, ok := ev.(StreamErrorEvent); ok && isModelProtocolUnsupportedErr(se.Err) {
				return false, se.Err
			}
			accepted = true
			if start != nil {
				if err := out.Emit(ctx, *start); err != nil {
					return true, err
				}
			}
		}
		if err := out.Emit(ctx, ev); err != nil {
			return true, err
		}
		switch ev.(type) {
		case StreamDoneEvent, StreamErrorEvent:
			return true, nil
		}
	}
	if !accepted {
		return false, fmt.Errorf("%s: stream ended without a terminal event", p.Name())
	}
	return true, nil
}
