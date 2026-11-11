package appsession

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/cli/config"
	"github.com/getan/golder/internal/cli/launchcfg"
	"github.com/getan/golder/internal/cli/prompts"
	"github.com/getan/golder/internal/provider"
)

// CredentialView is safe settings metadata. It never contains the credential.
type CredentialView struct {
	Available bool
	Source    string
	Name      string
	EnvVar    string
}

// ProviderSettings is the global provider defaults shown by Settings.
type ProviderSettings struct {
	Provider      string
	Model         string
	BaseURL       string
	Protocol      string
	Credential    CredentialView
	ThinkingLevel string
	Providers     []string
	Protocols     []string
}

// ProviderSettingsEdit is one settings save. NewCredential is write-only: it
// is consumed immediately and is never returned by a load or event.
type ProviderSettingsEdit struct {
	Provider         string
	Model            string
	BaseURL          string
	Protocol         string
	CredentialName   string
	NewCredential    string
	RemoveCredential bool
	ThinkingLevel    string
}

// AutomaticProtocolLabel previews the same wire selection as the TUI banner
// without constructing a provider or starting a network probe.
func (edit ProviderSettingsEdit) AutomaticProtocolLabel() string {
	spec, ok := provider.LookupProviderSpec(strings.TrimSpace(edit.Provider))
	if !ok {
		return ""
	}
	model := providerSettingsModel(edit.Model, spec)
	return provider.ProtocolLabel(provider.EffectiveProtocol(spec.Name, model, ""))
}

// LoadProviderSettings reads the shared config and derives a non-secret
// credential status for the launch default.
func (c *Controller) LoadProviderSettings() (ProviderSettings, error) {
	return c.LoadProviderSettingsFor("")
}

// LoadProviderSettingsFor loads a specific provider's saved form. It does not
// switch the live session or carry credentials from the previously edited form.
func (c *Controller) LoadProviderSettingsFor(name string) (ProviderSettings, error) {
	cfg, err := config.LoadFileConfig(config.FileConfigPath())
	if err != nil {
		return ProviderSettings{}, err
	}
	c.mu.Lock()
	live := *c.live
	c.mu.Unlock()

	providerName := launchcfg.CanonicalProviderName(firstSetting(name, cfg.Provider, live.ProviderName))
	model := cfg.ModelDefaults[providerName]
	thinking := ""
	if owner := launchcfg.CanonicalProviderName(cfg.Provider); owner == providerName || owner == "" && name == "" {
		model = firstSetting(model, cfg.Model)
		thinking = cfg.ThinkingLevel
	}
	profile, legacyKey, saved := launchcfg.ProviderConnection(cfg, providerName)
	if name == "" && providerName == live.ProviderName {
		model = firstSetting(model, live.Model)
		thinking = firstSetting(thinking, string(live.ThinkingLevel))
		if !saved {
			profile.BaseURL = live.BaseURL
		}
	}
	if spec, ok := provider.LookupProviderSpec(providerName); ok {
		model = providerSettingsModel(model, spec)
	}
	if scoped, scopedErr := config.DefaultThinkingLevelFor(providerName, model); scopedErr == nil && scoped != "" {
		thinking = scoped
	}
	credential := settingsCredential(providerName, profile.Credential, legacyKey, saved)
	credentialName := profile.Credential
	if credential.Status.Source == launchcfg.CredentialNamed {
		credentialName = credential.Status.Name
	}
	protocols := []string{"openai", "responses", "anthropic", "gemini"}
	if spec, ok := provider.LookupProviderSpec(providerName); ok {
		protocols = []string{"", spec.Protocol}
		if spec.Protocol == provider.ProtocolOpenAI {
			protocols = append(protocols, provider.ProtocolOpenAIResponses)
		}
		for _, variant := range spec.WireVariants {
			protocols = appendUniqueSetting(protocols, variant.Protocol)
		}
	}
	return ProviderSettings{
		Provider: providerName, Model: model, BaseURL: profile.BaseURL, Protocol: profile.ProtocolFor(model),
		Credential: CredentialView{
			Available: credential.Status.Available,
			Source:    string(credential.Status.Source),
			Name:      credentialName,
			EnvVar:    credential.Status.EnvVar,
		},
		ThinkingLevel: thinking,
		Providers:     provider.ProviderNames(),
		Protocols:     protocols,
	}, nil
}

func settingsCredential(name, reference, legacyKey string, saved bool) launchcfg.CredentialResult {
	if reference == "" && !saved {
		reference = name + "-default"
	}
	return launchcfg.ResolveCredential(name, legacyKey, reference)
}

