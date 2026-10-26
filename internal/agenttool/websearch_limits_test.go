package agenttool

import (
	"strings"
	"testing"
)

func TestRenderSearchResultsCapsOutput(t *testing.T) {
	results := make([]searchResult, 100)
	for i := range results {
		results[i] = searchResult{
			Title:   strings.Repeat("title", 30),
			URL:     "https://example.com/" + strings.Repeat("u", 100),
			Snippet: strings.Repeat("s", webSearchSnippetMax),
		}
	}
	got := renderSearchResults("q", "fixture", results)
	if len(got) > webSearchResultMax+128 {
		t.Fatalf("rendered results = %d bytes, want <= %d", len(got), webSearchResultMax)
	}
	if !strings.Contains(got, "truncated") {
		t.Fatalf("capped results missing truncation marker: %q", got[:min(200, len(got))])
	}
}
