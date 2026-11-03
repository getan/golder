package config

import (
	"fmt"
	"os"
	"strings"
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

// SetProviderDefaults updates the provider-facing top-level keys in one atomic
// write while preserving comments, unrelated keys and tables. Saving a named
// credential removes a legacy literal api_key line so the secret has one home.
func SetProviderDefaults(s ProviderDefaults) error {
	path := FileConfigPath()
	if path == "" {
		return fmt.Errorf("cannot locate the config directory")
	}
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
	text := string(data)
	hadTrailingNewline := text == "" || strings.HasSuffix(text, "\n")
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if text == "" {
		lines = nil
	}
	lines = setTopLevel(lines, providerKey, s.Provider)
	lines = setTopLevel(lines, modelKey, s.Model)
	lines = setTopLevel(lines, baseURLKey, s.BaseURL)
	lines = setTopLevel(lines, protocolKey, s.Protocol)
	lines = setTopLevel(lines, credentialKey, s.Credential)
	lines = setTopLevel(lines, thinkingKey, s.ThinkingLevel)
	if s.Credential != "" {
		lines = setTopLevel(lines, apiKeyKey, "")
	}
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
	return writeConfigFile(path, out)
}