// SaveProviderSettings writes a named credential first, then atomically updates
// config.toml to reference it. A failed credential write therefore never leaves
// a config reference to a value that was not saved.
func (c *Controller) SaveProviderSettings(ctx context.Context, edit ProviderSettingsEdit, apply bool) (ProviderSettings, error) {
	if err := ctx.Err(); err != nil {
		return ProviderSettings{}, err
	}
	edit, _, _, err := normalizeProviderEdit(edit)
	if err != nil {
		return ProviderSettings{}, err
	}
	name := strings.TrimSpace(edit.CredentialName)
	path := provider.CredentialFilePath()
	current, loadErr := config.LoadFileConfig(config.FileConfigPath())
	if loadErr != nil {
		return ProviderSettings{}, loadErr
	}
	_, legacyKey, _ := launchcfg.ProviderConnection(current, edit.Provider)
	if current.APIKey != "" && legacyKey == "" {
		if owner := launchcfg.LegacyProviderName(current); owner != "" {
			return ProviderSettings{}, fmt.Errorf("save settings for %q first to migrate its legacy API key before switching defaults", owner)
		}
		return ProviderSettings{}, fmt.Errorf("set the legacy API key's provider in config.toml before migrating it")
	}
	if edit.RemoveCredential {
		// Drop the config reference first. If deleting the secret then fails,
		// the harmless failure mode is an unreferenced value, never a dangling
		// config that makes the next launch unauthenticated.
	} else if value := strings.TrimSpace(edit.NewCredential); value != "" {
		if name == "" {
			name = edit.Provider + "-default"
		}
		if err := provider.SaveCredential(path, name, value); err != nil {
			return ProviderSettings{}, err
		}
	} else if strings.TrimSpace(legacyKey) != "" {
		// A pre-named-credential config may still hold api_key in plaintext.
		// Saving Settings is the migration boundary: encrypt it first, then let
		// SetProviderDefaults remove the literal only after that succeeds.
		name = firstSetting(name, edit.Provider+"-default")
		if err := provider.SaveCredential(path, name, legacyKey); err != nil {
			return ProviderSettings{}, err
		}
	} else if name != "" {
		if _, err := provider.ResolveCredentialReference(path, name); err != nil {
			return ProviderSettings{}, fmt.Errorf("credential %q is not stored; enter a new key or choose an existing reference", name)
		}
	}

	configName := name
	if edit.RemoveCredential {
		configName = ""
	}
	if err := config.SetProviderDefaults(config.ProviderDefaults{
		Provider: edit.Provider, Model: edit.Model, BaseURL: edit.BaseURL,
		Protocol: edit.Protocol, Credential: configName, ThinkingLevel: edit.ThinkingLevel,
	}); err != nil {
		return ProviderSettings{}, err
	}
	if edit.RemoveCredential {
		shared := current.Credential == name && launchcfg.LegacyProviderName(current) != edit.Provider
		for owner, profile := range current.ProviderProfiles {
			shared = shared || launchcfg.CanonicalProviderName(owner) != edit.Provider && profile.Credential == name
		}
		if name != "" && !shared {
			if err := provider.DeleteCredential(path, name); err != nil {
				return ProviderSettings{}, err
			}
		}
		name = ""
	}
	if apply {
		edit.CredentialName = name
		if err := c.applyProviderSettings(edit); err != nil {
			return ProviderSettings{}, err
		}
	}
	return c.LoadProviderSettings()
}

// TestProviderSettings resolves the same provider and credential a save would
// use, then asks its catalog endpoint for a model list. It is the only settings
// action that performs network I/O.
func (c *Controller) TestProviderSettings(ctx context.Context, edit ProviderSettingsEdit) error {
	edit, prov, resolved, err := normalizeProviderEdit(edit)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	creds := provider.NewCredentialStore(nil)
	if value := strings.TrimSpace(edit.NewCredential); value != "" {
		creds.SetOverride(resolved, value)
	} else {
		result := launchcfg.ResolveCredential(resolved, "", edit.CredentialName)
		if value := result.ResolvedValue(); value != "" {
			creds.SetOverride(resolved, value)
		}
	}
	live := &cli.LiveConfig{
		Model: edit.Model, ProviderName: resolved, Provider: prov,
		BaseURL: edit.BaseURL, Protocol: edit.Protocol,
	}
	ids, err := prompts.EnsureModelCatalog(live, creds)
	if err != nil {
		return fmt.Errorf("test connection: %w", err)
	}
	if len(ids) == 0 {
		return fmt.Errorf("test connection: provider returned no models")
	}
	return nil
}

