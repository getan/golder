package tuiosctl

import (
	"context"
	"encoding/json"
)

// TMLResult identifies a TML surface opened by the daemon.
type TMLResult struct {
	Surface   string `json:"surface"`
	WindowID  string `json:"window_id"`
	Target    string `json:"target"`
	Workspace int    `json:"workspace"`
}

// TMLOpenParams asks the daemon to show one TML document.
type TMLOpenParams struct {
	// Doc is the document: TML text or TML JSON, as a string or a JSON object.
	Doc       any
	Target    string // popup or pane
	Name      string
	Width     string
	Height    string
	Workspace int
}

// TMLOpen shows a TML document and returns the surface id.
func (c *Client) TMLOpen(ctx context.Context, p TMLOpenParams) (TMLResult, error) {
	params := map[string]any{"doc": p.Doc}
	if p.Target != "" {
		params["target"] = p.Target
	}
	if p.Name != "" {
		params["name"] = p.Name
	}
	if p.Width != "" {
		params["width"] = p.Width
	}
	if p.Height != "" {
		params["height"] = p.Height
	}
	if p.Workspace > 0 {
		params["workspace"] = p.Workspace
	}
	var out TMLResult
	err := c.CallInto(ctx, "tml-open", params, &out)
	return out, err
}

// TMLPatchResult is what one patch changed.
type TMLPatchResult struct {
	Version int      `json:"version"`
	Changed []string `json:"changed"`
}

// TMLPatch applies RFC 6902 operations to a surface's state.
func (c *Client) TMLPatch(ctx context.Context, surface string, ops json.RawMessage) (TMLPatchResult, error) {
	var out TMLPatchResult
	err := c.CallInto(ctx, "tml-patch", map[string]any{"surface": surface, "ops": ops}, &out)
	return out, err
}

// TMLClose closes a surface.
func (c *Client) TMLClose(ctx context.Context, surface string) error {
	_, err := c.Call(ctx, "tml-close", map[string]any{"surface": surface})
	return err
}
