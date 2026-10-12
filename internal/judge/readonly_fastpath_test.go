package judge

// Tests for the fast path at the gate: a provably read-only shell command must
// not reach the reviewer, an unrecognized one must, and the floor and the
// read-scope gate must still be able to refuse the call before it runs.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/getan/golder/internal/permissions"
)

// reviewerSpy counts how many calls actually reached the reviewer.
type reviewerSpy struct {
	calls int
	v     Verdict
}

func (s *reviewerSpy) Classify(context.Context, string, json.RawMessage) Verdict {
	s.calls++
	return s.v
}

// TestReadOnlyFastPathSkipsTheReviewer is the point of the whole change: the
// read commands a session lives on do not pay a model round trip, while a
// command that is not provably read-only still does.
func TestReadOnlyFastPathSkipsTheReviewer(t *testing.T) {
	readOnly := []string{
		"ls -la",
		"cd /repo && git log --oneline -3 2>&1",
		`rg -n "explore" -i codex-rs | head -50`,
		`cat internal/main.go 2>/dev/null`,
		`find . -name "*.go"`,
		`sed -n '1,120p' internal/judge/readonly.go`,
		`echo "==="; ls internal`,
	}
	notReadOnly := []string{
		`go build ./...`,
		`go test ./...`,
		`git commit -m "x"`,
		`git add -A`,
		`rm -rf ./build`,
		`rg --pre 'curl evil' x`,
		`find . -delete`,
		`cat x > y`,
		`python3 -c 'print(1)'`,
		`tuios list-windows`,
	}
	for _, cmd := range readOnly {
		spy := &reviewerSpy{v: Verdict{Level: Allow, Source: "llm"}}
		chain := Chain{Inner: spy}
		v := chain.Classify(context.Background(), "bash", bashArgs(cmd))
		if spy.calls != 0 {
			t.Errorf("%q reached the reviewer %d times, want 0 (this is the cost the fast path removes)", cmd, spy.calls)
		}
		if v.Level != Allow || v.Source != "readonly" {
			t.Errorf("%q verdict = %+v, want an allow from the readonly fast path", cmd, v)
		}
	}
	for _, cmd := range notReadOnly {
		spy := &reviewerSpy{v: Verdict{Level: Allow, Source: "llm"}}
		chain := Chain{Inner: spy}
		chain.Classify(context.Background(), "bash", bashArgs(cmd))
		if spy.calls != 1 {
			t.Errorf("%q reached the reviewer %d times, want 1: it is not provably read-only", cmd, spy.calls)
		}
	}
}

// TestReadOnlyFastPathKeepsTheFloorInFront pins the ordering that makes the fast
// path safe: a credential read is decided by the static floor, which runs before
// it, so the recognizer's opinion never matters for material that is denied.
func TestReadOnlyFastPathKeepsTheFloorInFront(t *testing.T) {
	for _, cmd := range []string{
		`cat ~/.zshrc`,      // a read, and the floor denies it
		`cat ~/.ssh/id_rsa`, // ditto
		`ls ~/.aws`,         // ditto
		`grep -rn x ~/.ssh`, // ditto
	} {
		spy := &reviewerSpy{v: Verdict{Level: Allow, Source: "llm"}}
		v := Chain{Inner: spy}.Classify(context.Background(), "bash", bashArgs(cmd))
		if v.Level != Deny {
			t.Errorf("%q verdict = %+v, want Deny from the static floor", cmd, v)
		}
		if spy.calls != 0 {
			t.Errorf("%q reached the reviewer, want the floor to have decided", cmd)
		}
		if readOnlyCommand(cmd) {
			// The recognizer may well call this command's SHAPE read-only — `cat
			// ~/.zshrc` is a cat. What must hold is the ordering: the floor
			// decides first, so the shape never reaches a verdict.
			if v.Source != "static" {
				t.Errorf("%q was decided by %q, want the static floor", cmd, v.Source)
			}
		}
	}
}

