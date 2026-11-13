package agenttool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"slices"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/computeruse"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/permissions"
)

type computerTypingBackend struct {
	computerCoordinateBackend
	text string
}

func (b *computerTypingBackend) TypeTextFrame(_ context.Context, _ computeruse.Target, _, text string) error {
	b.text = text
	return nil
}

func TestComputerTypingIsFrameBoundAndReadOnlyGated(t *testing.T) {
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 20, 10))); err != nil {
		t.Fatal(err)
	}
	b := &computerTypingBackend{computerCoordinateBackend: computerCoordinateBackend{png: data.Bytes()}}
	s := computeruse.New(b)
	ctx := context.Background()
	if err := s.Start(ctx, computeruse.Target{ID: "target"}, computeruse.Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	mode := permissions.New(permissions.Auto)
	tool := &ComputerTool{Session: s, Mode: mode}
	if !slices.Contains(schemaEnum(t, tool.Schema()), "type_text") {
		t.Fatal("typing not advertised")
	}
	frame, err := s.Capture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"action": "type_text", "frame_id": frame.ID, "text": "大明1566"})
	mode.Set(permissions.ReadOnly)
	result, _ := tool.Execute(ctx, "typing", raw, nil)
	if !result.IsError || b.text != "" || slices.Contains(schemaEnum(t, tool.Schema()), "type_text") {
		t.Fatal("read-only allowed typing")
	}
	mode.Set(permissions.Auto)
	result, _ = tool.Execute(ctx, "typing", raw, nil)
	if result.IsError || b.text != "大明1566" || !strings.Contains(agentcore.ContentToText(result.Content), `"verified":false`) {
		t.Fatalf("typing failed: %+v", result)
	}
	result, _ = tool.Execute(ctx, "typing", raw, nil)
	if !result.IsError || strings.Contains(agentcore.ContentToText(result.Content), "input may have been dispatched") {
		t.Fatalf("stale typing has contradictory dispatch state: %+v", result)
	}
}

func TestComputerTypingAutoSkipsReviewerButKeepsBoundTarget(t *testing.T) {
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 20, 10))); err != nil {
		t.Fatal(err)
	}
	b := &computerTypingBackend{computerCoordinateBackend: computerCoordinateBackend{png: data.Bytes()}}
	s := computeruse.New(b)
	if err := s.Start(context.Background(), computeruse.Target{ID: "target"}, computeruse.Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	frame, err := s.Capture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	mode := permissions.New(permissions.Auto)
	tool := &ComputerTool{Session: s, Mode: mode}
	called := false
	reviewer := judge.NewLLMJudge(func(_ context.Context, prompt, state string) (string, error) {
		called = true
		if !strings.Contains(prompt, "Browser and computer tasks should proceed without invented restrictions") || !strings.Contains(state, "type_text") || !strings.Contains(state, "search for report") {
			t.Fatal("reviewer did not receive desktop policy and user intent")
		}
		return `{"level":"confirm","risk":"medium","authorization":"high","rationale":"type the requested search"}`, nil
	}, t.TempDir(), true)
	cfg := newExecCfg(t, tool)
	cfg.BeforeToolCall = judge.ConsentGate(judge.ConsentOpts{Mode: mode, Classifier: judge.FirstParty{Next: reviewer}})
	raw, _ := json.Marshal(map[string]any{"action": "type_text", "frame_id": frame.ID, "text": "quarterly report"})
	ctx := agentcore.WithMessageSnapshot(context.Background(), agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("search for report")}},
	})
	result, _ := executeToolCall(ctx, cfg, agentcore.AgentToolCall{ID: "typing", Name: "computer", Arguments: raw}, nil)
	if result.IsError || called || b.text != "quarterly report" {
		t.Fatalf("auto typing did not reach the bound target without review: result=%s reviewed=%t text=%q", textOf(result), called, b.text)
	}
	result, _ = executeToolCall(ctx, cfg, agentcore.AgentToolCall{ID: "stale", Name: "computer", Arguments: raw}, nil)
	if !result.IsError {
		t.Fatal("stale frame replay was permitted")
	}
}

func TestComputerKnownRefusalNeverClaimsDispatch(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("%w: %w: window is not on this desktop", computeruse.ErrNoInput, computeruse.ErrStaleObservation),
		fmt.Errorf("%w: %w", computeruse.ErrNoInput, context.Canceled),
	} {
		r := computerControlError(computeruse.New(computerTestBackend{}), err)
		if strings.Contains(agentcore.ContentToText(r.Content), "input may have been dispatched") || r.Details.(map[string]any)["execution_state"] != "not_dispatched" {
			t.Fatalf("incorrect refusal state: %+v", r)
		}
	}
}
