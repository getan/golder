package appsession

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/cli/config"
	"github.com/getan/golder/internal/cli/launchcfg"
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

// StatusSnapshot reads the current session status without copying transcript items.
func (c *Controller) StatusSnapshot() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// Providers lists only configured conversation providers. The active provider
// remains visible even when its credentials are missing or it is custom.
func (c *Controller) Providers() []string {
	c.mu.Lock()
	creds := c.creds
	current := ""
	if c.live != nil {
		current = c.live.ProviderName
	}
	c.mu.Unlock()
	cfg, _ := config.LoadFileConfig(config.FileConfigPath())
	names, _ := provider.CredentialNames(provider.CredentialFilePath())
	named := make(map[string]bool, len(names))
	for _, name := range names {
		named[name] = true
	}
	specs := prompts.ProvidersAvailableFirst(creds)
	out := make([]string, 0, len(specs))
	seenCurrent := false
	for _, spec := range specs {
		configured := spec.Name == current || strings.EqualFold(spec.Name, cfg.Provider) ||
			prompts.CredentialAvailable(creds, spec.Name) || named[spec.Name+"-default"]
		if !configured {
			for _, env := range provider.BaseURLEnvVars(spec) {
				if strings.TrimSpace(os.Getenv(env)) != "" {
					configured = true
					break
				}
			}
		}
		if configured {
			out = append(out, spec.Name)
			seenCurrent = seenCurrent || spec.Name == current
		}
	}
	if current != "" && !seenCurrent {
		out = append([]string{current}, out...)
	}
	return out
}

// SelectProvider switches this session to a configured provider and its saved
// default model. Overrides from the old gateway must never follow the switch.
func (c *Controller) SelectProvider(ctx context.Context, name string) error {
	name = strings.ToLower(strings.TrimSpace(name))
	if err := ctx.Err(); err != nil {
		return err
	}
	configured := false
	for _, candidate := range c.Providers() {
		if candidate == name {
			configured = true
			break
		}
	}
	if !configured {
		return fmt.Errorf("provider %q is not configured; add it in Settings", name)
	}
	spec, ok := provider.LookupProviderSpec(name)
	if !ok || spec.DecisionOnly {
		return fmt.Errorf("provider %q is not a conversation provider", name)
	}
	model := ModelForProvider(name)
	if model == "" {
		return fmt.Errorf("provider %q has no default model", name)
	}
	cfg, err := config.LoadFileConfig(config.FileConfigPath())
	if err != nil {
		return err
	}
	baseURL, protocol, credentialName, apiKey := "", "", name+"-default", ""
	if strings.EqualFold(cfg.Provider, name) {
		baseURL, protocol = cfg.BaseURL, cfg.Protocol
		apiKey = cfg.APIKey
		if cfg.Credential != "" {
			credentialName = cfg.Credential
		}
	}
	prov, resolved, err := provider.ResolveProvider(model, baseURL, protocol, name, os.Getenv)
	if err != nil {
		return err
	}
	credential := launchcfg.ResolveCredential(resolved, apiKey, credentialName)
	level := prompts.DefaultThinkingLevelForProviderModel(name, model)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	if c.live == nil {
		c.mu.Unlock()
		return fmt.Errorf("session is not open")
	}
	if c.running {
		c.mu.Unlock()
		return ErrBusy
	}
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return err
	}
	previous := c.live.ProviderName
	if c.creds != nil && credential.ResolvedValue() != "" {
		c.creds.SetOverride(resolved, credential.ResolvedValue())
	}
	c.live.Model, c.live.ProviderName, c.live.Provider = model, resolved, prov
	c.live.BaseURL, c.live.Protocol = baseURL, protocol
	c.live.FetchedModels, c.live.FetchedKey, c.live.FetchedAt = nil, "", time.Time{}
	if level != "" {
		c.live.ThinkingLevel = level
	}
	c.live.RefreshContextWindow()
	c.header.Model, c.header.Provider = model, resolved
	c.status.Model, c.status.Provider = model, resolved
	c.status.Thinking, c.status.ContextLimit = c.live.ThinkingLevel, c.live.ContextWindow
	c.mu.Unlock()
	c.emitStatus()
	c.addNotice(fmt.Sprintf("Provider switched to %s (model: %s). Previous: %s.", resolved, model, previous), false)
	return nil
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
