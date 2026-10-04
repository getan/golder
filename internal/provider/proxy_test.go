package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getan/golder/internal/cli/config"
)

func TestProxySelectionRoutesDiscoveryAndAllChatProtocols(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GOLDER_PROXY", "")
	paths := make(chan string, 10)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !r.URL.IsAbs() || r.URL.Host != "relay.invalid" {
			t.Error("expected forward-proxy request to relay.invalid")
		}
		paths <- r.URL.Path
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"relay-model"}]}`))
		} else {
			// A terminal response suffices to verify routing without a live LLM.
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer proxy.Close()
	if err := config.SaveProxyConfig(config.ProxyConfig{URL: proxy.URL, Providers: []string{"openai", "anthropic"}}); err != nil {
		t.Fatal(err)
	}
	base := "http://relay.invalid/v1"
	ids, err := FetchProviderModels(context.Background(), "openai", base, "openai", "test-key")
	if err != nil || len(ids) != 1 || ids[0] != "relay-model" {
		t.Fatalf("discovery: ids=%v err=%v", ids, err)
	}
	if got := <-paths; got != "/v1/models" {
		t.Fatalf("model path = %q", got)
	}
	for _, tc := range []struct {
		driver Provider
		path   string
	}{
		{NewOpenAICompatibleProvider(base, nil), "/v1/chat/completions"},
		{NewOpenAIResponsesProvider("openai", base, nil), "/v1/responses"},
		{NewAnthropicProvider(base, nil), "/v1/messages"},
	} {
		stream, err := tc.driver.StreamCompletion(context.Background(), CompletionRequest{
			Model: "test-model", Config: StreamConfig{APIKey: "test-key"},
		})
		if err != nil && !strings.Contains(err.Error(), "400") {
			t.Fatal(err)
		}
		if stream != nil {
			for range stream.Events() {
			}
		}
		select {
		case got := <-paths:
			if got != tc.path {
				t.Errorf("path = %q, want %q", got, tc.path)
			}
		default:
			t.Errorf("%s did not reach the selected proxy", tc.path)
		}
	}
}

func TestProxyOffBypassesEnvironmentAndLegacyDefaults(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GOLDER_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	if err := config.SaveProxyConfig(config.ProxyConfig{Providers: []string{}}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"openai", "deepseek", "opencode-go"} {
		if NeedsProxy(name, "https://opencode.ai/zen/go/v1") {
			t.Errorf("%s unexpectedly selected", name)
		}
		tr := clientForURL(name, "https://relay.example/v1").Transport.(*http.Transport)
		if tr.Proxy != nil {
			t.Error("unselected provider inherited environment proxy")
		}
	}
}

func TestSelectedProxyWithoutAddressFailsClearly(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GOLDER_PROXY", "")
	if err := config.SaveProxyConfig(config.ProxyConfig{Providers: []string{"openai"}}); err != nil {
		t.Fatal(err)
	}
	_, err := FetchProviderModels(context.Background(), "openai", "http://relay.invalid/v1", "openai", "")
	if err == nil || !strings.Contains(err.Error(), "no address configured") {
		t.Fatalf("expected actionable proxy error, got %v", err)
	}
}

func TestProxyURLPrecedenceAndDisplay(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GOLDER_PROXY", "")
	if err := config.SaveProxyConfig(config.ProxyConfig{URL: "http://localhost:8080"}); err != nil {
		t.Fatal(err)
	}
	if ProxyURL() != "http://localhost:8080" {
		t.Fatal("saved address not used")
	}
	t.Setenv("GOLDER_PROXY", "http://localhost:7897")
	if ProxyURL() != "http://localhost:7897" {
		t.Fatal("environment address should win")
	}
	if got := DisplayURL("https://user:password@example.test/v1?key=secret#secret"); got != "https://example.test/v1" {
		t.Fatal("URL display did not remove sensitive components")
	}
}

func TestSpecialProvidersKeepRoutingIdentityAndBaseURLEnv(t *testing.T) {
	params := map[string]string{
		"AZURE_OPENAI_API_KEY": "test-key", "AWS_BEARER_TOKEN_BEDROCK": "test-key",
		"GOOGLE_CLOUD_API_KEY": "test-key", "GOOGLE_CLOUD_PROJECT": "test-project",
		"GOOGLE_CLOUD_LOCATION": "us-central1", "CLOUDFLARE_API_KEY": "test-key",
		"CLOUDFLARE_ACCOUNT_ID": "test-account", "CLOUDFLARE_GATEWAY_ID": "test-gateway",
	}
	for _, name := range []string{"azure-openai-responses", "amazon-bedrock", "google-vertex", "cloudflare-workers-ai", "cloudflare-ai-gateway"} {
		t.Run(name, func(t *testing.T) {
			spec, _ := LookupProviderSpec(name)
			envName := config.GenericBaseURLEnvVar(name)
			p, _, err := ResolveNamedProvider(name, "test-model", "", "", func(key string) string {
				if key == envName {
					return "https://relay.example/v1"
				}
				return params[key]
			})
			if err != nil {
				t.Fatal(err)
			}
			if p.Name() != name {
				t.Fatalf("routing identity = %q, want %q", p.Name(), name)
			}
			var base string
			switch d := p.(type) {
			case *openAICompatDriver:
				base = d.baseURL
			case *anthropicCompatDriver:
				base = d.baseURL
			}
			if !strings.HasPrefix(base, "https://relay.example/v1") || !IsSpecialAuthProvider(spec) {
				t.Fatal("special provider ignored BASE_URL environment override")
			}
		})
	}
}
