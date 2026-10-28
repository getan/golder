// Package extension defines the stable, protocol-independent vocabulary of a
// golder extension. It does not grant host authority and imports no internal API.
package extension

import (
	"context"
	"encoding/json"
)

type SourceRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

func (r SourceRef) String() string { return r.Kind + ":" + r.ID }

type ToolHints struct {
	ReadOnly    bool `json:"readOnly,omitempty"`
	Destructive bool `json:"destructive,omitempty"`
	Idempotent  bool `json:"idempotent,omitempty"`
}

type ToolSpec struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
	Hints        ToolHints       `json:"hints,omitempty"`
}

type Call struct {
	ID        string          `json:"id"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	// OperatorConfirmed is host-only state. MCP transports may attach the
	// namespaced metadata marker, never a field in model-controlled arguments.
	OperatorConfirmed bool `json:"-"`
}

const OperatorConfirmedMeta = "io.golder/operator-confirmed"
const ExecutablePathsEnv = "GOLDER_EXTENSION_EXECUTABLES"

// ContentBlock is data, never a request to execute or to change host policy.
type ContentBlock struct {
	Type     string `json:"type"` // text, image, resource_link
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"` // base64 for image
	MIMEType string `json:"mimeType,omitempty"`
	URI      string `json:"uri,omitempty"`
}

type Result struct {
	Content           []ContentBlock  `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError,omitempty"`
}

type Progress struct {
	Message string   `json:"message"`
	Current float64  `json:"current"`
	Total   *float64 `json:"total,omitempty"`
}

type ToolHandler func(context.Context, Call, func(Progress)) (Result, error)

func Text(text string) Result {
	return Result{Content: []ContentBlock{{Type: "text", Text: text}}}
}
