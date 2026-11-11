package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	baseURLKey    = "base_url"
	protocolKey   = "protocol"
	credentialKey = "credential"
	apiKeyKey     = "api_key"
)

// ProviderDefaults is the non-secret provider configuration a settings view
// edits. Credential is a named reference; API key values never belong here.
type ProviderDefaults struct {
	Provider      string
	Model         string
	BaseURL       string
	Protocol      string
	Credential    string
	ThinkingLevel string
}

// ProviderProfile stores shared connection settings and an optional override
// for one exact model, never an API key.
type ProviderProfile struct {
	BaseURL       string `toml:"base_url"`
	Credential    string `toml:"credential"`
	Protocol      string `toml:"protocol"`
	ProtocolModel string `toml:"protocol_model"`
}

func (p ProviderProfile) ProtocolFor(model string) string {
	if p.ProtocolModel != "" && p.ProtocolModel == model {
		return p.Protocol
	}
	return ""
}

// SetProviderDefaults selects the new launch default and stores its connection
// settings in a separate profile, preserving other providers and unrelated keys.
func SetProviderDefaults(s ProviderDefaults) error {
	path := FileConfigPath()
	if path == "" {
		return fmt.Errorf("cannot locate the config directory")
	}
	unlock, err := lockConfigFile(path)
	if err != nil {
		return err
	}
	defer unlock()
	s.Provider = strings.TrimSpace(s.Provider)
	s.Model = strings.TrimSpace(s.Model)
	s.BaseURL = strings.TrimSpace(s.BaseURL)
	s.Protocol = strings.TrimSpace(s.Protocol)
	s.Credential = strings.TrimSpace(s.Credential)
	s.ThinkingLevel = strings.TrimSpace(s.ThinkingLevel)
	if s.Provider == "" {
		return fmt.Errorf("cannot save provider settings without a provider")
	}

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read config %s: %w", path, err)
	}
	var old FileConfig
	if err := toml.Unmarshal(data, &old); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	text := string(data)
	hadTrailingNewline := text == "" || strings.HasSuffix(text, "\n")
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if text == "" {
		lines = nil
	}
	previous := strings.ToLower(strings.TrimSpace(old.Provider))
	if previous != "" && !strings.EqualFold(previous, s.Provider) {
		if old.APIKey != "" {
			return fmt.Errorf("save settings for %q first to migrate its legacy API key before switching defaults", previous)
		}
		if _, saved := old.ProviderProfiles[previous]; !saved && (old.BaseURL != "" || old.Credential != "" || old.Protocol != "") {
			lines = setProviderProfile(lines, previous, ProviderProfile{
				BaseURL: old.BaseURL, Credential: old.Credential, Protocol: old.Protocol, ProtocolModel: old.Model,
			})
		}
		if old.Model != "" && old.ModelDefaults[previous] == "" {
			lines = setTableValue(lines, modelDefaultsTable, previous, old.Model)
		}
	}
	lines = setTopLevel(lines, providerKey, s.Provider)
	lines = setTopLevel(lines, modelKey, s.Model)
	lines = setTopLevel(lines, baseURLKey, "")
	lines = setTopLevel(lines, protocolKey, "")
	lines = setTopLevel(lines, credentialKey, "")
	lines = setTopLevel(lines, thinkingKey, s.ThinkingLevel)
	lines = setTopLevel(lines, apiKeyKey, "")
	lines = setProviderProfile(lines, s.Provider, ProviderProfile{
		BaseURL: s.BaseURL, Credential: s.Credential, Protocol: s.Protocol, ProtocolModel: s.Model,
	})
	if s.Model != "" {
		lines = setTableValue(lines, modelDefaultsTable, s.Provider, s.Model)
	}
	if s.Model != "" && s.ThinkingLevel != "" {
		lines = editThinkingDefault(lines, s.Provider, s.Model, s.ThinkingLevel)
	}

	out := strings.Join(lines, "\n")
	if hadTrailingNewline && out != "" {
		out += "\n"
	}
	if out == text {
		return nil
	}
	var updated FileConfig
	if err := toml.Unmarshal([]byte(out), &updated); err != nil {
		return fmt.Errorf("cannot update provider settings without invalidating config %s: %w", path, err)
	}
	return writeConfigFile(path, out)
}

func setProviderProfile(lines []string, name string, p ProviderProfile) []string {
	table := "provider_profiles." + quoteTOMLKey(name)
	lines = setTableValue(lines, table, baseURLKey, p.BaseURL)
	lines = setTableValue(lines, table, credentialKey, p.Credential)
	lines = setTableValue(lines, table, protocolKey, p.Protocol)
	if p.Protocol == "" {
		p.ProtocolModel = ""
	}
	lines = setTableValue(lines, table, "protocol_model", p.ProtocolModel)
	// Keep an empty profile too: its connection settings were intentionally
	// cleared, and an old conventional credential must not be picked back up.
	if _, _, ok := findTOMLTable(lines, table); !ok {
		lines = append(lines, "", "["+table+"]")
	}
	return lines
}
