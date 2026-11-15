package androiduse

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/getan/golder/internal/agentcore"
)

var packageNamePattern = regexp.MustCompile(`^[A-Za-z0-9_]+(\.[A-Za-z0-9_]+)+$`)

// Tool exposes the Phase 1 Android accessibility surface to the Go agent.
// Node actions carry a snapshot id so a stale observation cannot silently click
// a different screen.
type Tool struct {
	client Client
}

func NewTool(endpoint string) *Tool {
	return &Tool{client: Client{Endpoint: endpoint}}
}

func (t *Tool) Name() string { return "android_use" }

func (t *Tool) Description() string {
	return "Observe and operate the user's visible Android app through a user-enabled AccessibilityService. Observe first, then act with the returned snapshotId and node ref. Use open_app/back/home for navigation; sensitive actions still require the user's judgment."
}

func (t *Tool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}

func (t *Tool) Schema() json.RawMessage { return json.RawMessage(toolSchema) }

type args struct {
	Action     string `json:"action"`
	SnapshotID string `json:"snapshotId,omitempty"`
	Ref        string `json:"ref,omitempty"`
	Text       string `json:"text,omitempty"`
	X          *int   `json:"x,omitempty"`
	Y          *int   `json:"y,omitempty"`
	ToX        *int   `json:"toX,omitempty"`
	ToY        *int   `json:"toY,omitempty"`
	Package    string `json:"packageName,omitempty"`
	DurationMS int    `json:"durationMs,omitempty"`
}

func (t *Tool) Execute(ctx context.Context, _ string, raw json.RawMessage, _ agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	var a args
	if len(raw) == 0 || json.Unmarshal(raw, &a) != nil {
		return errorResult("android_use: invalid arguments"), nil
	}
	action := strings.ToLower(strings.TrimSpace(a.Action))
	switch action {
	case "observe", "screenshot", "back", "home", "take_over", "wait", "tap", "type", "swipe", "open_app":
	default:
		return errorResult("android_use: unsupported action"), nil
	}
	if err := validate(action, &a); err != nil {
		return errorResult("android_use: " + err.Error()), nil
	}

	request := map[string]any{"action": action}
	addString(request, "snapshotId", a.SnapshotID)
	addString(request, "ref", a.Ref)
	addString(request, "text", a.Text)
	addString(request, "packageName", a.Package)
	if a.X != nil {
		request["x"] = *a.X
	}
	if a.Y != nil {
		request["y"] = *a.Y
	}
	if a.ToX != nil {
		request["toX"] = *a.ToX
	}
	if a.ToY != nil {
		request["toY"] = *a.ToY
	}
	if a.DurationMS != 0 {
		request["durationMs"] = a.DurationMS
	}

	response, err := t.client.Do(ctx, request)
	if err != nil {
		return errorResult("android_use: " + err.Error()), nil
	}
	out, err := json.Marshal(response)
	if err != nil {
		return errorResult("android_use: response encoding failed"), nil
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(string(out))},
	}, nil
}

func validate(action string, a *args) error {
	switch action {
	case "tap":
		if a.SnapshotID == "" {
			return fmt.Errorf("tap requires snapshotId")
		}
		if strings.TrimSpace(a.Ref) == "" && (a.X == nil || a.Y == nil) {
			return fmt.Errorf("tap requires ref or x/y")
		}
	case "type":
		if a.SnapshotID == "" || strings.TrimSpace(a.Ref) == "" {
			return fmt.Errorf("type requires snapshotId and ref")
		}
		if len([]rune(a.Text)) > maxTextCharacters {
			return fmt.Errorf("text is too long")
		}
	case "swipe":
		if a.SnapshotID == "" || a.X == nil || a.Y == nil || a.ToX == nil || a.ToY == nil {
			return fmt.Errorf("swipe requires snapshotId, x/y and toX/toY")
		}
		if a.DurationMS != 0 && (a.DurationMS < 50 || a.DurationMS > 5000) {
			return fmt.Errorf("durationMs must be between 50 and 5000")
		}
	case "open_app":
		if !packageNamePattern.MatchString(a.Package) {
			return fmt.Errorf("packageName is required and must be a valid package name")
		}
	case "wait":
		if a.DurationMS < 50 || a.DurationMS > 5000 {
			return fmt.Errorf("durationMs must be between 50 and 5000")
		}
	}
	return nil
}

func addString(m map[string]any, key, value string) {
	if value != "" {
		m[key] = value
	}
}

func errorResult(text string) agentcore.AgentToolResult {
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(text)},
		IsError: true,
	}
}

const toolSchema = `{
  "type":"object",
  "properties":{
    "action":{"type":"string","enum":["observe","screenshot","tap","type","swipe","open_app","back","home","wait","take_over"]},
    "snapshotId":{"type":"string","description":"Snapshot returned by observe; required for node actions."},
    "ref":{"type":"string","description":"Node ref from the current snapshot."},
    "text":{"type":"string","description":"Text to enter. It is not submitted automatically."},
    "x":{"type":"integer","description":"Screen x coordinate, only as a fallback."},
    "y":{"type":"integer","description":"Screen y coordinate, only as a fallback."},
    "toX":{"type":"integer","description":"Swipe end x."},
    "toY":{"type":"integer","description":"Swipe end y."},
    "packageName":{"type":"string","description":"Android package to launch."},
    "durationMs":{"type":"integer","description":"Wait or gesture duration, 50-5000 ms."}
  },
  "required":["action"],
  "additionalProperties":false
}`
