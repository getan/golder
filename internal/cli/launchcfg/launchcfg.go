// Package launchcfg owns the provider-facing part of launch configuration that
// every Golder front-end shares. It deliberately stops before run assembly: the
// CLI, TUI and GUI may build different views, but they must agree on flag/file
// precedence and on where a credential came from.
package launchcfg

import (
	"fmt"
	"strings"

	"github.com/getan/golder/internal/cli/config"
	"github.com/getan/golder/internal/provider"
)

// Values is the provider-facing launch state after defaults but before the
// config file is overlaid. APIKey is secret and must never be formatted or
// logged; the other fields are ordinary settings.
type Values struct {
	Model         string
	Provider      string
	BaseURL       string
	Protocol      string
	APIKey        string
	CredentialRef string
	ThinkingLevel string
}

// Changed reports whether a command-line flag was explicitly supplied. A nil
// function means no flag was supplied, which is what a settings-only GUI uses.
type Changed func(name string) bool

// Overlay applies the shared CLI > config.toml > built-in precedence for the
// provider-facing fields. A literal api_key wins over a named reference for
// backward compatibility, but new writers should save only CredentialRef.
func Overlay(v Values, cfg config.FileConfig, changed Changed) Values {
	set := func(name string) bool { return changed != nil && changed(name) }
	if cfg.Model != "" && !set("model") {
		v.Model = cfg.Model
	}
	if cfg.Provider != "" && !set("provider") {
		v.Provider = cfg.Provider
	}
	if cfg.BaseURL != "" && !set("base-url") {
		v.BaseURL = cfg.BaseURL
	}
	if cfg.Protocol != "" && !set("protocol") {
		v.Protocol = cfg.Protocol
	}
	if cfg.APIKey != "" && !set("api-key") {
		v.APIKey = cfg.APIKey
		v.CredentialRef = ""
	} else if cfg.Credential != "" && !set("api-key") {
		v.CredentialRef = cfg.Credential
	}
	if cfg.ThinkingLevel != "" && !set("thinking-level") {
		v.ThinkingLevel = cfg.ThinkingLevel
	}
	return v
}

// CredentialSource is safe display metadata. It never contains the credential.
type CredentialSource string

const (
	CredentialMissing     CredentialSource = "missing"
	CredentialExplicit    CredentialSource = "explicit"
	CredentialNamed       CredentialSource = "named"
	CredentialEnvironment CredentialSource = "environment"
)

// CredentialStatus tells a UI whether authentication is available and where it
// came from, without revealing a value.
type CredentialStatus struct {
	Available bool
	Source    CredentialSource
	// Name is the named-credential reference when Source is named.
	Name string
	// EnvVar is the inherited environment variable when Source is environment.
	EnvVar string
}

// CredentialResult is the launch-time credential resolution. A value is returned
// only for an explicit value or a successfully loaded named reference; ambient
// environment credentials stay ambient and are read by CredentialStore.
type CredentialResult struct {
	apiKey   string
	Status   CredentialStatus
	Warnings []string
}

// ResolvedValue returns the launch-only secret for passing into the provider
// store. Callers must not display or persist it.
func (r CredentialResult) ResolvedValue() string { return r.apiKey }

// ResolveCredential applies the shared static credential precedence:
// explicit/config api_key > named credential > inherited provider environment.
// A broken named reference warns and falls through to the environment, matching
// the CLI's historical behavior.
func ResolveCredential(providerName, apiKey, credentialRef string) CredentialResult {
	if key := strings.TrimSpace(apiKey); key != "" {
		return CredentialResult{apiKey: key, Status: CredentialStatus{Available: true, Source: CredentialExplicit}}
	}

	var out CredentialResult
	name := strings.TrimSpace(credentialRef)
	if name != "" {
		path := provider.CredentialFilePath()
		if provider.CredentialFilePermissionsWarn(path) {
			out.Warnings = append(out.Warnings, fmt.Sprintf("%s is readable by group/other; chmod 600 recommended", path))
		}
		key, err := provider.ResolveCredentialReference(path, name)
		if err == nil {
			out.apiKey = key
			out.Status = CredentialStatus{Available: true, Source: CredentialNamed, Name: name}
			return out
		}
		out.Warnings = append(out.Warnings, fmt.Sprintf("credential %q: %v", name, err))
	}

	if envVar, ok := provider.EnvCredentialVar(strings.TrimSpace(providerName)); ok {
		out.Status = CredentialStatus{Available: true, Source: CredentialEnvironment, EnvVar: envVar}
		return out
	}
	out.Status = CredentialStatus{Source: CredentialMissing, Name: name}
	return out
}
