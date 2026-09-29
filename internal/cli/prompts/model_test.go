package prompts

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/runtime"
)

// TestModelCommandSwitchesToBareProviderName verifies /model zai selects the
// zai provider's default model (issue #564): live.Model carries the concrete
// preset id so the next turn's wire request targets a real model instead of
// sending the literal provider name to OpenRouter.
func TestModelCommandSwitchesToBareProviderName(t *testing.T) {
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

// TestModelListsLiveCatalogAndSwitchesByNumber drives the bare-/model list
// against an httptest endpoint (issue #566): the catalog lands on
// live.FetchedModels, renders numbered with the current model marked, and
// /model <n> switches pinned to the live provider.
func TestModelListsLiveCatalogAndSwitchesByNumber(t *testing.T) {
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

// TestModelThinkSubcommand verifies /model think shows and sets the reasoning
// level through the shared thinkAction.
func TestModelThinkSubcommand(t *testing.T) {
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
