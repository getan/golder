// Package redact implements golder's embedded data-loss-prevention layer: it
// finds credential-shaped values in text that is about to enter the model's
// context (tool output, prompts, compaction summaries, memory) and replaces
// them with stable placeholders before the provider ever sees them.
//
// The rule set below is a curated subset of the shapes gitleaks and similar
// scanners look for. It is deliberately small and dependency-free: the engine
// uses an anchor automaton plus per-rule regexes instead of running every
// regex over every byte, so clean text costs one linear pass.
package redact

import "regexp"

// rule is one credential shape. Anchors are literal substrings that must appear
// in the text before Pattern is tried; the automaton matches them
// case-insensitively. Group selects the capture to replace (0 = the whole
// match), so an assignment rule can keep its NAME= prefix while redacting only
// the value.
type rule struct {
	id       string
	priority int // lower wins when two spans overlap
	anchors  []string
	pattern  *regexp.Regexp
	group    int
	minLen   int
	maxLen   int
	entropy  float64
	// requiresAssign is a fast pre-check: the anchor must be followed by '='
	// or ':' nearby. Without it the word "token" would trigger a window regex
	// on every occurrence in ordinary prose.
	requiresAssign bool
	// wholeText rules (PEM blocks) may span far more than the window, so their
	// regex runs once over the full text after the anchor is seen.
	wholeText bool
}

// builtinRules is the compiled rule table. Priorities: 0 known/private key,
// 1 vendor-prefixed tokens that are easy to confuse (anthropic before openai),
// 2 other vendor tokens, 9 generic assignments.
var builtinRules = []*rule{
	{
		id:        "private_key",
		priority:  0,
		anchors:   []string{"-----begin"},
		pattern:   regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`),
		minLen:    32,
		maxLen:    65536,
		wholeText: true,
	},
	{
		id:       "anthropic_key",
		priority: 1,
		anchors:  []string{"sk-ant-"},
		pattern:  regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}\b`),
		minLen:   27,
		maxLen:   512,
	},
	{
		id:       "github_pat",
		priority: 1,
		anchors:  []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_"},
		pattern:  regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,255}|github_pat_[A-Za-z0-9_]{22,255})\b`),
		minLen:   40,
		maxLen:   512,
	},
	{
		id:       "gitlab_pat",
		priority: 1,
		anchors:  []string{"glpat-"},
		pattern:  regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}\b`),
		minLen:   26,
		maxLen:   512,
	},
	{
		id:       "slack_token",
		priority: 1,
		anchors:  []string{"xoxb-", "xoxp-", "xoxa-", "xoxr-", "xoxs-"},
		pattern:  regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`),
		minLen:   15,
		maxLen:   512,
	},
	{
		id:       "stripe_key",
		priority: 1,
		anchors:  []string{"sk_live_", "rk_live_"},
		pattern:  regexp.MustCompile(`\b(?:sk|rk)_live_[A-Za-z0-9]{20,}\b`),
		minLen:   28,
		maxLen:   512,
	},
	{
		id:       "openai_key",
		priority: 3,
		anchors:  []string{"sk-"},
		pattern:  regexp.MustCompile(`\bsk-(?:proj-|svcacct-)?[A-Za-z0-9_-]{20,}\b`),
		minLen:   23,
		maxLen:   512,
	},
	{
		id:       "aws_access_key",
		priority: 2,
		anchors:  []string{"akia", "asia"},
		pattern:  regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),
		minLen:   20,
		maxLen:   20,
	},
	{
		id:       "aws_secret",
		priority: 2,
		anchors:  []string{"aws_secret", "aws-secret", "secret_access_key"},
		pattern:  regexp.MustCompile(`(?i)aws_?secret_?access_?key\s*[:=]\s*["']?([A-Za-z0-9/+=]{40})`),
		group:    1,
		minLen:   40,
		maxLen:   40,
	},
	{
		id:       "gcp_api_key",
		priority: 2,
		anchors:  []string{"aiza"},
		pattern:  regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`),
		minLen:   39,
		maxLen:   39,
	},
	{
		id:       "azure_key",
		priority: 2,
		anchors:  []string{"accountkey", "sharedaccesskey"},
		pattern:  regexp.MustCompile(`(?i)(?:AccountKey|SharedAccessKey)=([A-Za-z0-9+/=]{40,})`),
		group:    1,
		minLen:   40,
		maxLen:   512,
	},
	{
		id:       "npm_token",
		priority: 2,
		anchors:  []string{"npm_"},
		pattern:  regexp.MustCompile(`\bnpm_[A-Za-z0-9]{36}\b`),
		minLen:   40,
		maxLen:   40,
	},
	{
		id:       "pypi_token",
		priority: 2,
		anchors:  []string{"pypi-"},
		pattern:  regexp.MustCompile(`\bpypi-AgEIcHlwaS5vcmc[A-Za-z0-9_-]{20,}\b`),
		minLen:   40,
		maxLen:   512,
	},
	{
		id:       "huggingface",
		priority: 2,
		anchors:  []string{"hf_"},
		pattern:  regexp.MustCompile(`\bhf_[A-Za-z0-9]{30,}\b`),
		minLen:   33,
		maxLen:   512,
	},
	{
		id:       "jwt",
		priority: 2,
		anchors:  []string{"eyj"},
		pattern:  regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`),
		minLen:   40,
		maxLen:   8192,
	},
	{
		id:       "db_url",
		priority: 2,
		anchors:  []string{"postgres://", "postgresql://", "mysql://", "mongodb://", "mongodb+srv://", "redis://", "rediss://"},
		pattern:  regexp.MustCompile(`\b(?:postgres(?:ql)?|mysql|mongodb(?:\+srv)?|redis|rediss)://[^\s:@/]+:([^\s@/]{3,})@[^\s]+`),
		group:    1,
		minLen:   3,
		maxLen:   512,
	},
	{
		id:       "bearer_token",
		priority: 8,
		anchors:  []string{"bearer "},
		pattern:  regexp.MustCompile(`(?i)\bBearer\s+([A-Za-z0-9._~+/=-]{16,})`),
		group:    1,
		minLen:   16,
		maxLen:   4096,
		entropy:  3.0,
	},
	{
		id:             "generic_assignment",
		priority:       9,
		anchors:        []string{"password", "passwd", "secret", "token", "api_key", "apikey", "access_key", "private_key", "client_secret"},
		pattern:        regexp.MustCompile(`(?i)(?:password|passwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|client[_-]?secret)\s*[:=]\s*["']?([^\s"'\\]{8,})`),
		group:          1,
		minLen:         8,
		maxLen:         512,
		entropy:        3.5, // overridden by Config.Entropy
		requiresAssign: true,
	},
}
