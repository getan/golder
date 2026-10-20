package provider

import "testing"

func TestFallbackModelCatalogCoversTheFirstRunGateways(t *testing.T) {
	for _, name := range []string{"opencode-go", "commandcode", "deepseek", "openai"} {
		ids := FallbackModelCatalog(name)
		if len(ids) == 0 {
			t.Fatalf("%s has no fallback catalog", name)
		}
		for _, id := range ids {
			if id == "" {
				t.Fatalf("%s has an empty model id", name)
			}
		}
	}
	if got := FallbackModelCatalog("not-a-provider"); got != nil {
		t.Errorf("unknown provider fallback = %v, want nil", got)
	}
}

func TestFallbackModelCatalogReturnsACopy(t *testing.T) {
	ids := FallbackModelCatalog("deepseek")
	if len(ids) == 0 {
		t.Fatal("deepseek fallback is empty")
	}
	ids[0] = "mutated"
	if got := FallbackModelCatalog("deepseek")[0]; got == "mutated" {
		t.Error("FallbackModelCatalog returned the shared slice")
	}
}
