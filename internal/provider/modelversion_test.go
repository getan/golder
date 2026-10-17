package provider

import "testing"

// TestModelSeries pins the vendor grouping the /model list uses: OpenAI's ids
// are one series across gpt-, codex- and o-series names, and each other vendor
// is one series of its own.
func TestModelSeries(t *testing.T) {
	cases := map[string]string{
		"gpt-5.6-luna":               "openai",
		"gpt-5.3-codex":              "openai",
		"o3":                         "openai",
		"o4-mini":                    "openai",
		"claude-opus-5":              "anthropic",
		"anthropic/claude-haiku-5-5": "anthropic",
		"gemini-4-argon-contributor": "google",
		"grok-4.7":                   "xai",
		"muse-spark-1.3":             "meta",
		"glm-5.3":                    "zhipu",
		"qwen3.8-max":                "alibaba",
		"kimi-k3":                    "moonshot",
		"deepseek-v4.1-flash":        "deepseek",
		"jev-1.13":                   "typesafe",
	}
	for id, want := range cases {
		if got := ModelSeries(id); got != want {
			t.Errorf("ModelSeries(%q) = %q, want %q", id, got, want)
		}
	}
}

// TestOrderModelIDs pins the picker order: series groups alphabetically, and
// inside a group the newest version comes first.
func TestOrderModelIDs(t *testing.T) {
	got := OrderModelIDs([]string{
		"glm-5.2", "gpt-5", "gemini-3.7-flash", "o3",
		"glm-5.3", "gpt-5.6-luna", "gemini-3.8-flash",
	})
	want := []string{
		"gemini-3.8-flash", "gemini-3.7-flash",
		"gpt-5.6-luna", "gpt-5", "o3",
		"glm-5.3", "glm-5.2",
	}
	if len(got) != len(want) {
		t.Fatalf("OrderModelIDs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("OrderModelIDs = %v, want %v", got, want)
		}
	}
}

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
