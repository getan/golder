package repl

import (
	"io"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/session"
)

func TestRunResume(t *testing.T) {
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	mk := func(model, provider string, at time.Time, msgs agentcore.MessageList) string {
		h := session.SessionHeader{ID: session.NewID(at), CreatedAt: at, UpdatedAt: at, Model: model, Provider: provider}
		if err := store.Save(h, msgs); err != nil {
			t.Fatalf("Save: %v", err)
		}
		return h.ID
	}
	now := time.Now().UTC()
	idA := mk("model-a", "prov", now, nil)
	idB := mk("model-b", "prov", now.Add(time.Second), agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hi b")}},
	})

	deps := &replDeps{
		store:    store,
		header:   session.SessionHeader{ID: idA, Model: "model-a", Provider: "prov"},
		agentCtx: &agentcore.AgentContext{},
		live:     &cli.LiveConfig{Model: "model-a", ProviderName: "prov"},
	}

	// Bare lists without switching.
	runResume(io.Discard, deps, "")
	if deps.header.ID != idA {
		t.Fatalf("bare /resume moved header to %q", deps.header.ID)
	}
	// Numeric selection switches and replays.
	runResume(io.Discard, deps, "1")
	if deps.header.ID != idB {
		t.Fatalf("header.ID = %q, want %q", deps.header.ID, idB)
	}
	if deps.live.Model != "model-b" {
		t.Errorf("live.Model = %q, want model-b", deps.live.Model)
	}
	if len(deps.agentCtx.Messages) != 1 {
		t.Errorf("messages = %d, want 1", len(deps.agentCtx.Messages))
	}
	if deps.hookDeps.SessionID != idB {
		t.Errorf("hookDeps.SessionID = %q, want %q", deps.hookDeps.SessionID, idB)
	}
	// Unknown id keeps state.
	runResume(io.Discard, deps, "no-such-session")
	if deps.header.ID != idB {
		t.Errorf("failed switch moved header to %q", deps.header.ID)
	}
}
