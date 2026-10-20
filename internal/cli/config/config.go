// Package config implements golder's optional user config file at
// $GOLDER_HOME/config.toml (~/.golder/config.toml by default) plus the
// provider-agnostic base-url env-var name derivation. Values in the file
// replace golder's built-in defaults, but an explicit command-line flag always
// wins over the file:
//
//	command-line flag > config.toml > built-in default
//
// A missing file is not an error (defaults apply); a malformed file is surfaced
// to the caller so it can warn rather than silently ignore user intent.
//
// The package is intentionally free of any cliOptions/run-assembly concern: it
// only loads and decodes the file and derives env-var names. Overlaying a
// FileConfig onto the parsed CLI options lives in cmd/golder, alongside the
// options struct it mutates.
package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/getan/golder/internal/golderhome"
)

// FileConfig is the on-disk shape of config.toml. Every field is optional; an
// absent (zero-value) field leaves the corresponding default/flag untouched.
// Keys are snake_case to read naturally in TOML.
type FileConfig struct {
	Model   string `toml:"model"`
	BaseURL string `toml:"base_url"`
	APIKey  string `toml:"api_key"`
	// Credential is a named credential REFERENCE (issue #568): the literal key
	// lives in $GOLDER_HOME/.credentials.yaml (mode 0600) under this name, and
	// config.toml carries only the name. It resolves to the API-key tier, below
	// an explicit --api-key flag. Empty means no reference.
	Credential    string `toml:"credential"`
	Protocol      string `toml:"protocol"`
	Provider      string `toml:"provider"`
	ThinkingLevel string `toml:"thinking_level"`
	OutputFormat  string `toml:"output_format"`
	NoTools       bool   `toml:"no_tools"`
	NoSkills      bool   `toml:"no_skills"`
	Approve       bool   `toml:"approve"`
	SystemPrompt  string `toml:"system_prompt"`
	// AllowedTools and DisallowedTools are the tool-level admission boundary:
	// the config-file tier of --allowed-tools / --disallowed-tools. Names match
	// case-insensitively and DisallowedTools wins when a name appears in both.
	// A CLI flag replaces the file value wholesale rather than merging with it,
	// so passing --allowed-tools can widen a boundary the file narrowed.
	AllowedTools    []string `toml:"allowed_tools"`
	DisallowedTools []string `toml:"disallowed_tools"`
	// Prompts is the config.toml `prompts` array: paths (files or dirs) to load
	// prompt templates from at the settings tier (mirrors pi's settings prompts).
	Prompts []string `toml:"prompts"`
	// Memory, Checkpoint, and Compaction are nested TOML tables for the
	// persistent-memory / infinite-context feature. They are pure config
	// plumbing here; defaults/parsing live in memory.go (Resolve* helpers) and
	// the overlay into runtime options lives in cmd/golder. See
	// tasks/spec-persistent-memory-infinite-context.md §3/§4/§5.2.
	Memory     MemoryConfig     `toml:"memory"`
	Checkpoint CheckpointConfig `toml:"checkpoint"`
	Compaction CompactionConfig `toml:"compaction"`
	// Dream is the [dream] TOML table for the /dream memory-consolidation
	// feature. Pure config plumbing here; defaults/normalization live in
	// internal/dream (Config). See tasks/spec-dream-memory-consolidation.md
	// §3.3.
	Dream DreamConfig `toml:"dream"`
	// Redact is the [redact] table: golder's embedded DLP layer. It is applied
	// in-process before text reaches the provider, so there is no proxy to run.
	Redact RedactConfig `toml:"redact"`
}

// RedactConfig is the [redact] TOML table.
type RedactConfig struct {
	// Mode is off | observe | redact | block. Empty means redact (the safe
	// default); observe records findings without changing text.
	Mode string `toml:"mode"`
	// Entropy is the minimum Shannon entropy for generic NAME=value matches.
	// Zero means the engine default (3.5).
	Entropy float64 `toml:"entropy"`
	// Audit is the JSONL audit path. Empty means $GOLDER_HOME/redact-audit.jsonl.
	Audit string `toml:"audit"`
	// Allow lists exact values that must never be redacted.
	Allow []string `toml:"allow"`
}

// DreamConfig is the [dream] TOML table for /dream memory consolidation.
// Enabled is a pointer so an absent key (nil) is distinguishable from an
// explicit false: nil is treated as true, only enabled = false disables
// auto-trigger. IntervalDays and RecentSessions use zero as "apply default"
// (7 and 20 respectively); normalization lives in dream.Config.
type DreamConfig struct {
	Enabled        *bool `toml:"enabled"`
	IntervalDays   int   `toml:"interval_days"`
	RecentSessions int   `toml:"recent_sessions"`
}

// FileConfigPath returns the path to the user config file: config.toml in the
// golder state root ($GOLDER_HOME, else ~/.golder). It returns "" when the root
// cannot be resolved, so the caller treats the file as absent.
func FileConfigPath() string { return golderhome.File(golderhome.ConfigFile) }

// LoadFileConfig reads and decodes config.toml. A missing file (or an empty
// path) returns a zero config with no error; a malformed file is an error.
func LoadFileConfig(path string) (FileConfig, error) {
	if path == "" {
		return FileConfig{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return FileConfig{}, nil
		}
		return FileConfig{}, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg FileConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return FileConfig{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	return cfg, nil
}

// GenericBaseURLEnvVar derives the generic base-url override env var name for a
// provider: the provider name uppercased with hyphens rewritten to underscores,
// suffixed with _BASE_URL. For example "zai-coding-cn" → "ZAI_CODING_CN_BASE_URL"
// and "deepseek" → "DEEPSEEK_BASE_URL". An empty provider name yields "".
//
// It lives here (not with ResolveBaseURL) because it is a pure name derivation
// with no dependency on the provider registry — the provider-agnostic part of
// base-url resolution. ResolveBaseURL itself lives in internal/provider.
func GenericBaseURLEnvVar(providerName string) string {
	n := strings.TrimSpace(providerName)
	if n == "" {
		return ""
	}
	n = strings.ReplaceAll(n, "-", "_")
	return strings.ToUpper(n) + "_BASE_URL"
}
