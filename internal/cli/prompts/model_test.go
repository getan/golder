package prompts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/provider"
	"github.com/getan/golder/internal/runtime"
)

// TestModelCommandSwitchesToBareProviderName verifies /model zai selects the
// zai provider's default model (issue #564): live.Model carries the concrete
// preset id so the next turn's wire request targets a real model instead of
// sending the literal provider name to OpenRouter.
func TestModelCommandSwitchesToBareProviderName(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	live := &cli.LiveConfig{Model: "openrouter/free", ProviderName: "openrouter"}
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, provider.NewCredentialStore(nil))

	out, err := reg.ResolveOutcome("/model zai")
	if err != nil {
		t.Fatalf("ResolveOutcome /model zai: %v", err)
	}
	if live.Model != "glm-4.7" {
		t.Errorf("live.Model = %q, want glm-4.7", live.Model)
	}
	if live.ProviderName != "zai" {
		t.Errorf("live.ProviderName = %q, want zai", live.ProviderName)
	}
	if !strings.Contains(out.Message, "glm-4.7") || !strings.Contains(out.Message, "zai") {
		t.Errorf("message = %q, want it to mention glm-4.7 (zai)", out.Message)
	}
}

// TestModelCommandConcreteIdStaysOnProvider verifies the fork behavior: a
// concrete model id switches verbatim but stays on the current provider
// instead of jumping through the heuristic chain (which used to route unknown
// ids to OpenRouter).
func TestModelCommandConcreteIdStaysOnProvider(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	live := &cli.LiveConfig{Model: "muse-spark-1.3-contributor", ProviderName: "opencode-go"}
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, provider.NewCredentialStore(nil))

	out, err := reg.ResolveOutcome("/model muse-spark-1.2-contributor")
	if err != nil {
		t.Fatalf("ResolveOutcome /model muse-spark-1.2: %v", err)
	}
	if live.Model != "muse-spark-1.2-contributor" || live.ProviderName != "opencode-go" {
		t.Errorf("live = (%q, %q), want (muse-spark-1.2-contributor, opencode-go)", live.Model, live.ProviderName)
	}
	if !strings.Contains(out.Message, "muse-spark-1.2-contributor") {
		t.Errorf("message = %q, want it to mention muse-spark-1.2-contributor", out.Message)
	}

	// A foreign-provider id also stays pinned to the current provider.
	out, err = reg.ResolveOutcome("/model gpt-4o")
	if err != nil {
		t.Fatalf("ResolveOutcome /model gpt-4o: %v", err)
	}
	if live.Model != "gpt-4o" || live.ProviderName != "opencode-go" {
		t.Errorf("live = (%q, %q), want (gpt-4o, opencode-go)", live.Model, live.ProviderName)
	}
	if !strings.Contains(out.Message, "gpt-4o") {
		t.Errorf("message = %q, want it to mention gpt-4o", out.Message)
	}
}

// TestModelSwitchMessageIncludesProtocol verifies the switch confirmation
// names the effective wire protocol, so a mid-session protocol change is
// visible in history even though the startup banner stays a launch snapshot.
// A Responses-family id on an OpenAI-wired gateway shows openai/resp_api;
// anything else on the same gateway shows openai/chat.
func TestModelSwitchMessageIncludesProtocol(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	live := &cli.LiveConfig{
		Model:         "glm-4.7",
		ProviderName:  "opencode-go",
		FetchedModels: []string{"glm-4.7", "deepseek-v4.1-flash"},
	}
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, provider.NewCredentialStore(nil))

	out, err := reg.ResolveOutcome("/model deepseek-v4.1-flash")
	if err != nil {
		t.Fatalf("ResolveOutcome /model deepseek-v4.1-flash: %v", err)
	}
	if !strings.Contains(out.Message, "protocol: openai/resp_api") {
		t.Errorf("message = %q, want it to name protocol openai/resp_api", out.Message)
	}

	out, err = reg.ResolveOutcome("/model glm-4.7")
	if err != nil {
		t.Fatalf("ResolveOutcome /model glm-4.7: %v", err)
	}
	if !strings.Contains(out.Message, "protocol: openai/chat") {
		t.Errorf("message = %q, want it to name protocol openai/chat", out.Message)
	}
}

