package prompts

import (
	"testing"

	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/provider"
)

// TestModelCatalogUsesBundledFallbackOnFetchFailure covers the first run on a
// restricted network: the live endpoint is unreachable and no disk cache
// exists, so /model must still open with the bundled list instead of failing.
func TestModelCatalogUsesBundledFallbackOnFetchFailure(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	t.Setenv("GOLDER_PROXY", "")
	t.Setenv("OPENAI_API_KEY", "test")

	live := &cli.LiveConfig{
		ProviderName: "openai",
		BaseURL:      "http://127.0.0.1:1",
	}
	creds := provider.NewCredentialStore(nil)
	ids, err := fetchModelCatalogIDs(live, creds)
	if err != nil {
		t.Fatalf("fetch with unreachable endpoint: %v", err)
	}
	if len(ids) == 0 {
		t.Fatal("fallback returned no models")
	}
	if len(live.FetchedModels) == 0 {
		t.Fatal("live.FetchedModels was not populated from the fallback")
	}
	// The fallback is a session floor, not a cache entry: the next run must
	// still try the live endpoint.
	key, _ := modelCatalogKey(live)
	if cached, fresh := provider.CachedModelCatalog(key); fresh || len(cached) != 0 {
		t.Errorf("fallback was persisted to the disk cache: %v fresh=%v", cached, fresh)
	}
}
