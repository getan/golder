package androiduse

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/getan/golder/internal/agentcore"
)

const maxImageBytes = 8 << 20

var packageNamePattern = regexp.MustCompile(`^[A-Za-z0-9_]+(\.[A-Za-z0-9_]+)+$`)

// Tool exposes the Phase 1 Android accessibility surface to the Go agent.
// Node actions carry a snapshot id so a stale observation cannot silently click
// a different screen.
type Tool struct {
	client        Client
	schema        json.RawMessage
	phase2        *Phase2Gate
	phase2Enabled bool
}

func NewTool(endpoint string) *Tool {
	return newTool(endpoint, false)
}

// NewToolWithPhase2 exposes the high-privilege background-display actions only
// after the host has received explicit advanced-control consent.
func NewToolWithPhase2(endpoint string, enabled bool) *Tool {
	return newTool(endpoint, enabled)
}

func newTool(endpoint string, phase2Enabled bool) *Tool {
	schema := toolSchema
	if phase2Enabled {
		schema = toolSchemaPhase2
	}
	gate := NewPhase2Gate()
	if phase2Enabled {
		_ = gate.Enable(true)
	}
	return &Tool{
		client:        Client{Endpoint: endpoint},
		schema:        json.RawMessage(schema),
		phase2:        gate,
		phase2Enabled: phase2Enabled,
	}
}

func (t *Tool) Name() string { return "android_use" }

func (t *Tool) Description() string {
	if t.phase2Enabled {
		return "Operate another app in the background display by default, so the person keeps their own screen: background_status, background_start, background_open_app, background_observe and background_tap. Observe before acting and after the person hands the display back; pass its snapshotId to the next background action and never replay an earlier action. Use visible-screen observe/tap/type only when the person wants to watch, when background control is unavailable, or after resolving a background refusal. Release the display with background_release and stop the helper with background_stop. Background control requires explicit user consent."
	}
	return "Observe and operate the user's visible Android app through a user-enabled AccessibilityService. Observe first, then act with the returned snapshotId and node ref. Use open_app/back/home for navigation; sensitive actions still require the user's judgment."
}

func (t *Tool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}

func (t *Tool) Schema() json.RawMessage {
	if len(t.schema) == 0 {
		return json.RawMessage(toolSchema)
	}
	return t.schema
}

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
	if strings.HasPrefix(action, "background_") {
		return t.executePhase2(ctx, "phase2_"+strings.TrimPrefix(action, "background_"), &a)
	}
	if strings.HasPrefix(action, "phase2_") {
		return t.executePhase2(ctx, action, &a)
	}
	switch action {
	case "observe", "screenshot", "status", "back", "home", "take_over", "resume", "wait", "tap", "type", "swipe", "open_app":
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
	result := agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(string(out))},
	}
	if action == "screenshot" {
		if path, _ := response["path"].(string); path != "" {
			if info, statErr := os.Stat(path); statErr == nil && info.Size() > 0 && info.Size() <= maxImageBytes {
				if data, readErr := os.ReadFile(path); readErr == nil {
					result.Content = append(result.Content,
						agentcore.NewImageContent(base64.StdEncoding.EncodeToString(data), "image/png"))
				}
			}
		}
	}
	return result, nil
}

