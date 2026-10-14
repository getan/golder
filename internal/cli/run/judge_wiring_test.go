package run

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/provider"
)

// TestReviewCheckCarriesSession verifies the reviewer's provider request
// carries the session id (opencode's x-opencode-session) exactly like the
// main loop's requests, so sticky routing accepts it.
func TestReviewCheckCarriesSession(t *testing.T) {
	var gotExtra map[string]any
	stream := func(_ context.Context, _ string, _ provider.LlmContext, cfg provider.StreamConfig) (*provider.AssistantMessageEventStream, error) {
		gotExtra = cfg.Extra
		s := provider.NewAssistantMessageEventStream(0)
		final := agentcore.AssistantMessage{
			RoleField:  agentcore.RoleAssistant,
			StopReason: agentcore.StopReasonEndTurn,
			Content:    agentcore.ContentList{agentcore.NewTextContent(`{"level":"allow","rationale":"ok"}`)},
		}
		s.SetResult(final)
		s.Close()
		return s, nil
	}
	check := ReviewCheck("m", "opencode-go", stream, nil, "sess-1")
	out, err := check(context.Background(), "sys", "user")
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if out != `{"level":"allow","rationale":"ok"}` {
		t.Fatalf("out = %q", out)
	}
	if got := provider.SessionHeaderValue("opencode-go", gotExtra); got != "sess-1" {
		t.Fatalf("session header value = %q, want sess-1 (extra %v)", got, gotExtra)
	}
}

// TestReviewCheckFallsBackSession verifies an empty session id still yields a
// stable process id rather than a headerless request the opencode endpoint
// rejects.
func TestReviewCheckFallsBackSession(t *testing.T) {
	var gotExtra map[string]any
	stream := func(_ context.Context, _ string, _ provider.LlmContext, cfg provider.StreamConfig) (*provider.AssistantMessageEventStream, error) {
		gotExtra = cfg.Extra
		s := provider.NewAssistantMessageEventStream(0)
		s.SetResult(agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonEndTurn})
		s.Close()
		return s, nil
	}
	check := ReviewCheck("m", "opencode-go", stream, nil, "")
	if _, err := check(context.Background(), "sys", "user"); err != nil {
		t.Fatalf("check: %v", err)
	}
	if got := provider.SessionHeaderValue("opencode-go", gotExtra); got == "" {
		t.Fatalf("session header empty for a derived process id: %v", gotExtra)
	}
}

