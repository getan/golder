// Package pluginwire defines the host-side wire types for the legacy stdio
// plugin v1 protocol. Process lifecycle, trust and tool adaptation live in
// internal/extensions; this package is deliberately data-only.
package pluginwire

import "encoding/json"

// Manifest is returned by a legacy plugin's initialize call. Commands remain
// visible for compatibility diagnostics but are not invoked directly: business
// operations must be exposed as gated tools.
type Manifest struct {
	Name     string        `json:"name"`
	Version  string        `json:"version,omitempty"`
	Tools    []ToolSpec    `json:"tools,omitempty"`
	Commands []CommandSpec `json:"commands,omitempty"`
	Events   []string      `json:"events,omitempty"`
}

type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
}

type CommandSpec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Prompt      string `json:"prompt,omitempty"`
}

type CallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type CallResult struct {
	Content string `json:"content"`
	IsError bool   `json:"isError,omitempty"`
}

type EventParams struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}
