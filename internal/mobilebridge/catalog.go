package mobilebridge

import (
	"encoding/json"

	"github.com/getan/golder/internal/provider"
)

// mobileReasoningLevels is the single fixed ladder shown by the Android
// picker. A phone UI stays simple and predictable across providers; the
// provider wire layer clamps unsupported levels. TUI/GUI keep per-model
// discovery.
var mobileReasoningLevels = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

// CatalogProvider is one provider choice for the native picker. Models is
// intentionally small: it contains the registry default, and the host may add
// a custom model id. Remote model discovery can fill this list later without
// changing the picker contract.
type CatalogProvider struct {
	Name         string   `json:"name"`
	DefaultModel string   `json:"defaultModel"`
	Models       []string `json:"models"`
	Reasoning    []string `json:"reasoning"`
}

// Catalog is the provider/model/thinking data shown by a native host.
type Catalog struct {
	Providers       []CatalogProvider `json:"providers"`
	DefaultProvider string            `json:"defaultProvider"`
	DefaultModel    string            `json:"defaultModel"`
}

// CatalogJSON returns the chat-provider catalog from the same registry the TUI
// uses. Decision-only endpoints are omitted because they cannot serve a turn.
func CatalogJSON() (string, error) {
	specs := provider.ProviderSpecs()
	out := Catalog{Providers: make([]CatalogProvider, 0, len(specs))}
	for _, spec := range specs {
		if spec.DecisionOnly {
			continue
		}
		reasoning := append([]string(nil), mobileReasoningLevels...)
		out.Providers = append(out.Providers, CatalogProvider{
			Name: spec.Name, DefaultModel: spec.DefaultModel,
			Models: []string{spec.DefaultModel}, Reasoning: reasoning,
		})
	}
	if len(out.Providers) == 0 {
		out.Providers = append(out.Providers, CatalogProvider{
			Name: "openai", DefaultModel: "gpt-4o-mini",
			Models: []string{"gpt-4o-mini"}, Reasoning: append([]string(nil), mobileReasoningLevels...),
		})
	}
	out.DefaultProvider, out.DefaultModel = out.Providers[0].Name, out.Providers[0].DefaultModel
	for _, p := range out.Providers {
		if p.Name == "openai" {
			out.DefaultProvider, out.DefaultModel = p.Name, p.DefaultModel
			break
		}
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