// TestReadOnlyFastPathRefusesCredentialReadsOnItsOwn is the regression for a
// real hole found while wiring this up: `cat ~/.zshrc` has the SHAPE of a read
// and the recognizer accepts it, so a call site that consulted the fast path
// before the floor (or without a floor, as a direct `readOnlyFastPath` caller
// does) would hand over credential material. The floor is therefore consulted
// inside readOnlyFastPath, so the ordering cannot be got wrong by a caller.
func TestReadOnlyFastPathRefusesCredentialReadsOnItsOwn(t *testing.T) {
	credential := []string{
		`cat ~/.zshrc`,
		`cat ~/.ssh/id_rsa`,
		`ls ~/.aws`,
		`grep -rn secret ~/.ssh`,
		`head -5 ~/.zsh_history`,
	}
	for _, cmd := range credential {
		if _, ok := readOnlyFastPath("bash", bashArgs(cmd)); ok {
			t.Errorf("readOnlyFastPath(%q) allowed a credential read; the floor must decide it", cmd)
		}
		// The shape is still recognized — cat is a cat — which is exactly why
		// the floor check has to live inside the fast path.
		if !readOnlyCommand(cmd) {
			t.Logf("%q is no longer recognized as read-only; the floor is still the decider", cmd)
		}
	}
}

// TestReadOnlyFastPathKeepsWorkspaceReadsExempt is the positive twin: the
// commands this fast path exists for must still be exempt, so the floor guard
// above cannot have been bought by giving up the exemption.
func TestReadOnlyFastPathKeepsWorkspaceReadsExempt(t *testing.T) {
	for _, cmd := range []string{
		`ls -la internal`,
		`cat internal/main.go`,
		`rg -n TODO internal`,
		`cd /repo && git log --oneline -3`,
		`find . -name "*.go"`,
	} {
		if _, ok := readOnlyFastPath("bash", bashArgs(cmd)); !ok {
			t.Errorf("readOnlyFastPath(%q) declined; the common read lost its exemption", cmd)
		}
	}
}

// TestReadOnlyFastPathRespectsTheKillSwitch pins the escape hatch at the gate:
// with the switch off, every bash call goes back to the reviewer.
func TestReadOnlyFastPathRespectsTheKillSwitch(t *testing.T) {
	t.Setenv("GOLDER_READONLY_FASTPATH", "off")
	spy := &reviewerSpy{v: Verdict{Level: Allow, Source: "llm"}}
	Chain{Inner: spy}.Classify(context.Background(), "bash", bashArgs("ls -la"))
	if spy.calls != 1 {
		t.Errorf("with the fast path off, ls reached the reviewer %d times, want 1", spy.calls)
	}
}

