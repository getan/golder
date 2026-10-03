// Package permissions defines pigo's approval modes and the live, mutable
// state that tracks the active one. It is a leaf package: standard library
// only, no imports from judge/trust/runtime, so every layer (the risk gate,
// the bash tool, the slash command) can read the mode without an import
// cycle.
//
// The four modes mirror codex's approval presets, adapted to pigo's gate:
//
//   - read-only:   mutating tools (bash, apply_patch) are blocked outright.
//   - ask:         the reviewer grades each mutating call; Allow runs, the
//     rest asks the human (or is denied when no prompt exists).
//   - auto:        the reviewer decides: Allow/Confirm run, Sandbox runs
//     isolated, Deny is blocked — each non-trivial decision is announced
//     with a one-line rationale.
//   - full-access: no review and no sandbox; only the static hard-deny
//     floor still applies.
package permissions

import (
	"strings"
	"sync"
)

// Mode is one of the four approval modes.
type Mode int

const (
	// ReadOnly blocks mutating tools: the model may inspect but not change.
	ReadOnly Mode = iota
	// Ask grades each mutating call and asks the human about anything that is
	// not clearly low-risk. Without an interactive prompt the call is denied.
	Ask
	// Auto lets the reviewer decide without a human: reviewed calls run, risky
	// ones run sandboxed, dangerous ones are blocked, and each decision is
	// announced with its rationale.
	Auto
	// FullAccess disables review and sandboxing (the static floor remains).
	FullAccess
)

// String returns the canonical, lowercase mode name used by the CLI, the
// slash command, and the config/env layers.
func (m Mode) String() string {
	switch m {
	case ReadOnly:
		return "read-only"
	case Ask:
		return "ask"
	case FullAccess:
		return "full-access"
	default:
		return "auto"
	}
}

// Parse resolves a user-supplied mode name. It accepts the canonical names
// plus a few common shorthands ("readonly", "ro", "full"), case-insensitively.
// ok is false for anything unrecognized so callers can report a usage error
// instead of silently picking a default.
func Parse(s string) (Mode, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "read-only", "readonly", "read only", "ro":
		return ReadOnly, true
	case "ask", "confirm":
		return Ask, true
	case "auto", "review":
		return Auto, true
	case "full-access", "full", "fullaccess", "yolo":
		return FullAccess, true
	default:
		return Auto, false
	}
}

// FromEnv resolves the default mode from PIGO_PERMISSIONS. PIGO_JUDGE=off is
// honored as a legacy alias for full-access. ok is false when the variable is
// unset or unparseable, leaving the caller's own default in place.
func FromEnv(getenv func(string) string) (Mode, bool) {
	if getenv == nil {
		return Auto, false
	}
	if m, ok := Parse(getenv("PIGO_PERMISSIONS")); ok {
		return m, true
	}
	if strings.EqualFold(strings.TrimSpace(getenv("PIGO_JUDGE")), "off") {
		return FullAccess, true
	}
	return Auto, false
}

// Label returns the human-facing name of the mode in the given language
// ("zh" for Simplified Chinese, anything else English). UI surfaces use it so
// the /permissions output matches the language of the conversation.
func (m Mode) Label(lang string) string {
	if lang == "zh" {
		switch m {
		case ReadOnly:
			return "只读"
		case Ask:
			return "每次询问"
		case FullAccess:
			return "完全放行（危险）"
		default:
			return "自动审批（LLM 审查）"
		}
	}
	switch m {
	case ReadOnly:
		return "Read Only"
	case Ask:
		return "Ask"
	case FullAccess:
		return "Full Access (dangerous)"
	default:
		return "Auto (LLM review)"
	}
}

// Description returns the one-line explanation shown next to the label.
func (m Mode) Description(lang string) string {
	if lang == "zh" {
		switch m {
		case ReadOnly:
			return "只读文件；bash、apply_patch 等改动型工具会被直接拒绝"
		case Ask:
			return "改动型调用先由模型审查：低风险直接放行，其余询问你（无输入时拒绝）"
		case FullAccess:
			return "不做审查、不套沙箱；仅保留静态硬拒名单（sudo、rm -rf / 等）"
		default:
			return "由当前模型审查每次改动型调用：低风险放行、需隔离的进沙箱、高危拒绝，并附一行理由"
		}
	}
	switch m {
	case ReadOnly:
		return "Read files only; bash and apply_patch are blocked"
	case Ask:
		return "The model reviews changes first: low risk runs, the rest asks you"
	case FullAccess:
		return "No review, no sandbox; only the hard blocklist applies"
	default:
		return "The model reviews every change: low risk runs, risky runs sandboxed, dangerous blocked, with a reason"
	}
}

// State is the process-wide, concurrency-safe holder of the active mode. The
// gate, the bash tool and the slash command all read it live, so a
// /permissions switch applies to the very next tool call (even mid-run).
type State struct {
	mu   sync.RWMutex
	mode Mode
}

// New returns a State seeded with mode.
func New(mode Mode) *State { return &State{mode: mode} }

// Mode returns the active mode.
func (s *State) Mode() Mode {
	if s == nil {
		return FullAccess
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mode
}

// Set replaces the active mode.
func (s *State) Set(mode Mode) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mode = mode
}