func (c *Controller) applyProviderSettings(edit ProviderSettingsEdit) error {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return ErrBusy
	}
	oldProvider := c.live.ProviderName
	c.mu.Unlock()

	prov, resolved, err := provider.ResolveProvider(edit.Model, edit.BaseURL, edit.Protocol, edit.Provider, os.Getenv)
	if err != nil {
		return err
	}
	result := launchcfg.ResolveCredential(resolved, "", edit.CredentialName)
	c.mu.Lock()
	c.creds.ClearOverride(oldProvider)
	c.creds.ClearOverride(resolved)
	if value := result.ResolvedValue(); value != "" {
		c.creds.SetOverride(resolved, value)
	}
	c.live.Model, c.live.ProviderName, c.live.Provider = edit.Model, resolved, prov
	c.live.BaseURL, c.live.Protocol = edit.BaseURL, edit.Protocol
	if thinking := settingThinkingLevel(edit.ThinkingLevel); thinking != "" {
		c.live.ThinkingLevel = thinking
	}
	c.live.RefreshContextWindow()
	c.header.Model, c.header.Provider = c.live.Model, c.live.ProviderName
	c.status.Model, c.status.Provider = c.live.Model, c.live.ProviderName
	c.status.Thinking, c.status.ContextLimit = c.live.ThinkingLevel, c.live.ContextWindow
	c.mu.Unlock()
	c.emitStatus()
	c.addNotice(fmt.Sprintf("Provider settings applied: %s / %s.", resolved, edit.Model), false)
	return nil
}

func normalizeProviderEdit(edit ProviderSettingsEdit) (ProviderSettingsEdit, provider.Provider, string, error) {
	edit.Provider = launchcfg.CanonicalProviderName(edit.Provider)
	edit.BaseURL = strings.TrimSpace(edit.BaseURL)
	edit.Protocol = strings.TrimSpace(edit.Protocol)
	edit.CredentialName = strings.TrimSpace(edit.CredentialName)
	edit.ThinkingLevel = strings.TrimSpace(edit.ThinkingLevel)
	if edit.Provider == "" {
		return edit, nil, "", fmt.Errorf("provider is required")
	}
	spec, ok := provider.LookupProviderSpec(edit.Provider)
	if !ok {
		return edit, nil, "", fmt.Errorf("unknown provider %q", edit.Provider)
	}
	edit.Model = providerSettingsModel(edit.Model, spec)
	if edit.Protocol != "" {
		canonical, err := provider.NormalizeProtocol(edit.Protocol)
		if err != nil {
			return edit, nil, "", err
		}
		if !provider.AllowsProtocol(spec, canonical) {
			return edit, nil, "", fmt.Errorf("provider %q does not support protocol %q (available: %s)", edit.Provider, edit.Protocol, provider.ProtocolChoices(spec))
		}
		edit.Protocol = canonical
	}
	if err := validateProviderURL(edit.BaseURL); err != nil {
		return edit, nil, "", err
	}
	if edit.ThinkingLevel != "" && settingThinkingLevel(edit.ThinkingLevel) == "" {
		return edit, nil, "", fmt.Errorf("unknown reasoning level %q", edit.ThinkingLevel)
	}
	prov, resolved, err := provider.ResolveProvider(edit.Model, edit.BaseURL, edit.Protocol, edit.Provider, os.Getenv)
	return edit, prov, resolved, err
}

func providerSettingsModel(model string, spec provider.ProviderSpec) string {
	return firstSetting(provider.CanonicalizeModel(strings.TrimSpace(model)), spec.DefaultModel)
}

func validateProviderURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("base URL is invalid")
	}
	if strings.EqualFold(u.Scheme, "https") {
		return nil
	}
	host := u.Hostname()
	if strings.EqualFold(u.Scheme, "http") && (host == "localhost" || net.ParseIP(host).IsLoopback()) {
		return nil
	}
	return fmt.Errorf("base URL must use HTTPS (loopback HTTP is allowed)")
}

func settingThinkingLevel(value string) agentcore.ThinkingLevel {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "off":
		return agentcore.ThinkingOff
	case "minimal":
		return agentcore.ThinkingMinimal
	case "low":
		return agentcore.ThinkingLow
	case "medium":
		return agentcore.ThinkingMedium
	case "high":
		return agentcore.ThinkingHigh
	case "xhigh":
		return agentcore.ThinkingXHigh
	case "max":
		return agentcore.ThinkingMax
	default:
		return ""
	}
}

func firstSetting(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func appendUniqueSetting(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
