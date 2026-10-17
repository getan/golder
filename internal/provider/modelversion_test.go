package provider

import "testing"

// TestCompareModelVersion pins the version ordering the family probe uses to
// choose its representative model.
func TestCompareModelVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"deepseek-v4.1-flash", "deepseek-v4", 1},
		{"deepseek-v4", "deepseek-v4.1-flash", -1},
		{"gpt-5.6-luna", "gpt-5", 1},
		{"glm-5.3", "glm-5", 1},
		{"qwen3.8-max", "qwen3.7-plus", 1},
		{"gpt-5", "gpt-5", 0},
		{"gemini-3.8-flash", "gemini-3.7-flash", 1},
	}
	for _, c := range cases {
		if got := CompareModelVersion(c.a, c.b); got != c.want {
			t.Errorf("CompareModelVersion(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// TestNewestInFamily: the catalog decides the representative; without one the
// requested model is asked about directly.
func TestNewestInFamily(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	resetWireCache()
	spec := ProviderSpec{Name: "p", Protocol: ProtocolOpenAI}
	base := "https://example.test/v1"
	env := func(string) string { return "" }
	if got := newestInFamily(spec, "deepseek-v4.1-flash", base, env); got != "deepseek-v4.1-flash" {
		t.Fatalf("without catalog = %q, want the requested model", got)
	}
	StoreModelCatalog(ModelCatalogCacheKey(spec.Name, base), []string{
		"deepseek-v4", "deepseek-v4.1-flash", "deepseek-v4-pro", "glm-5.3",
	})
	if got := newestInFamily(spec, "deepseek-v4", base, env); got != "deepseek-v4.1-flash" {
		t.Fatalf("with catalog = %q, want the newest deepseek", got)
	}
}
