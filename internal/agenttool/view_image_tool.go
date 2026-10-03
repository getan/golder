// This file implements the view_image tool: read a local image file and return
// it to the model as an ImageContent block alongside a short text summary. The
// image rides a tool result, so each provider serializes it through its own
// tool-result channel (Responses function_call_output content items, Anthropic
// tool_result blocks; Chat Completions cannot carry images and degrades to a
// placeholder).
package agenttool

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
)

// viewImageMaxBytes caps how large a file view_image will read. A tool result
// is persisted into the session and replayed on every later request, so an
// oversized image would tax context and disk on every turn; the cap keeps a
// single view bounded. Callers that need the full asset can inspect it with
// external tooling.
const viewImageMaxBytes = 8 << 20 // 8 MiB

// ViewImageTool reads an image under Root (or ExtraRoots) and returns it as a
// tool result. It never mutates anything.
type ViewImageTool struct {
	// Root is the directory that bounds reads. A path resolving outside Root
	// is rejected. Empty Root defaults to the current working directory.
	Root string
	// ExtraRoots are additional trusted directories, mirroring ReadTool: the
	// skills directory may hold images a skill references.
	ExtraRoots []string
}

// viewImageArgs is the decoded argument shape for ViewImageTool.
type viewImageArgs struct {
	// Path is the image file, relative to Root (or absolute within it).
	Path string `json:"path"`
	// Detail is a hint for the provider: "high" (default) or "original".
	Detail string `json:"detail,omitempty"`
}

// Name implements AgentTool.
func (t *ViewImageTool) Name() string { return "view_image" }

// Description implements AgentTool.
func (t *ViewImageTool) Description() string {
	return "View a local image file from disk and attach it for visual inspection. " +
		"Use this for screenshots, design mockups, diagrams, or rendered output " +
		"that cannot be understood as text."
}

// Schema implements AgentTool.
func (t *ViewImageTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path":   {"type": "string", "description": "Image file path, relative to the workspace root (PNG, JPEG, GIF, or WebP)."},
    "detail": {"type": "string", "enum": ["high", "original"], "description": "Detail hint. Defaults to high; original preserves exact resolution."}
  },
  "required": ["path"],
  "additionalProperties": false
}`)
}

// ExecutionMode implements AgentTool. Reading is side-effect free → parallel.
func (t *ViewImageTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionParallel
}

// resolvePath mirrors ReadTool's boundary policy.
func (t *ViewImageTool) resolvePath(p string) (string, error) {
	if len(t.ExtraRoots) == 0 {
		return resolveWithin(t.Root, p)
	}
	return resolveWithinAny(append([]string{t.Root}, t.ExtraRoots...), p)
}

// Execute implements AgentTool. Read failures are error results, not Go
// errors, so the model can react (a missing file is not a harness failure).
func (t *ViewImageTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	a, bad := decodeArgs[viewImageArgs](args, "view_image")
	if bad != nil {
		return *bad, nil
	}
	if a.Path == "" {
		return errorResult("view_image: path is required"), nil
	}
	if a.Detail != "" && a.Detail != "high" && a.Detail != "original" {
		return errorResult(`view_image: detail must be "high" or "original"`), nil
	}
	full, err := t.resolvePath(a.Path)
	if err != nil {
		return errorResult("view_image: " + err.Error()), nil
	}
	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return errorResult(fmt.Sprintf("view_image: file %q does not exist", a.Path)), nil
		}
		return errorResult(fmt.Sprintf("view_image: cannot stat %q: %v", a.Path, err)), nil
	}
	if info.IsDir() {
		return errorResult(fmt.Sprintf("view_image: %q is a directory, not an image file", a.Path)), nil
	}
	if info.Size() > viewImageMaxBytes {
		return errorResult(fmt.Sprintf(
			"view_image: %q is %d bytes, larger than the %d-byte view limit; resize or crop it first",
			a.Path, info.Size(), viewImageMaxBytes)), nil
	}

	data, err := os.ReadFile(full)
	if err != nil {
		return errorResult(fmt.Sprintf("view_image: cannot read %q: %v", a.Path, err)), nil
	}
	mime := imageMimeFromPath(full)
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	if !strings.HasPrefix(mime, "image/") {
		return errorResult(fmt.Sprintf(
			"view_image: %q is not a recognized image (detected %q)", a.Path, mime)), nil
	}

	detail := a.Detail
	if detail == "" {
		detail = "high"
	}
	img := agentcore.NewImageContent(base64.StdEncoding.EncodeToString(data), mime)
	text := fmt.Sprintf("Viewed image %s (%s, %d bytes)", a.Path, mime, len(data))
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(text), img},
		Details: map[string]any{
			"path":   a.Path,
			"mime":   mime,
			"bytes":  len(data),
			"detail": detail,
		},
	}, nil
}

// imageMimeFromPath maps a file extension to an image mime type, returning ""
// for unknown extensions so the caller can fall back to content sniffing. It
// mirrors the prompt-image loader's mapping so both paths accept the same set.
func imageMimeFromPath(path string) string {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".png"):
		return "image/png"
	case strings.HasSuffix(lower, ".jpg"), strings.HasSuffix(lower, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(lower, ".gif"):
		return "image/gif"
	case strings.HasSuffix(lower, ".webp"):
		return "image/webp"
	default:
		return ""
	}
}