// TestModelListsLiveCatalogAndSwitchesByNumber drives the bare-/model list
// against an httptest endpoint (issue #566): the catalog lands on
// live.FetchedModels, renders numbered with the current model marked, and
// /model <n> switches pinned to the live provider.
func TestModelListsLiveCatalogAndSwitchesByNumber(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":[{"id":"m-b"},{"id":"m-a"},{"id":"m-b"}]}`))
	}))
	defer srv.Close()

	live := &cli.LiveConfig{Model: "m-a", ProviderName: "openai", BaseURL: srv.URL + "/v1"}
	creds := provider.NewCredentialStore(nil)
	creds.SetOverride("openai", "test-key")
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, creds)

	out, err := reg.ResolveOutcome("/model")
	if err != nil {
		t.Fatalf("ResolveOutcome /model: %v", err)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", gotAuth)
	}
	if len(live.FetchedModels) != 2 || live.FetchedModels[0] != "m-a" || live.FetchedModels[1] != "m-b" {
		t.Fatalf("live.FetchedModels = %v, want [m-a m-b]", live.FetchedModels)
	}
	if live.FetchedAt.IsZero() {
		t.Error("FetchedAt not stamped")
	}
	for _, want := range []string{"m-a (current)", "1. m-a", "2. m-b", "openai"} {
		if !strings.Contains(out.Message, want) {
			t.Errorf("message missing %q:\n%s", want, out.Message)
		}
	}

	// Numeric pick switches pinned to the live provider.
	out, err = reg.ResolveOutcome("/model 2")
	if err != nil {
		t.Fatalf("ResolveOutcome /model 2: %v", err)
	}
	if live.Model != "m-b" || live.ProviderName != "openai" {
		t.Fatalf("live = (%q, %q), want (m-b, openai)", live.Model, live.ProviderName)
	}
	if models := live.Provider.Models(); len(models) != 1 || models[0].ID != "m-b" || models[0].Provider != "openai" {
		t.Fatalf("wire models = %+v, want one openai/m-b entry", models)
	}
	if !strings.Contains(out.Message, "m-b") {
		t.Errorf("message = %q, want it to mention m-b", out.Message)
	}

	// Out-of-range numbers error without moving.
	if out, err = reg.ResolveOutcome("/model 9"); err != nil {
		t.Fatalf("ResolveOutcome /model 9: %v", err)
	} else if !strings.Contains(out.Message, "out of range") {
		t.Errorf("message = %q, want out-of-range note", out.Message)
	}
	if live.Model != "m-b" {
		t.Errorf("live.Model = %q, want unchanged m-b", live.Model)
	}
}

// TestModelCommandSetsLevel verifies the combined form: /model <id> <level> and
// /model <n> <level> switch the model and apply the reasoning level in one step
// (this is what the TUI picker's stage-2 confirm runs).
func TestModelCommandSetsLevel(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	live := &cli.LiveConfig{Model: "openrouter/free", ProviderName: "openrouter"}
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, provider.NewCredentialStore(nil))

	out, err := reg.ResolveOutcome("/model zai high")
	if err != nil {
		t.Fatalf("ResolveOutcome /model zai high: %v", err)
	}
	if live.Model != "glm-4.7" || live.ProviderName != "zai" {
		t.Errorf("live = (%q, %q), want (glm-4.7, zai)", live.Model, live.ProviderName)
	}
	if live.ThinkingLevel != "high" {
		t.Errorf("live.ThinkingLevel = %q, want high", live.ThinkingLevel)
	}
	for _, want := range []string{"glm-4.7", "think level set to high"} {
		if !strings.Contains(out.Message, want) {
			t.Errorf("message %q missing %q", out.Message, want)
		}
	}

	// An invalid level is rejected without touching the current model or level.
	out, err = reg.ResolveOutcome("/model zai bogus")
	if err != nil {
		t.Fatalf("ResolveOutcome /model zai bogus: %v", err)
	}
	if !strings.Contains(out.Message, "invalid reasoning level") {
		t.Errorf("message = %q, want invalid-level note", out.Message)
	}
	if live.Model != "glm-4.7" || live.ThinkingLevel != "high" {
		t.Errorf("invalid level changed live state: (%q, %q)", live.Model, live.ThinkingLevel)
	}

	// Numeric picks accept the level too.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"m-a"},{"id":"m-b"}]}`))
	}))
	defer srv.Close()
	live.Model = "m-a"
	live.ProviderName = "openai"
	live.BaseURL = srv.URL + "/v1"
	out, err = reg.ResolveOutcome("/model 2 medium")
	if err != nil {
		t.Fatalf("ResolveOutcome /model 2 medium: %v", err)
	}
	if live.Model != "m-b" || live.ThinkingLevel != "medium" {
		t.Errorf("numeric pick = (%q, %q), want (m-b, medium)", live.Model, live.ThinkingLevel)
	}
}

