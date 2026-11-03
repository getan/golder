package appsession

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/cli/prompts"
	"github.com/getan/golder/internal/provider"
)

// ListModels returns the models the live provider serves, newest families
// first. A failed catalog fetch falls back to the bundled floor for the
// provider, never to an empty picker.
func (c *Controller) ListModels(ctx context.Context) ([]ModelOption, error) {
	c.mu.Lock()
	live := *c.live
	creds := c.creds
	c.mu.Unlock()

	ids, err := prompts.EnsureModelCatalog(&live, creds)
	if err != nil || len(ids) == 0 {
		ids = provider.FallbackModelCatalog(live.ProviderName)
		if len(ids) == 0 {
			if spec, ok := provider.LookupProviderSpec(live.ProviderName); ok && spec.DefaultModel != "" {
				ids = []string{spec.DefaultModel}
			}
		}
		if len(ids) == 0 && err != nil {
			return nil, err
		}
	}
	ids = provider.OrderModelIDs(ids)
	defaultModel := prompts.DefaultModelForProvider(live.ProviderName)
	out := make([]ModelOption, 0, len(ids))
	for _, id := range ids {
		out = append(out, ModelOption{
			ID:       id,
			Provider: live.ProviderName,
			Current:  id == live.Model,
			Default:  id == defaultModel,
		})
	}
	if live.Model != "" && !containsID(out, live.Model) {
		out = append([]ModelOption{{
			ID:       live.Model,
			Provider: live.ProviderName,
			Current:  true,
			Default:  live.Model == defaultModel,
		}}, out...)
	}
	return out, nil
}

func containsID(opts []ModelOption, id string) bool {
	for _, o := range opts {
		if o.ID == id {
			return true
		}
	}
	return false
}

// Providers lists the conversation providers, ready ones first.
func (c *Controller) Providers() []string {
	c.mu.Lock()
	creds := c.creds
	c.mu.Unlock()
	specs := prompts.ProvidersAvailableFirst(creds)
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.Name
	}
	return out
}

// SelectModel switches the live model (and provider, when the id belongs to
// another gateway).
func (c *Controller) SelectModel(ctx context.Context, providerName, model string, thinking agentcore.ThinkingLevel) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return fmt.Errorf("model: empty model id")
	}
	c.mu.Lock()
	live := c.live
	prevModel, prevProvider, prevThinking := live.Model, live.ProviderName, live.ThinkingLevel
	baseURL, protocol := live.BaseURL, live.Protocol
	c.mu.Unlock()

	prov, resolved, err := provider.ResolveProvider(model, baseURL, protocol, providerName, os.Getenv)
	if err != nil {
		return err
	}
	c.mu.Lock()
	live.Model, live.ProviderName, live.Provider = model, resolved, prov
	if thinking != "" {
		live.ThinkingLevel = thinking
	}
	live.RefreshContextWindow()
	c.header.Model, c.header.Provider = live.Model, live.ProviderName
	c.status.Model, c.status.Provider = live.Model, live.ProviderName
	c.status.Thinking = live.ThinkingLevel
	c.status.ContextLimit = live.ContextWindow
	c.mu.Unlock()

	c.emitStatus()
	c.addNotice(fmt.Sprintf("Model switched to %s (provider: %s). Previous: %s on %s.",
		model, resolved, prevModel, prevProvider), false)
	if prevThinking != "" && thinking != "" && prevThinking != thinking {
		c.mu.Lock()
		c.status.Thinking = thinking
		c.mu.Unlock()
	}
	return nil
}

// SetThinking changes the reasoning level for the next turn.
func (c *Controller) SetThinking(level agentcore.ThinkingLevel) error {
	switch level {
	case agentcore.ThinkingOff, agentcore.ThinkingMinimal, agentcore.ThinkingLow,
		agentcore.ThinkingMedium, agentcore.ThinkingHigh, agentcore.ThinkingXHigh,
		agentcore.ThinkingMax:
	default:
		return fmt.Errorf("unknown reasoning level %q", level)
	}
	c.mu.Lock()
	c.live.ThinkingLevel = level
	c.status.Thinking = level
	c.mu.Unlock()
	c.emitStatus()
	return nil
}

// ThinkingLevels lists the selectable reasoning levels.
func ThinkingLevels() []string {
	return []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}
}

// ModelForProvider returns the model a switch to providerName would start with.
func ModelForProvider(providerName string) string {
	return prompts.DefaultModelForProvider(providerName)
}
