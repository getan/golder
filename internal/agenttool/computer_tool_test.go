package agenttool

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"slices"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/computeruse"
	"github.com/getan/golder/internal/permissions"
)

type computerTestBackend struct{}

func (computerTestBackend) Validate(context.Context, computeruse.Target, bool) error { return nil }
func (computerTestBackend) Close() error                                             { return nil }
func (computerTestBackend) Snapshot(context.Context, computeruse.Target) (string, []computeruse.Element, bool, error) {
	return "s1", []computeruse.Element{{Ref: "@e1", Role: "text", Secure: true, Value: "secret"}}, false, nil
}

type computerControlBackend struct{ calls int }

func (*computerControlBackend) Validate(context.Context, computeruse.Target, bool) error { return nil }
func (*computerControlBackend) Close() error                                             { return nil }
func (*computerControlBackend) Snapshot(context.Context, computeruse.Target) (string, []computeruse.Element, bool, error) {
	return "backend-snapshot", []computeruse.Element{{Ref: "@e1", Role: "button", Enabled: true}}, false, nil
}
func (*computerControlBackend) SupportsAction(action string) bool { return action == "click" }
func (b *computerControlBackend) Act(context.Context, computeruse.Target, string, string, string, string) error {
	b.calls++
	return nil
}

func TestComputerToolAdvertisesAndEnforcesSessionState(t *testing.T) {
	s := computeruse.New(computerTestBackend{})
	tool := &ComputerTool{Session: s}
	if slices.Contains(schemaEnum(t, tool.Schema()), "snapshot") {
		t.Fatal("unselected target advertised observation")
	}
	call := func(action string) agentcore.AgentToolResult {
		result, err := tool.Execute(context.Background(), "id", json.RawMessage(`{"action":"`+action+`"}`), nil)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if result := call("snapshot"); !result.IsError {
		t.Fatal("unselected target allowed observation")
	}
	if err := s.Start(context.Background(), computeruse.Target{ID: "opaque"}, computeruse.Capabilities{Observe: true}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(schemaEnum(t, tool.Schema()), "snapshot") {
		t.Fatal("authorized observation not advertised")
	}
	result := call("snapshot")
	if result.IsError || strings.Contains(agentcore.ContentToText(result.Content), "secret") {
		t.Fatalf("snapshot leaked secure value or failed: %+v", result)
	}
	call("pause")
	if slices.Contains(schemaEnum(t, tool.Schema()), "snapshot") || !call("snapshot").IsError {
		t.Fatal("paused session exposed or permitted snapshot")
	}
	if !call("resume").IsError {
		t.Fatal("model was permitted to resume")
	}
}

func TestComputerToolRequiresExactConfirmationForControl(t *testing.T) {
	backend := &computerControlBackend{}
	s := computeruse.New(backend)
	if err := s.Start(context.Background(), computeruse.Target{ID: "target"}, computeruse.Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	tool := &ComputerTool{Session: s}
	if !slices.Contains(schemaEnum(t, tool.Schema()), "click") {
		t.Fatal("supported control action was not advertised")
	}
	snapshot, err := s.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"action": "click", "snapshot_id": snapshot.ID, "ref": "@e1"})
	if !tool.RequiresOperatorConfirmationFor(raw) {
		t.Fatal("control did not require confirmation")
	}
	result, err := tool.Execute(context.Background(), "one", raw, nil)
	if err != nil || !result.IsError || backend.calls != 0 {
		t.Fatalf("unconfirmed call: result=%+v calls=%d err=%v", result, backend.calls, err)
	}
	ctx := agentcore.WithOperatorConfirmation(context.Background(), "one", "computer", raw)
	result, err = tool.Execute(ctx, "one", raw, nil)
	if err != nil || result.IsError || backend.calls != 1 {
		t.Fatalf("confirmed call: result=%+v calls=%d err=%v", result, backend.calls, err)
	}
	result, err = tool.Execute(ctx, "one", raw, nil)
	if err != nil || !result.IsError || backend.calls != 1 {
		t.Fatalf("stale confirmed call: result=%+v calls=%d err=%v", result, backend.calls, err)
	}
}

type computerFillBackend struct{ value string }

func (*computerFillBackend) Validate(context.Context, computeruse.Target, bool) error { return nil }
func (*computerFillBackend) Close() error                                             { return nil }
func (*computerFillBackend) Snapshot(context.Context, computeruse.Target) (string, []computeruse.Element, bool, error) {
	return "backend-snapshot", []computeruse.Element{{Ref: "@e1", Role: "text", Enabled: true, Editable: true}}, false, nil
}
func (*computerFillBackend) SupportsAction(action string) bool { return action == "fill" }
func (b *computerFillBackend) Act(_ context.Context, _ computeruse.Target, _, _, _, text string) error {
	b.value = text
	return nil
}

func TestComputerFillRequiresExactConfirmation(t *testing.T) {
	backend := &computerFillBackend{}
	s := computeruse.New(backend)
	if err := s.Start(context.Background(), computeruse.Target{ID: "target"}, computeruse.Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	tool := &ComputerTool{Session: s}
	if !slices.Contains(schemaEnum(t, tool.Schema()), "fill") {
		t.Fatal("editable action was not advertised")
	}
	snapshot, err := s.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"action": "fill", "snapshot_id": snapshot.ID, "ref": "@e1", "text": "世界"})
	result, err := tool.Execute(context.Background(), "fill", raw, nil)
	if err != nil || !result.IsError || backend.value != "" || !tool.RequiresOperatorConfirmationFor(raw) {
		t.Fatalf("unconfirmed fill: result=%+v value=%q err=%v", result, backend.value, err)
	}
	ctx := agentcore.WithOperatorConfirmation(context.Background(), "fill", "computer", raw)
	result, err = tool.Execute(ctx, "fill", raw, nil)
	if err != nil || result.IsError || backend.value != "世界" {
		t.Fatalf("confirmed fill: result=%+v value=%q err=%v", result, backend.value, err)
	}
}

