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
