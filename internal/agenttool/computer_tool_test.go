package agenttool

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/computeruse"
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
