package repl

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	mk := func(model, provider, dir string, at time.Time, msgs agentcore.MessageList) string {
		h := session.SessionHeader{ID: session.NewID(at), CreatedAt: at, UpdatedAt: at, Model: model, Provider: provider, Cwd: dir}
		if err := store.Save(h, msgs); err != nil {
			t.Fatalf("Save: %v", err)
		}
		return h.ID
	}
	now := time.Now().UTC()
	idA := mk("model-a", "prov", cwd, now, nil)
	idB := mk("model-b", "prov", cwd, now.Add(time.Second), agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hi b")}},
	})
	// A newer session from another project: hidden by the default scope, listed
	// by --all, and reachable by its literal id.
	idC := mk("model-c", "prov", t.TempDir(), now.Add(2*time.Second), agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hi c")}},
	})

	deps := &replDeps{
		store:    store,
		header:   session.SessionHeader{ID: idA, Model: "model-a", Provider: "prov"},
		agentCtx: &agentcore.AgentContext{},
		live:     &cli.LiveConfig{Model: "model-a", ProviderName: "prov"},
		cwd:      cwd,
	}

	// Bare lists this project's sessions without switching, and says how many
	// other projects are hidden.
	var out bytes.Buffer
	runResume(&out, deps, "")
	if deps.header.ID != idA {
		t.Fatalf("bare /resume moved header to %q", deps.header.ID)
	}
	if got := out.String(); !strings.Contains(got, "Recent sessions in "+cli.DisplayHomePath(cwd)) ||
		!strings.Contains(got, "1 session(s) in other projects") {
		t.Errorf("bare list = %q, want the scoped header and the hidden-session hint", got)
	}
	if strings.Contains(out.String(), idC) {
		t.Errorf("bare list leaked another project's session:\n%s", out.String())
	}
	// --all widens the list to every project and switches to the newest one.
	out.Reset()
	runResume(&out, deps, "--all")
	if deps.header.ID != idA {
		t.Fatalf("bare /resume --all moved header to %q", deps.header.ID)
	}
	if !strings.Contains(out.String(), idC) {
		t.Errorf("--all list = %q, want the other project's session", out.String())
	}
	runResume(io.Discard, deps, "--all 1")
	if deps.header.ID != idC {
		t.Fatalf("--all 1 header.ID = %q, want %q", deps.header.ID, idC)
	}
	// A literal id resolves globally even without --all.
	runResume(io.Discard, deps, idA)
	if deps.header.ID != idA {
		t.Fatalf("literal id header.ID = %q, want %q", deps.header.ID, idA)
	}
	// An out-of-range project-scoped number names the way out.
	out.Reset()
	runResume(&out, deps, "9")
	if !strings.Contains(out.String(), cli.AllSessionsFlag) {
		t.Errorf("out-of-range message = %q, want a hint naming %s", out.String(), cli.AllSessionsFlag)
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

// TestRunResumeEmptyProjectHint: a project with no sessions of its own says so
// and names --all instead of reporting an empty store.
func TestRunResumeEmptyProjectHint(t *testing.T) {
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	other := t.TempDir()
	now := time.Now().UTC()
	h := session.SessionHeader{ID: session.NewID(now), CreatedAt: now, UpdatedAt: now, Cwd: other}
	if err := store.Save(h, nil); err != nil {
		t.Fatalf("Save: %v", err)
	}
	deps := &replDeps{
		store:    store,
		header:   session.SessionHeader{ID: "current"},
		agentCtx: &agentcore.AgentContext{},
		live:     &cli.LiveConfig{Model: "m", ProviderName: "p"},
		cwd:      filepath.Join(t.TempDir(), "fresh-project"),
	}
	var out bytes.Buffer
	runResume(&out, deps, "")
	if got := out.String(); !strings.Contains(got, cli.AllSessionsFlag) || strings.Contains(got, "No saved sessions yet.") {
		t.Errorf("empty-project list = %q, want a hint naming %s", got, cli.AllSessionsFlag)
	}
}