type computerCoordinateBackend struct {
	png   []byte
	calls int
}

func (*computerCoordinateBackend) Validate(context.Context, computeruse.Target, bool) error {
	return nil
}
func (*computerCoordinateBackend) Close() error { return nil }
func (b *computerCoordinateBackend) Capture(context.Context, computeruse.Target) ([]byte, error) {
	return b.png, nil
}
func (b *computerCoordinateBackend) CaptureBound(context.Context, computeruse.Target) ([]byte, string, error) {
	return b.png, "geometry", nil
}
func (b *computerCoordinateBackend) ClickFrame(context.Context, computeruse.Target, string, float64, float64) error {
	b.calls++
	return nil
}

type limitedComputerKeyBackend struct{ computerCoordinateBackend }

func (*limitedComputerKeyBackend) PressFrame(context.Context, computeruse.Target, string, string) error {
	return nil
}

func (*limitedComputerKeyBackend) SupportsFrameKey(key string) bool {
	return key == "enter" || key == "space"
}

func TestComputerSchemaRestrictsFrameKeysToBackend(t *testing.T) {
	session := computeruse.New(&limitedComputerKeyBackend{})
	if err := session.Start(context.Background(), computeruse.Target{ID: "target"}, computeruse.Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	tool := &ComputerTool{Session: session}
	if !slices.Contains(schemaEnum(t, tool.Schema()), "press") {
		t.Fatal("supported key action was not advertised")
	}
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(tool.Schema(), &schema); err != nil {
		t.Fatal(err)
	}
	if got := schema.Properties["key"].Enum; !slices.Equal(got, []string{"enter", "space"}) {
		t.Fatalf("advertised keys = %v", got)
	}
}

func TestComputerCoordinateClickRequiresConfirmationAndFrame(t *testing.T) {
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 20, 10))); err != nil {
		t.Fatal(err)
	}
	backend := &computerCoordinateBackend{png: imageData.Bytes()}
	s := computeruse.New(backend)
	if err := s.Start(context.Background(), computeruse.Target{ID: "target"}, computeruse.Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	tool := &ComputerTool{Session: s}
	if !slices.Contains(schemaEnum(t, tool.Schema()), "click_at") {
		t.Fatal("bound coordinate action was not advertised")
	}
	frame, err := s.Capture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"action": "click_at", "frame_id": frame.ID, "x": 5, "y": 4})
	result, err := tool.Execute(context.Background(), "coordinate", raw, nil)
	if err != nil || !result.IsError || backend.calls != 0 || !tool.RequiresOperatorConfirmationFor(raw) {
		t.Fatalf("unconfirmed coordinate click: result=%+v calls=%d err=%v", result, backend.calls, err)
	}
	ctx := agentcore.WithOperatorConfirmation(context.Background(), "coordinate", "computer", raw)
	result, err = tool.Execute(ctx, "coordinate", raw, nil)
	if err != nil || result.IsError || backend.calls != 1 || !strings.Contains(agentcore.ContentToText(result.Content), `"verified":false`) {
		t.Fatalf("confirmed coordinate click: result=%+v calls=%d err=%v", result, backend.calls, err)
	}
}

func TestComputerSchemaFollowsReadOnlyAndEnforcedSandbox(t *testing.T) {
	backend := &computerControlBackend{}
	s := computeruse.New(backend)
	if err := s.Start(context.Background(), computeruse.Target{ID: "target"}, computeruse.Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	mode := permissions.New(permissions.Auto)
	tool := &ComputerTool{Session: s, Mode: mode}
	mode.Set(permissions.ReadOnly)
	if actions := schemaEnum(t, tool.Schema()); slices.Contains(actions, "click") || !slices.Contains(actions, "snapshot") {
		t.Fatalf("read-only actions = %v", actions)
	}
	mode.Set(permissions.Auto)
	t.Setenv("GOLDER_SANDBOX", "enforce")
	if actions := schemaEnum(t, tool.Schema()); slices.Contains(actions, "click") || slices.Contains(actions, "snapshot") || !slices.Contains(actions, "stop") {
		t.Fatalf("enforced sandbox actions = %v", actions)
	}
}