// TestModelListAnnotatesCatalogLevels verifies the bare /model list annotates
// the models the models.dev cache knows with their advertised reasoning levels
// (the file shape matches provider's on-disk cache).
func TestModelListAnnotatesCatalogLevels(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GOLDER_HOME", dir)
	fixture, err := json.Marshal(map[string]any{
		"checked_at": time.Now(),
		"providers": map[string]any{
			"openai": map[string]any{
				"m-a": map[string]any{"reasoning": true, "levels": []string{"low", "high", "max"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "reasoning-catalog.json"), fixture, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"m-a"},{"id":"m-b"}]}`))
	}))
	defer srv.Close()

	live := &cli.LiveConfig{Model: "m-a", ProviderName: "openai", BaseURL: srv.URL + "/v1"}
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, provider.NewCredentialStore(nil))
	out, err := reg.ResolveOutcome("/model")
	if err != nil {
		t.Fatalf("ResolveOutcome /model: %v", err)
	}
	if !strings.Contains(out.Message, "m-a  [low|high|max]") {
		t.Errorf("message missing catalog levels:\n%s", out.Message)
	}
	if strings.Contains(out.Message, "m-b  [") {
		t.Errorf("m-b is not in the catalog and must not be annotated:\n%s", out.Message)
	}
}

// TestModelThinkSubcommand verifies /model think shows and sets the reasoning
// level through the shared thinkAction.
func TestModelThinkSubcommand(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	live := &cli.LiveConfig{Model: "m", ProviderName: "openai", ThinkingLevel: "medium"}
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, provider.NewCredentialStore(nil))

	out, err := reg.ResolveOutcome("/model think")
	if err != nil {
		t.Fatalf("ResolveOutcome /model think: %v", err)
	}
	if !strings.Contains(out.Message, "medium") {
		t.Errorf("message = %q, want current level medium", out.Message)
	}
	out, err = reg.ResolveOutcome("/model think xhigh")
	if err != nil {
		t.Fatalf("ResolveOutcome /model think xhigh: %v", err)
	}
	if live.ThinkingLevel != "xhigh" {
		t.Errorf("live.ThinkingLevel = %q, want xhigh", live.ThinkingLevel)
	}
	if out, err = reg.ResolveOutcome("/model think bogus"); err != nil {
		t.Fatalf("ResolveOutcome /model think bogus: %v", err)
	} else if !strings.Contains(out.Message, "invalid level") {
		t.Errorf("message = %q, want invalid-level note", out.Message)
	}
}

// TestModelListDegrades verifies a failing endpoint degrades gracefully:
// the error is reported with a /model <id> hint and the live model is
// untouched.
func TestModelListDegrades(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	live := &cli.LiveConfig{Model: "openrouter/free", ProviderName: "openai", BaseURL: srv.URL}
	creds := provider.NewCredentialStore(nil)
	creds.SetOverride("openai", "test-key")
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, creds)

	out, err := reg.ResolveOutcome("/model")
	if err != nil {
		t.Fatalf("ResolveOutcome /model: %v", err)
	}
	if live.FetchedModels != nil {
		t.Errorf("live.FetchedModels = %v, want nil after failed fetch", live.FetchedModels)
	}
	if !strings.Contains(out.Message, "/model <id>") {
		t.Errorf("message = %q, want the direct-switch hint", out.Message)
	}
	if live.Model != "openrouter/free" {
		t.Errorf("live.Model = %q, want unchanged", live.Model)
	}
}

// TestModelListUsesDiskCache verifies the cross-session cache: a fresh on-disk
// entry is served without any network request, so only the first session in
// the TTL window pays for the fetch.
func TestModelListUsesDiskCache(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("network hit: a fresh disk cache must be served offline")
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	defer srv.Close()
	base := srv.URL + "/v1"
	provider.StoreModelCatalog(provider.ModelCatalogCacheKey("openai", base), []string{"cached-a", "cached-b"})

	live := &cli.LiveConfig{Model: "cached-a", ProviderName: "openai", BaseURL: base}
	creds := provider.NewCredentialStore(nil)
	creds.SetOverride("openai", "test-key")
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, creds)

	out, err := reg.ResolveOutcome("/model")
	if err != nil {
		t.Fatalf("ResolveOutcome /model: %v", err)
	}
	for _, want := range []string{"1. cached-a", "2. cached-b"} {
		if !strings.Contains(out.Message, want) {
			t.Errorf("message missing %q:\n%s", want, out.Message)
		}
	}
	if len(live.FetchedModels) != 2 {
		t.Errorf("live.FetchedModels = %v, want the cached two", live.FetchedModels)
	}
}

// TestModelListServesStaleOnFailure verifies the resilience path: when the
// endpoint fails but a stale cache entry exists, the stale list is served
// instead of failing the picker.
func TestModelListServesStaleOnFailure(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GOLDER_HOME", dir)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	base := srv.URL + "/v1"
	storeStaleModelCatalog(t, dir, provider.ModelCatalogCacheKey("openai", base), []string{"old-a", "old-b"})

	live := &cli.LiveConfig{Model: "old-a", ProviderName: "openai", BaseURL: base, Protocol: "openai"}
	creds := provider.NewCredentialStore(nil)
	creds.SetOverride("openai", "test-key")
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, creds)

	out, err := reg.ResolveOutcome("/model")
	if err != nil {
		t.Fatalf("ResolveOutcome /model: %v", err)
	}
	if !strings.Contains(out.Message, "old-b") {
		t.Errorf("stale fallback not listed:\n%s", out.Message)
	}
	if len(live.FetchedModels) != 2 {
		t.Errorf("live.FetchedModels = %v, want the stale two", live.FetchedModels)
	}
}

// storeStaleModelCatalog writes a model-catalog.json entry aged past the TTL,
// so the next lookup treats it as stale.
func storeStaleModelCatalog(t *testing.T, home, key string, ids []string) {
	t.Helper()
	entry := map[string]any{
		"entries": map[string]any{
			key: map[string]any{
				"checked_at": time.Now().Add(-48 * time.Hour),
				"ids":        ids,
			},
		},
	}
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal stale cache: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "model-catalog.json"), data, 0o644); err != nil {
		t.Fatalf("write stale cache: %v", err)
	}
}