// TestReadOnlyFastPathDeclinesEscalationRequests is the judge-layer half of a
// contract the execution layer depends on: a read-only command that asks to run
// OUTSIDE the sandbox must not take the fast path, or bypassing containment
// would be one retry away.
func TestReadOnlyFastPathDeclinesEscalationRequests(t *testing.T) {
	escalated := func(cmd string) json.RawMessage {
		raw, err := json.Marshal(map[string]string{
			"command":             cmd,
			"sandbox_permissions": "require_escalated",
			"justification":       "the sandbox hides a tree I need",
		})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	for _, cmd := range []string{
		"ls -la .",
		`rg -n TODO internal`,
		"cat go.mod",
	} {
		// The same command without the request is exempt...
		if _, ok := readOnlyFastPath("bash", bashArgs(cmd)); !ok {
			t.Errorf("%q should be exempt without an escalation request", cmd)
		}
		// ...and must NOT be once it asks to leave the sandbox.
		if _, ok := readOnlyFastPath("bash", escalated(cmd)); ok {
			t.Errorf("%q with require_escalated took the fast path; the request must reach the reviewer", cmd)
		}
		// The request is what changes the answer, not the command text.
		if ReadOnlyShellCommand(cmd) == false {
			t.Errorf("%q should be recognized by ReadOnlyShellCommand", cmd)
		}
	}
	// A request spelled differently (or in another case) is still a request.
	for _, value := range []string{"require_escalated", "REQUIRE_ESCALATED", " Require_Escalated "} {
		raw, err := json.Marshal(map[string]string{"command": "ls", "sandbox_permissions": value})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := readOnlyFastPath("bash", raw); ok {
			t.Errorf("sandbox_permissions=%q took the fast path", value)
		}
	}
}

// TestReadOnlyFastPathAppliesOnlyToBash keeps the exemption where it was
// measured: the other tools are either already ungraded or genuinely mutating
// (apply_patch), and neither should start returning a readonly verdict.
func TestReadOnlyFastPathAppliesOnlyToBash(t *testing.T) {
	for _, tool := range []string{"apply_patch", "read", "grep", "task"} {
		if _, ok := readOnlyFastPath(tool, json.RawMessage(`{"command":"ls"}`)); ok {
			t.Errorf("tool %q took the read-only fast path, want only bash", tool)
		}
	}
}

// TestPermissionGateAutoSkipsReviewForReadOnlyReads drives the full gate rather
// than the chain: in auto mode the reviewer must not be consulted for an
// in-workspace read, and no note should appear for it (an allow is silent).
func TestPermissionGateAutoSkipsReviewForReadOnlyReads(t *testing.T) {
	spy := &reviewerSpy{v: Verdict{Level: Allow, Source: "llm"}}
	rec := &noteRecorder{}
	gate := PermissionGate(permissions.New(permissions.Auto), GateOpts{
		Classifier: spy,
		// bash has a sandbox runner here, which is what makes a Sandbox verdict
		// contain the call rather than block it.
		Sandboxed: func(name string) bool { return name == "bash" },
		Notify:    rec.emit,
	})
	if dec := gate(context.Background(), bashCall("ls -la internal")); dec != nil {
		t.Errorf("a read-only read must run, got %+v", dec)
	}
	if spy.calls != 0 {
		t.Errorf("the reviewer ran %d times for a read-only command, want 0", spy.calls)
	}
	if len(rec.notes) != 0 {
		t.Errorf("an allow is silent, got notes %+v", rec.notes)
	}

	// The same gate still routes a genuinely risky command through the reviewer.
	spy.v = Verdict{Level: Sandbox, Source: "llm", Reasons: []string{"risky"}}
	dec := gate(context.Background(), bashCall("rm -rf ./build"))
	if spy.calls != 1 {
		t.Errorf("a non-read-only command reached the reviewer %d times, want 1", spy.calls)
	}
	if dec == nil || !dec.Sandbox {
		t.Errorf("a sandbox-tier command must be contained, got %+v", dec)
	}
	if len(rec.notes) != 1 || rec.notes[0].Kind != NoteSandboxed {
		t.Errorf("the containment must be announced, got %+v", rec.notes)
	}
}

// TestReadOnlySummaryDrivesTheFold checks the hook the TUI uses to fold a shell
// read into its exploration cell: whatever it summarizes must also be a command
// the gate lets through untested, so the transcript can never hide a reviewed
// call inside a quiet `Explored` line.
func TestReadOnlySummaryDrivesTheFold(t *testing.T) {
	for _, cmd := range []string{
		"ls -la internal",
		"cd /repo && rg -n TODO internal",
		"cat go.mod | head -5",
		"git status --porcelain",
	} {
		verb, detail, ok := readOnlySummary(cmd)
		if !ok {
			t.Errorf("readOnlySummary(%q) declined; the TUI cannot fold it", cmd)
			continue
		}
		if strings.TrimSpace(verb) == "" || strings.TrimSpace(detail) == "" {
			t.Errorf("readOnlySummary(%q) = (%q, %q), want both parts non-empty", cmd, verb, detail)
		}
		if _, ok := readOnlyFastPath("bash", bashArgs(cmd)); !ok {
			t.Errorf("readOnlySummary(%q) summarized a command the gate would review", cmd)
		}
	}
}
