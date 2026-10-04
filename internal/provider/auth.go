// This file implements credential resolution (US-012): API key lookup from
// environment variables and a config file (per provider), plus an OAuth token
// source that refreshes short-lived tokens on expiry. The resolver satisfies
// the LoopConfig.GetAPIKey shape (func(ctx, provider) string) so the agent loop
// can obtain a fresh key per request.
//
// Security (FR: secret values are not written to logs): secret values are never logged or embedded in
// error messages. Errors and String()/redaction helpers reference credentials
// by key name / provider only.
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// APIKeyConfig is the on-disk config-file shape: a map of provider name to API
// key. It is parsed from JSON and holds only static keys (OAuth lives in
// TokenSource). Values are secrets and must not be logged.
type APIKeyConfig struct {
	// Keys maps provider name → API key.
	Keys map[string]string `json:"keys"`
}

// LoadAPIKeyConfig parses an APIKeyConfig from JSON bytes (e.g. a config file).
func LoadAPIKeyConfig(data []byte) (*APIKeyConfig, error) {
	var cfg APIKeyConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("auth: parse api key config: %w", err)
	}
	if cfg.Keys == nil {
		cfg.Keys = make(map[string]string)
	}
	return &cfg, nil
}

// LoadAPIKeyConfigFile reads and parses an APIKeyConfig from a file path. A
// missing file is not an error — it returns an empty config so env/OAuth can
// still resolve keys.
func LoadAPIKeyConfigFile(path string) (*APIKeyConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &APIKeyConfig{Keys: make(map[string]string)}, nil
		}
		return nil, fmt.Errorf("auth: read api key config %q: %w", path, err)
	}
	return LoadAPIKeyConfig(data)
}

// envAPIKey returns the API key for a provider from the environment. It derives
// the candidate variable names from the provider registry (the single source of
// truth: LookupProviderSpec(provider).EnvVars, in precedence order), then falls
// back to a generic <PROVIDER>_API_KEY when the provider is unknown or none of
// its registered vars are set. Returns "" when no value is present.
func envAPIKey(provider string) string {
	if name, ok := EnvCredentialVar(provider); ok {
		return os.Getenv(name)
	}
	return ""
}

// EnvCredentialVar reports the name of the environment variable holding a
// credential for a provider, without returning its value. Candidates come from
// the provider registry (EnvVars, in precedence order), then the generic
// <PROVIDER>_API_KEY fallback. A variable that is set but empty does not count.
// The name is safe to display; the value never leaves this call.
func EnvCredentialVar(provider string) (string, bool) {
	if spec, ok := LookupProviderSpec(provider); ok {
		for _, name := range spec.EnvVars {
			if v, ok := os.LookupEnv(name); ok && strings.TrimSpace(v) != "" {
				return name, true
			}
		}
	}
	generic := strings.ToUpper(provider) + "_API_KEY"
	if v, ok := os.LookupEnv(generic); ok && strings.TrimSpace(v) != "" {
		return generic, true
	}
	return "", false
}

// TokenSource yields an access token, refreshing it when expired. It models an
// OAuth credential whose access token is short-lived (FR-15: getApiKey refreshes
// on expiry). It is safe for concurrent use.
type TokenSource struct {
	mu           sync.Mutex
	accessToken  string
	expiry       time.Time
	refreshToken string
	// Refresh exchanges the current refresh token for a new access token. It
	// returns the new access token, its expiry, and (optionally) a rotated
	// refresh token. Required for a TokenSource to refresh; nil means the token
	// is static and never refreshed.
	Refresh func(ctx context.Context, refreshToken string) (OAuthToken, error)
	// Now is injectable for testing; defaults to time.Now.
	Now func() time.Time
	// Leeway refreshes the token this long before its actual expiry to avoid
	// racing the boundary. Defaults to 30s.
	Leeway time.Duration
}

// OAuthToken is the result of an OAuth exchange/refresh. Values are secrets.
type OAuthToken struct {
	AccessToken  string
	RefreshToken string
	Expiry       time.Time
}

// NewTokenSource builds a TokenSource seeded with an initial token and a refresh
// function. refresh may be nil for a static (never-expiring) token.
func NewTokenSource(initial OAuthToken, refresh func(ctx context.Context, refreshToken string) (OAuthToken, error)) *TokenSource {
	return &TokenSource{
		accessToken:  initial.AccessToken,
		expiry:       initial.Expiry,
		refreshToken: initial.RefreshToken,
		Refresh:      refresh,
	}
}

func (t *TokenSource) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

// defaultTokenLeeway is how far before an OAuth token's expiry it is treated as
// already expired, so a refresh happens before a request rather than mid-flight.
const defaultTokenLeeway = 30 * time.Second

func (t *TokenSource) leeway() time.Duration {
	if t.Leeway > 0 {
		return t.Leeway
	}
	return defaultTokenLeeway
}

// expired reports whether the access token is missing or within leeway of its
// expiry. A zero expiry means "never expires" (static token).
func (t *TokenSource) expired() bool {
	if t.accessToken == "" {
		return true
	}
	if t.expiry.IsZero() {
		return false
	}
	return !t.now().Before(t.expiry.Add(-t.leeway()))
}

