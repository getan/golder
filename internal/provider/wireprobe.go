package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// probeInflight deduplicates background probes: one question per (provider,
// model) at a time, so a re-resolution cannot stack a second request.
var probeInflight sync.Map

// ProbeWire asks an endpoint which of the provider's declared wires it actually
// serves for a model. It is the answer for the case the static tables cannot
// describe: a gateway that translates a model onto another wire, or serves a
// family on several. Candidates are tried in the caller's order with one minimal
// request each; the first one that answers with a 2xx AND a body shaped like
// that wire's response wins. A 2xx alone is not enough: a gateway's web UI
// answers unknown paths with 200 + HTML, which is how a root-level /messages
// used to be mistaken for a working Anthropic endpoint.
//
// The probe is deliberately tiny — a one-token completion, no tools — so the
// cost is one cheap request per candidate. It is opt-in (see
// selectWireWithProbe): golder only pays for it when GOLDER_WIRE_PROBE is set,
// because a run that never needed the answer should not spend it.
func ProbeWire(ctx context.Context, baseURL, apiKey, model string, candidates []Wire, timeout time.Duration) (Wire, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return "", fmt.Errorf("probe: no base URL")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if len(candidates) == 0 {
		candidates = WirePriority
	}
	client := &http.Client{Timeout: timeout}
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
		resp, err := client.Do(req)
		if err != nil {
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

// selectWireWithProbe is SelectWire plus a background probe, for every provider
// alike: when the model's native wire differs from the provider's registered
// wire, the static answer is a guess about how this endpoint routes the model.
// The guess is returned immediately — resolution never waits on the network —
// and the endpoint is asked in the background; its answer is stored per model
// and per family (see wirecache.go), so the next resolution already knows. A
// cached answer (one month) skips the question entirely, as do a provider that
// serves a single wire and a model whose native wire is the registered one.
// GOLDER_WIRE_PROBE=off disables new questions for offline runs; cached answers
// still apply.
func selectWireWithProbe(spec ProviderSpec, model, baseURL string, env func(string) string) Wire {
	wire := SelectWire(spec, model)
	if len(ProviderWires(spec)) < 2 {
		return wire
	}
	if ModelWire(model) == wireForProtocol(spec.Protocol) {
		return wire
	}
	if confirmed, ok := CachedWire(spec.Name, model); ok && wireAllowed(spec, confirmed) {
		return confirmed
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
	// One probe per (provider, model) at a time: a re-resolution while the
	// first question is still in flight must not spawn a second.
	key := wireCacheKey(spec.Name, model)
	if _, busy := probeInflight.LoadOrStore(key, struct{}{}); busy {
		return wire
	}
	go func() {
		defer probeInflight.Delete(key)
		if probed, err := ProbeWire(context.Background(), ResolveBaseURL(spec, baseURL, env), apiKey, target, ProviderWires(spec), 5*time.Second); err == nil {
			storeWire(spec.Name, model, probed)
			if fam := ModelFamily(model); fam != "" {
				storeWire(spec.Name, "family:"+fam, probed)
			}
		}
	}()
	return wire
}
