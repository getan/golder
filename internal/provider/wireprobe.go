package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// probeInflight deduplicates background probes: one question per (provider,
// model) at a time, so a re-resolution cannot stack a second request.
var probeInflight sync.Map

// wireProbeTimeout is how long one candidate gets to answer, and it is
// deliberately generous: nothing waits on the probe — it runs in the background
// of a resolution — while a real endpoint can need the time. The reported case
// answered /responses in 12-19s (a 24KB response body through a relay), which
// the old 5s budget read as "no Responses endpoint" and recorded as Chat.
const wireProbeTimeout = 30 * time.Second

// ProbeWire asks an endpoint which of the provider's declared wires it actually
// serves for a model. It is the answer for the case the static tables cannot
// describe: a gateway that translates a model onto another wire, or serves a
// family on several. Candidates are tried in the caller's order with one minimal
// request each; the first one that answers with a 2xx AND a body shaped like
// that wire's response wins. A 2xx alone is not enough: a gateway's web UI
// answers unknown paths with 200 + HTML, which is how a root-level /messages
// used to be mistaken for a working Anthropic endpoint.
//
// Only a rejection settles a question. A candidate that does not answer within
// timeout has not been refused — it may be a working wire behind a slow relay —
// so the probe stops there without an answer rather than letting a faster wire
// behind it claim the model. The caller keeps its static choice, and a wire that
// really is unsupported is discovered by the request itself (see fallback.go,
// which records a wire only after that wire served a real request).
//
// client carries the routing the caller's own requests take (HTTPClientFor), so
// the probe measures the endpoint the run will reach and not a second route. It
// is copied before the probe's timeout is applied, because a client is shared
// per route. Nil means a direct client — the explicit choice every provider
// request makes, so an unproxied endpoint does not inherit the shell's
// HTTP(S)_PROXY.
//
// The probe is deliberately tiny — a one-token completion, no tools — so the
// cost is one cheap request per candidate. It is opt-in (see
// selectWireWithProbe): golder only pays for it when GOLDER_WIRE_PROBE is set,
// because a run that never needed the answer should not spend it.
func ProbeWire(ctx context.Context, client *http.Client, baseURL, apiKey, model string, candidates []Wire, timeout time.Duration) (Wire, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return "", fmt.Errorf("probe: no base URL")
	}
	if timeout <= 0 {
		timeout = wireProbeTimeout
	}
	if len(candidates) == 0 {
		candidates = WirePriority
	}
	if client == nil {
		client = &http.Client{Transport: directTransport}
	}
	probe := *client
	probe.Timeout = timeout
	var failures []string
	for _, wire := range candidates {
		path, body, headers := probeRequest(wire, model)
		if path == "" {
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(body))
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", wire, err))
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if apiKey != "" {
			setProbeAuth(req, wire, apiKey)
		}
		resp, err := probe.Do(req)
		if err != nil {
			if probeTimedOut(err) {
				// The wire never answered, which proves nothing about it. Letting
				// the next candidate speak for the model is exactly how a working
				// Responses endpoint got recorded as Chat.
				msg := fmt.Sprintf("probe: %s did not answer within %s for %q, so no wire could be settled", wire, timeout, model)
				if len(failures) > 0 {
					msg += " (after " + strings.Join(failures, "; ") + ")"
				}
				return "", errors.New(msg)
			}
			failures = append(failures, fmt.Sprintf("%s: %v", wire, err))
			continue
		}
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 &&
			probeAccepted(wire, resp.Header.Get("Content-Type"), respBody) {
			return wire, nil
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			failures = append(failures, fmt.Sprintf("%s: HTTP %d, body is not a %s response", wire, resp.StatusCode, wire))
		} else {
			failures = append(failures, fmt.Sprintf("%s: HTTP %d", wire, resp.StatusCode))
		}
	}
	return "", fmt.Errorf("probe: no wire answered for %q (%s)", model, strings.Join(failures, "; "))
}

// probeTimedOut reports whether a candidate failed for time rather than for what
// it is. The probe's own timeout and a cancelled caller context say nothing
// about the wire; a refused connection or a 404 does.
func probeTimedOut(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) ||
		os.IsTimeout(err)
}

// probeAccepted reports whether a response really came from the wire that was
// asked. The status and the body both have to agree: a gateway that serves its
// SPA for unknown paths returns 200 + HTML (the case that made a root
// /messages look like a working Anthropic endpoint), and some gateways return
// an error object with a 2xx. Only the wire's own response shape counts.
func probeAccepted(wire Wire, contentType string, body []byte) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if strings.Contains(ct, "html") || strings.Contains(ct, "xml") {
		return false
	}
	b := bytes.TrimSpace(body)
	if len(b) == 0 || b[0] == '<' {
		return false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(b, &obj) != nil {
		return false
	}
	nonNull := func(key string) bool {
		raw, ok := obj[key]
		return ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
	}
	text := func(key string) string {
		var s string
		_ = json.Unmarshal(obj[key], &s)
		return s
	}
	if nonNull("error") {
		return false
	}
	switch wire {
	case WireResponses:
		return text("object") == "response" || nonNull("output") || strings.HasPrefix(text("id"), "resp_")
	case WireMessages:
		return text("type") == "message" || nonNull("content")
	case WireGemini:
		return nonNull("candidates") || nonNull("promptFeedback")
	case WireChat:
		return nonNull("choices")
	}
	return false
}