func (t *Tool) executePhase2(ctx context.Context, action string, a *args) (agentcore.AgentToolResult, error) {
	if !t.phase2Enabled || t.phase2 == nil {
		return errorResult("android_use: background control is not enabled"), nil
	}
	if err := validatePhase2(action, a); err != nil {
		return errorResult("android_use: " + err.Error()), nil
	}
	switch action {
	case "phase2_status":
		response, err := t.syncPhase2(ctx)
		if err != nil {
			return errorResult("android_use: " + err.Error()), nil
		}
		return phase2Result(response), nil
	case "phase2_observe":
		if err := t.requirePhase2Ready(ctx); err != nil {
			return errorResult("android_use: " + err.Error()), nil
		}
		response, err := t.client.Do(ctx, map[string]any{"action": action})
		if err != nil {
			return errorResult("android_use: " + err.Error()), nil
		}
		image, _ := response["imageBase64"].(string)
		delete(response, "imageBase64")
		decoded, err := base64.StdEncoding.DecodeString(image)
		if err != nil || len(decoded) == 0 || len(decoded) > maxImageBytes {
			return errorResult("android_use: invalid background frame"), nil
		}
		result := phase2Result(response)
		result.Content = append(result.Content, agentcore.NewImageContent(image, "image/png"))
		return result, nil
	case "phase2_start":
		if _, err := t.syncPhase2(ctx); err != nil {
			return errorResult("android_use: " + err.Error()), nil
		}
		switch t.phase2.State() {
		case Phase2Disabled:
			return errorResult("android_use: background control is disabled in Golder settings"), nil
		case Phase2NeedsPairing:
			return errorResult("android_use: background control needs wireless-debugging pairing"), nil
		}
		response, err := t.client.Do(ctx, map[string]any{"action": action})
		if err != nil {
			return errorResult("android_use: " + err.Error()), nil
		}
		t.applyPhase2Status(response)
		if t.phase2.State() != Phase2Ready {
			return errorResult("android_use: helper did not become ready"), nil
		}
		return phase2Result(response), nil
	case "phase2_open_app":
		if err := t.requirePhase2Ready(ctx); err != nil {
			return errorResult("android_use: " + err.Error()), nil
		}
		response, err := t.client.Do(ctx, map[string]any{
			"action": action, "packageName": a.Package, "snapshotId": a.SnapshotID,
		})
		if err != nil {
			return errorResult("android_use: " + err.Error()), nil
		}
		return phase2Result(response), nil
	case "phase2_tap":
		if err := t.requirePhase2Ready(ctx); err != nil {
			return errorResult("android_use: " + err.Error()), nil
		}
		response, err := t.client.Do(ctx, map[string]any{
			"action": action, "x": *a.X, "y": *a.Y, "snapshotId": a.SnapshotID,
		})
		if err != nil {
			return errorResult("android_use: " + err.Error()), nil
		}
		return phase2Result(response), nil
	case "phase2_release":
		response, err := t.client.Do(ctx, map[string]any{"action": action, "snapshotId": a.SnapshotID})
		if err != nil {
			return errorResult("android_use: " + err.Error()), nil
		}
		t.applyPhase2Status(response)
		return phase2Result(response), nil
	case "phase2_stop":
		response, err := t.client.Do(ctx, map[string]any{"action": action, "snapshotId": a.SnapshotID})
		if err != nil {
			return errorResult("android_use: " + err.Error()), nil
		}
		t.phase2.Stop()
		return phase2Result(response), nil
	default:
		return errorResult("android_use: unsupported background action"), nil
	}
}

func (t *Tool) syncPhase2(ctx context.Context) (map[string]any, error) {
	response, err := t.client.Do(ctx, map[string]any{"action": "phase2_status"})
	if err != nil {
		return nil, err
	}
	t.applyPhase2Status(response)
	return response, nil
}

func (t *Tool) applyPhase2Status(response map[string]any) {
	if t.phase2 == nil {
		t.phase2 = NewPhase2Gate()
	}
	advanced, _ := response["advanced"].(bool)
	paired, _ := response["paired"].(bool)
	helper, _ := response["helperConnected"].(bool)
	if !advanced {
		t.phase2.Disable()
		return
	}
	if t.phase2.State() == Phase2Disabled {
		_ = t.phase2.Enable(true)
	}
	if paired {
		t.phase2.PairingCompleted()
	}
	if helper {
		_ = t.phase2.HelperConnected()
	} else if t.phase2.State() == Phase2Ready {
		t.phase2.HelperDisconnected()
	}
}

func (t *Tool) requirePhase2Ready(ctx context.Context) error {
	if _, err := t.syncPhase2(ctx); err != nil {
		return err
	}
	if t.phase2.State() != Phase2Ready {
		return fmt.Errorf("background control helper is not ready (%s)", t.phase2.State())
	}
	return nil
}

func phase2Result(response map[string]any) agentcore.AgentToolResult {
	if action, ok := response["action"].(string); ok && strings.HasPrefix(action, "phase2_") {
		response["action"] = "background_" + strings.TrimPrefix(action, "phase2_")
	}
	out, err := json.Marshal(response)
	if err != nil {
		return errorResult("android_use: response encoding failed")
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(string(out))},
	}
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

func validatePhase2(action string, a *args) error {
	switch action {
	case "phase2_open_app":
		if !packageNamePattern.MatchString(a.Package) {
			return fmt.Errorf("packageName is required and must be a valid package name")
		}
	case "phase2_tap":
		if a.X == nil || a.Y == nil || *a.X < 0 || *a.Y < 0 {
			return fmt.Errorf("background_tap requires non-negative x/y")
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
    "action":{"type":"string","enum":["observe","screenshot","status","tap","type","swipe","open_app","back","home","wait","take_over","resume"]},
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

const toolSchemaPhase2 = `{
  "type":"object",
  "properties":{
    "action":{"type":"string","enum":["observe","screenshot","status","tap","type","swipe","open_app","back","home","wait","take_over","resume","background_status","background_observe","background_start","background_stop","background_open_app","background_tap","background_release"]},
    "snapshotId":{"type":"string","description":"Snapshot returned by observe; required for visible-screen node actions and the first background action after user handback."},
    "ref":{"type":"string","description":"Node ref from the current visible-screen snapshot."},
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