// TestNewReviewerGradesEveryPatch pins the wiring after the routine-edit fast
// path was removed: no patch skips the reviewer, and one whose effect outlives
// the call reaches it like any other. The provider is nil on purpose, which
// degrades the reviewer to fail-closed — so the outcome is a failed verdict
// rather than an allow, and that is exactly what proves the reviewer was
// consulted instead of a shortcut answering.
func TestNewReviewerGradesEveryPatch(t *testing.T) {
	root := t.TempDir()
	classifier := NewReviewer("m", "opencode-go", nil, nil, "", root, true)

	patchArgs := func(p string) []byte {
		raw, err := json.Marshal(map[string]string{
			"patch": "*** Begin Patch\n*** Add File: " + p + "\n+x\n*** End Patch\n",
		})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	// An ordinary in-workspace patch still reaches the reviewer. There used to be
	// an exemption here; with it gone, an absent reviewer fails closed, which is
	// the safe direction and the distinguishable outcome this test relies on.
	v := classifier.Classify(context.Background(), "apply_patch", patchArgs("internal/main.go"))
	if v.Level == judge.Allow {
		t.Errorf("a routine patch was allowed without a reviewer: %+v", v)
	}
	if !v.Failed {
		t.Errorf("with no provider the verdict must fail closed, got %+v", v)
	}

	// A patch whose effect outlives the call is graded the same way: it is not
	// singled out into a shortcut, and the fact about it travels to the reviewer
	// through the state (judge.OutlivingPaths) rather than gating it here.
	v = classifier.Classify(context.Background(), "apply_patch", patchArgs(".git/hooks/pre-commit"))
	if v.Level == judge.Allow {
		t.Errorf("a patch to .git/hooks was allowed without a reviewer: %+v", v)
	}
}

// TestLocalTableTakesTheDecisionModelsPlace pins the relationship between the
// two routine graders: they are ALTERNATIVES, chosen once per reviewer from
// whether the decision model can run at all.
//
// The distinction matters in one direction. Chaining them would route the
// decision model's escalations into the table, and an escalation means "this
// deserves a model's judgment" — not "try something cheaper first". So with no
// decision-model credential the table runs in its place, and with one it is not
// in the chain at all.
func TestLocalTableTakesTheDecisionModelsPlace(t *testing.T) {
	root := t.TempDir()
	// A credential store holding a decision-model key: the table must step aside.
	// The endpoint is unreachable on purpose — what is asserted is WHICH grader
	// ran, and an unreachable decision model still answers (it disables itself and
	// escalates), so the source is never "local".
	creds := provider.NewCredentialStore(nil)
	creds.SetOverride("typesafe", "test-key-not-used")
	t.Setenv("GOLDER_JEV_ENDPOINT", "http://127.0.0.1:1/unreachable")
	t.Setenv("GOLDER_JEV_TIMEOUT_MS", "50")

	classifier := NewReviewer("m", "opencode-go", nil, creds, "", root, true)
	raw, err := json.Marshal(map[string]string{"command": "ls internal"})
	if err != nil {
		t.Fatal(err)
	}
	if v := classifier.Classify(context.Background(), "bash", raw); v.Source == judge.LocalSource {
		t.Errorf("with a decision-model credential the table must not be in the chain, got %+v", v)
	}
}

// TestLocalStageIsAStageOfTheGatePipeline pins what the local table classifier
// decides and what it hands to the reviewer.
//
// It must not be an exemption: everything it does not decide reaches the
// reviewer exactly as before — observed here through the reviewer's fail-closed
// verdict (the provider is nil on purpose, so "reached the reviewer" and "failed
// closed" are the same observation).
//
// The tiers it decides are the routine two: reads are allowed, and the ordinary
// work of a session — a build, an install, a fetch, a redirect into a file — is
// graded Confirm, which the gate settles by the mode it is in (auto runs it, ask
// asks). The destructive and the unmodelled tiers are not decided here at all.
func TestLocalStageIsAStageOfTheGatePipeline(t *testing.T) {
	root := t.TempDir()
	classifier := NewReviewer("m", "opencode-go", nil, nil, "", root, true)
	bash := func(cmd string) json.RawMessage {
		raw, err := json.Marshal(map[string]string{"command": cmd})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	// Reads: allowed, with no model call and no prompt in any mode.
	for _, cmd := range []string{
		"ls internal",
		"grep -rn foo internal",
		"git status --short", // the subcommand table, not the git base
		"wc -l main.go",
		"git log --oneline -3",
		"jq . file.json",
	} {
		v := classifier.Classify(context.Background(), "bash", bash(cmd))
		if v.Level != judge.Allow || v.Source != judge.LocalSource {
			t.Errorf("%q = %+v, want an allow from the local stage", cmd, v)
		}
	}

	// Ordinary local and outbound work: graded Confirm rather than allowed, so
	// `ask` mode still asks and the mode keeps its meaning — but no model call.
	for _, cmd := range []string{
		"go test ./...",                 // the subcommand table: LOCAL_EFFECTS
		"go build ./...",                // likewise
		"mkdir -p /tmp/dir-for-test",    //
		"curl -fsS https://example.com", // EXTERNAL_EFFECTS: an ordinary fetch
		"echo hi > /tmp/out.txt",        // a redirect writes a file
		"ls > /tmp/out.txt",             // ... whatever the program is
	} {
		v := classifier.Classify(context.Background(), "bash", bash(cmd))
		if v.Level != judge.Confirm || v.Source != judge.LocalSource {
			t.Errorf("%q = %+v, want a confirm from the local stage", cmd, v)
		}
	}

	// The tiers it refuses to weigh reach the reviewer, and none of them may come
	// back allowed: destructive commands, opaque interpreters, and programs with
	// no rule at all.
	for _, cmd := range []string{
		"rm -rf build",             // DANGEROUS
		"git push --force",         // DANGEROUS: the subcommand table
		"python3 script.py",        // DANGEROUS: an opaque program
		"sh -c 'ls; rm -rf build'", // DANGEROUS: a shell that could do anything
		"unknown-program --flag",   // no rule at all
		"terraform apply",          // DANGEROUS
	} {
		v := classifier.Classify(context.Background(), "bash", bash(cmd))
		if v.Level == judge.Allow {
			t.Errorf("%q was allowed by the local stage; it must reach the reviewer: %+v", cmd, v)
		}
		if !v.Failed {
			t.Errorf("%q should have reached the fail-closed reviewer, got %+v", cmd, v)
		}
	}

	// And the floor still runs in front of the stage, so credential material is
	// denied rather than graded: the stage never sees it.
	if v := classifier.Classify(context.Background(), "bash", bash("cat ~/.ssh/id_rsa")); v.Level != judge.Deny || v.Source != "static" {
		t.Errorf("credential read = %+v, want a denial from the static floor", v)
	}

	// A non-shell tool is not this stage's to grade: an in-workspace patch goes
	// to the reviewer like everything else.
	patch, err := json.Marshal(map[string]string{"patch": "*** Begin Patch\n*** Add File: internal/x.go\n+x\n*** End Patch\n"})
	if err != nil {
		t.Fatal(err)
	}
	if v := classifier.Classify(context.Background(), "apply_patch", patch); v.Source == judge.LocalSource {
		t.Errorf("apply_patch was graded by the local stage: %+v", v)
	}
}

// TestTheTableHasNoOffSwitch pins a decision rather than a behavior: the table
// is the fallback for a run with no decision model, so it is not configurable.
// An off switch was written and then removed — a fallback that can be disabled
// is a fallback that leaves the run paying a model round trip for `ls`, which is
// the cost this layer exists to remove.
//
// The test states it the only way a test can state an absence: the value that
// used to disable the layer has no effect.
func TestTheTableHasNoOffSwitch(t *testing.T) {
	for _, v := range []string{"off", "0", "false", "no", "disabled"} {
		t.Setenv("GOLDER_LOCAL_REVIEW", v)
		root := t.TempDir()
		classifier := NewReviewer("m", "opencode-go", nil, nil, "", root, true)
		raw, err := json.Marshal(map[string]string{"command": "ls internal"})
		if err != nil {
			t.Fatal(err)
		}
		if got := classifier.Classify(context.Background(), "bash", raw); got.Source != judge.LocalSource {
			t.Errorf("GOLDER_LOCAL_REVIEW=%s changed the outcome (%+v); the table is the fallback and is always on", v, got)
		}
	}
}