// probeRequest builds the minimal body each wire accepts.
func probeRequest(wire Wire, model string) (path string, body []byte, headers map[string]string) {
	switch wire {
	case WireResponses:
		payload := map[string]any{"model": model, "input": "ping", "max_output_tokens": 1, "store": false}
		b, _ := json.Marshal(payload)
		return "/responses", b, nil
	case WireMessages:
		payload := map[string]any{
			"model": model, "max_tokens": 1,
			"messages": []map[string]any{{"role": "user", "content": "ping"}},
		}
		b, _ := json.Marshal(payload)
		return "/messages", b, map[string]string{"anthropic-version": "2023-06-01"}
	case WireGemini:
		payload := map[string]any{
			"contents":         []map[string]any{{"parts": []map[string]any{{"text": "ping"}}}},
			"generationConfig": map[string]any{"maxOutputTokens": 1},
		}
		b, _ := json.Marshal(payload)
		return "/models/" + model + ":generateContent", b, nil
	case WireChat:
		payload := map[string]any{
			"model": model, "max_tokens": 1,
			"messages": []map[string]any{{"role": "user", "content": "ping"}},
		}
		b, _ := json.Marshal(payload)
		return "/chat/completions", b, nil
	}
	return "", nil, nil
}

// setProbeAuth uses the header each wire's servers conventionally expect.
func setProbeAuth(req *http.Request, wire Wire, key string) {
	switch wire {
	case WireMessages:
		req.Header.Set("x-api-key", key)
	case WireGemini:
		req.Header.Set("x-goog-api-key", key)
	default:
		req.Header.Set("Authorization", "Bearer "+key)
	}
}

// wireProbeDisabled reports whether the lazy probe was switched off.
func wireProbeDisabled(env func(string) string) bool {
	if env == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(env("GOLDER_WIRE_PROBE"))) {
	case "0", "off", "false", "no", "disabled":
		return true
	}
	return false
}

// cachedWireFor returns the cached answer for a model when that answer is
// allowed to stand in for the static wire.
//
// The shared family table (ModelWire) is a public rule, and a gateway's declared
// ModelWireRules are its own word for a family — neither is a guess about an
// endpoint, so neither is a probe's to overturn. Where one of them answers and
// this endpoint serves that wire, only evidence may contradict it: an answer a
// real request produced (served, see storeServedWire), which is how a gateway
// that really diverges gets recorded — the adaptive fallback watches the wire
// carry the model's own turn. A probe's answer may stand in only where the
// question was genuinely open: an endpoint that does not serve the family's wire
// at all, so the static pick is a translation (openrouter reaching Claude over
// Chat).
func cachedWireFor(spec ProviderSpec, model string, static Wire) (Wire, bool) {
	confirmed, served, ok := CachedWire(spec.Name, model)
	if !ok || !wireAllowed(spec, confirmed) {
		return "", false
	}
	if served {
		return confirmed, true
	}
	if _, pinned := staticWire(spec, model); pinned {
		return "", false
	}
	if static == ModelWire(model) {
		return "", false
	}
	return confirmed, true
}

// selectWireWithProbe is SelectWire plus a background probe — asked only where
// the question is genuinely open. The family table is a public rule and a
// gateway's declared rules are its own word, so where either answers and the
// endpoint serves that wire, nothing is asked: probing there is what recorded a
// slow /responses as a Chat endpoint. That leaves the case the probe exists for —
// a model whose family wire this gateway does not serve, so the static choice is
// a translation and another declared wire may carry it better. The static answer
// is returned immediately (resolution never waits on the network) and the
// endpoint is asked in the background; a settled answer is stored per model and
// per family (see wirecache.go) for the next resolution, and a candidate that
// timed out settles nothing (see ProbeWire). GOLDER_WIRE_PROBE=off disables new
// questions for offline runs; cached answers still apply.
func selectWireWithProbe(spec ProviderSpec, model, baseURL string, env func(string) string) Wire {
	wire := SelectWire(spec, model)
	if confirmed, ok := cachedWireFor(spec, model, wire); ok {
		return confirmed
	}
	// Nothing left to ask: a wire the gateway declares for the family, the
	// family table's own wire that this endpoint serves, or an endpoint with only
	// one wire to speak.
	if _, pinned := staticWire(spec, model); pinned || wire == ModelWire(model) {
		return wire
	}
	if len(ProviderWires(spec)) < 2 {
		return wire
	}
	if wireProbeDisabled(env) {
		return wire
	}
	// A family routes the same way on a given endpoint, so the question is asked
	// about the family's newest model (see newestInFamily) and the answer is
	// stored for the family and for the model the run actually uses. The wait is
	// bounded: a slow gateway costs this run the static answer (and, at worst,
	// one failed request the background answer then fixes), never a stalled
	// launch.
	target := newestInFamily(spec, model, baseURL, env)
	apiKey := ""
	if len(spec.EnvVars) > 0 && env != nil {
		apiKey = env(spec.EnvVars[0])
	}
	// The question goes over the route this provider's own requests take, so the
	// answer describes the endpoint the run will reach — proxy selection
	// included — instead of a second route the probe picked for itself.
	base := ResolveBaseURL(spec, baseURL, env)
	client := HTTPClientFor(spec.Name, base)
	// One probe per (provider, model) at a time: a re-resolution while the
	// first question is still in flight must not spawn a second.
	key := wireCacheKey(spec.Name, model)
	if _, busy := probeInflight.LoadOrStore(key, struct{}{}); busy {
		return wire
	}
	go func() {
		defer probeInflight.Delete(key)
		if probed, err := ProbeWire(context.Background(), client, base, apiKey, target, ProviderWires(spec), wireProbeTimeout); err == nil {
			storeWire(spec.Name, model, probed)
			if fam := ModelFamily(model); fam != "" {
				storeWire(spec.Name, "family:"+fam, probed)
			}
		}
	}()
	return wire
}
