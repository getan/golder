package mobilebridge

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCatalogJSONIncludesChatProviders(t *testing.T) {
	raw, err := CatalogJSON()
	if err != nil {
		t.Fatal(err)
	}
	var catalog Catalog
	if err := json.Unmarshal([]byte(raw), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Providers) == 0 {
		t.Fatal("catalog has no providers")
	}
	foundOpenAI := false
	for _, p := range catalog.Providers {
		if p.Name == "openai" {
			foundOpenAI = true
			if p.DefaultModel == "" || len(p.Models) == 0 || len(p.Reasoning) == 0 {
				t.Fatalf("openai catalog entry incomplete: %+v", p)
			}
		}
		if p.Name == "typesafe" {
			t.Fatal("decision-only provider leaked into the chat catalog")
		}
	}
	if !foundOpenAI || !strings.Contains(raw, `"defaultProvider":"openai"`) {
		t.Fatalf("catalog default/provider missing: %s", raw)
	}
}