// Token returns a valid access token, refreshing it when expired. It errors if
// a refresh is needed but no Refresh func is set, or if Refresh fails. The
// returned error never contains the token value.
func (t *TokenSource) Token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.expired() {
		return t.accessToken, nil
	}
	if t.Refresh == nil {
		return "", fmt.Errorf("auth: token expired and no refresh function configured")
	}
	tok, err := t.Refresh(ctx, t.refreshToken)
	if err != nil {
		return "", fmt.Errorf("auth: token refresh failed: %w", err)
	}
	t.accessToken = tok.AccessToken
	t.expiry = tok.Expiry
	if tok.RefreshToken != "" {
		t.refreshToken = tok.RefreshToken
	}
	return t.accessToken, nil
}

// CredentialStore resolves API keys per provider from three layers, in order:
// OAuth token source (if registered), environment variable, config file. It
// implements the LoopConfig.GetAPIKey shape via GetAPIKey.
//
// It is safe for concurrent use.
type CredentialStore struct {
	mu        sync.RWMutex
	config    *APIKeyConfig
	sources   map[string]*TokenSource // provider → OAuth token source
	overrides map[string]string       // provider → explicit key (highest static priority)
}

// NewCredentialStore builds a store over an optional config file. A nil config
// is treated as empty.
func NewCredentialStore(config *APIKeyConfig) *CredentialStore {
	if config == nil {
		config = &APIKeyConfig{Keys: make(map[string]string)}
	}
	return &CredentialStore{
		config:    config,
		sources:   make(map[string]*TokenSource),
		overrides: make(map[string]string),
	}
}

// SetOverride records an explicit API key for a provider that wins over the
// environment variable and config file (but not a live OAuth token, which is
// auto-refreshed). It is the seam for a CLI --api-key flag: an empty key is
// ignored so a bare flag does not clobber env/config resolution.
func (c *CredentialStore) SetOverride(provider, key string) {
	if key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.overrides[provider] = key
}

// RegisterOAuth registers an OAuth TokenSource for a provider. Once registered,
// GetAPIKey prefers the (auto-refreshing) OAuth token over static keys.
func (c *CredentialStore) RegisterOAuth(provider string, src *TokenSource) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sources[provider] = src
}

// GetAPIKey resolves the API key for a provider. Resolution order: OAuth token
// (refreshed on expiry) → explicit override (--api-key) → environment variable
// → config file. Returns "" when no credential is available. This matches
// LoopConfig.GetAPIKey so it can be assigned directly.
//
// On OAuth refresh failure it falls back to override/env/config rather than
// returning a secret-bearing error; the empty return lets the caller fall back
// to a static key. It never logs secret values.
func (c *CredentialStore) GetAPIKey(ctx context.Context, provider string) string {
	c.mu.RLock()
	src := c.sources[provider]
	override := c.overrides[provider]
	cfgKey := ""
	if c.config != nil {
		cfgKey = c.config.Keys[provider]
	}
	c.mu.RUnlock()

	if src != nil {
		if tok, err := src.Token(ctx); err == nil && tok != "" {
			return tok
		}
		// Refresh failed → fall through to static layers.
	}
	if override != "" {
		return override
	}
	if env := envAPIKey(provider); env != "" {
		return env
	}
	return cfgKey
}

// HasCredential reports whether any credential (OAuth/env/config) is available
// for a provider, without exposing the value.
func (c *CredentialStore) HasCredential(ctx context.Context, provider string) bool {
	return c.GetAPIKey(ctx, provider) != ""
}

// CredentialStatus reports which credential sources hold a value for a
// provider, in resolution priority order. It is a presence probe: no secret
// value is retrieved or returned, only which layer can supply one.
type CredentialStatus struct {
	// OAuth reports a registered OAuth token source (auto-refreshing).
	OAuth bool
	// Override reports an explicit key (--api-key).
	Override bool
	// EnvVar names the environment variable holding a value, or "" for none.
	EnvVar string
	// Config reports a key in the config-file layer.
	Config bool
}

// Available reports whether any layer can supply a credential.
func (s CredentialStatus) Available() bool {
	return s.OAuth || s.Override || s.EnvVar != "" || s.Config
}

// Source returns a short, value-free label for the highest-priority layer that
// holds a credential, or "" when none does. Env sources return the variable
// name (safe to display), never the value.
func (s CredentialStatus) Source() string {
	switch {
	case s.OAuth:
		return "oauth"
	case s.Override:
		return "--api-key"
	case s.EnvVar != "":
		return s.EnvVar
	case s.Config:
		return "config file"
	default:
		return ""
	}
}

// Status probes the credential layers for a provider without reading any
// secret into a caller-visible value. A nil store (or empty config) reports no
// config/override/OAuth source; the env layer is always probed.
func (c *CredentialStore) Status(provider string) CredentialStatus {
	var st CredentialStatus
	if name, ok := EnvCredentialVar(provider); ok {
		st.EnvVar = name
	}
	if c == nil {
		return st
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	st.OAuth = c.sources[provider] != nil
	st.Override = c.overrides[provider] != ""
	if c.config != nil {
		st.Config = c.config.Keys[provider] != ""
	}
	return st
}
