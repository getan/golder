package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ProbeWire asks an endpoint which wire it actually serves for a model. It is
// the answer for the case the static tables cannot describe: a gateway that
// translates a model onto another wire, or serves a family on several. The
// candidates are tried in WirePriority order with one minimal request each, and
// the first 2xx wins.
//
// The probe is deliberately tiny — a one-token completion, no tools — so the
// cost is one cheap request per candidate. It is opt-in (see
// selectWireWithProbe): golder only pays for it when GOLDER_WIRE_PROBE is set,
// because a run that never needed the answer should not spend it.
func ProbeWire(ctx context.Context, baseURL, apiKey, model string, timeout time.Duration) (Wire, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return "", fmt.Errorf("probe: no base URL")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	var failures []string
	for _, wire := range WirePriority {
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
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return wire, nil
		}
		failures = append(failures, fmt.Sprintf("%s: HTTP %d", wire, resp.StatusCode))
	}
	return "", fmt.Errorf("probe: no wire answered for %q (%s)", model, strings.Join(failures, "; "))
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

// wireProbeEnabled reports whether the opt-in probe is on.
func wireProbeEnabled(env func(string) string) bool {
	if env == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(env("GOLDER_WIRE_PROBE"))) {
	case "1", "on", "true", "yes":
		return true
	}
	return false
}

// selectWireWithProbe is SelectWire plus the opt-in probe: when the model's
// native wire is not among the provider's wires and the probe is enabled, ask
// the endpoint once which wire it serves.
func selectWireWithProbe(spec ProviderSpec, model, baseURL string, env func(string) string) Wire {
	wire := SelectWire(spec, model)
	if WireNative(spec, model) || !wireProbeEnabled(env) {
		return wire
	}
	key := ""
	if len(spec.EnvVars) > 0 && env != nil {
		key = env(spec.EnvVars[0])
	}
	if probed, err := ProbeWire(context.Background(), ResolveBaseURL(spec, baseURL, env), key, model, 5*time.Second); err == nil {
		return probed
	}
	return wire
}
