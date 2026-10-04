package prompts

import (
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/cli/config"
	"github.com/getan/golder/internal/provider"
	"github.com/getan/golder/internal/runtime"
)

func TestProxyCommandPersistsSelection(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GOLDER_PROXY", "")
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, &cli.LiveConfig{}, nil)
	for _, command := range []string{"/proxy url http://127.0.0.1:7897", "/proxy openai on", "/proxy opencode off"} {
		out, err := reg.ResolveOutcome(command)
		if err != nil || !strings.Contains(out.Message, "Saved") {
			t.Fatalf("%s: %v %s", command, err, out.Message)
		}
	}
	cfg, err := config.LoadProxyConfig()
	if err != nil || cfg.URL != "http://127.0.0.1:7897" ||
		!slices.Contains(cfg.Providers, "openai") || slices.Contains(cfg.Providers, "opencode-zen") {
		t.Fatalf("saved selection incorrect: %v", err)
	}
	info, err := os.Stat(config.ProxyConfigPath())
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("proxy file must have private permissions")
	}
	before, _ := os.ReadFile(config.ProxyConfigPath())
	for _, command := range []string{"/proxy openai maybe", "/proxy unknown on", "/proxy url invalid"} {
		reg.ResolveOutcome(command)
		after, _ := os.ReadFile(config.ProxyConfigPath())
		if string(before) != string(after) {
			t.Errorf("invalid command changed settings: %s", command)
		}
	}
	out, _ := reg.ResolveOutcome("/proxy")
	if !strings.Contains(out.Message, "[x] openai") || !strings.Contains(out.Message, "[ ] deepseek") {
		t.Fatal("listing does not reflect saved selections")
	}
}

func TestProviderSwitchUsesEnvironmentRelayForModelsAndCache(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GOLDER_HOME", t.TempDir())
	t.Setenv("GOLDER_PROXY", "")
	t.Setenv("OPENAI_API_KEY", "test-key")
	requests := 0
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("incorrect relay model request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"relay-only-model"}]}`))
	}))
	defer relay.Close()
	t.Setenv("OPENAI_BASE_URL", relay.URL+"/v1")
	live := &cli.LiveConfig{ProviderName: "deepseek", BaseURL: "https://previous.invalid"}
	reg := runtime.NewSlashRegistry()
	creds := provider.NewCredentialStore(nil)
	RegisterLiveCommands(reg, live, creds)
	reg.ResolveOutcome("/provider openai")
	ids, err := EnsureModelCatalog(live, creds)
	if err != nil || len(ids) != 1 || ids[0] != "relay-only-model" || requests != 1 {
		t.Fatalf("relay discovery failed: ids=%v err=%v requests=%d", ids, err, requests)
	}
	key := provider.ModelCatalogCacheKey("openai", relay.URL+"/v1")
	if cached, fresh := provider.CachedModelCatalog(key); !fresh || len(cached) != 1 {
		t.Fatal("catalog not cached under relay endpoint")
	}
	list, _ := reg.ResolveOutcome("/provider")
	for _, want := range []string{"OPENAI_BASE_URL", relay.URL + "/v1", "[OPENAI_BASE_URL]", "direct"} {
		if !strings.Contains(list.Message, want) {
			t.Errorf("provider listing missing %q", want)
		}
	}
	// Changing endpoint must not reuse the previous relay's session cache.
	t.Setenv("OPENAI_BASE_URL", "https://different.invalid/v1")
	if ids := CachedModelCatalog(live); len(ids) != 0 {
		t.Fatalf("stale endpoint catalog reused: %v", ids)
	}
}
