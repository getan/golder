package judge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/grants/grantstest"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/shellfacts"
)

// The two commands from the report, verbatim: the clone that was approved and
// the clone that was blocked had the same shape, and the only difference the
// gate could see was which one the reviewer happened to grant Direct.
const (
	cloneAgentS      = `git -c http.proxy=http://127.0.0.1:7897 clone https://github.com/simular-ai/Agent-S.git ../Agent-S`
	cloneBrowserUse  = `git -c http.proxy=http://127.0.0.1:7897 clone https://github.com/browser-use/browser-use.git ../browser-use`
	cloneEscalated   = `{"command":"` + cloneBrowserUse + `","sandbox_permissions":"require_escalated","justification":"the user asked to clone this public repository"}`
	cloneProxyPrefix = "http://127.0.0.1:7897"
)

// cloneTarget is the single destination a command names, as the readers in this
// package see it. "" means no destination could be named.
func cloneTarget(t *testing.T, command string) string {
	t.Helper()
	plan := shellfacts.Parse(command)
	if !plan.Parsed {
		t.Fatalf("parse %q: %s", command, plan.ParseError)
	}
	var got []string
	for _, c := range plan.AllCommands() {
		got = append(got, cloneTargetPaths(c)...)
	}
	if len(got) > 1 {
		t.Fatalf("%q named %d destinations: %v", command, len(got), got)
	}
	if len(got) == 0 {
		return ""
	}
	return got[0]
}

// TestCloneTargetPaths pins the reader: the destination is the second POSITIONAL
// word after the clone subcommand, and every other word on the line — a config
// setting, a proxy URL, a repository URL, an option's value, a derived name — is
// not it.
func TestCloneTargetPaths(t *testing.T) {
	home, _ := os.UserHomeDir()
	cases := []struct {
		name    string
		command string
		want    string
	}{
		{"the reported pair", cloneAgentS, "../Agent-S"},
		{"the reported pair, second repo", cloneBrowserUse, "../browser-use"},
		{"plain", `git clone https://github.com/org/repo.git ../repo`, "../repo"},
		{"attached config setting", `git -chttp.proxy=http://127.0.0.1:7897 clone https://github.com/org/repo.git ../repo`, "../repo"},
		{"two config settings", `git -c http.proxy=http://127.0.0.1:7897 -c advice.detachedHead=false clone https://github.com/org/repo.git ../repo`, "../repo"},
		{"long options with values", `git clone --depth 1 --branch main https://github.com/org/repo.git ../repo`, "../repo"},
		{"attached long values", `git clone --depth=1 --single-branch https://github.com/org/repo.git ../repo`, "../repo"},
		{"short option value", `git clone -b main https://github.com/org/repo.git ../repo`, "../repo"},
		{"short option value attached", `git clone -bmain https://github.com/org/repo.git ../repo`, "../repo"},
		{"quiet cluster", `git clone -qb main https://github.com/org/repo.git ../repo`, "../repo"},
		{"origin value", `git clone -o upstream https://github.com/org/repo.git ../repo`, "../repo"},
		{"separate git dir", `git clone --separate-git-dir=/tmp/g https://github.com/org/repo.git ../repo`, "../repo"},
		{"end of options", `git clone -- https://github.com/org/repo.git ../repo`, "../repo"},
		{"quoted destination with spaces", `git clone https://github.com/org/repo.git "../repo with spaces"`, "../repo with spaces"},
		{"absolute destination", `git clone https://github.com/org/repo.git /Volumes/KIOXIA/repo`, "/Volumes/KIOXIA/repo"},
		{"home destination", `git clone https://github.com/org/repo.git ~/repos/repo`, filepath.Join(home, "repos/repo")},
		{"env prefix", `env GIT_TERMINAL_PROMPT=0 git clone https://github.com/org/repo.git ../repo`, "../repo"},
		{"git -C folds into a relative destination", `git -C tools clone https://github.com/org/repo.git ../repo`, "repo"},
		{"git -C with an absolute destination", `git -C /elsewhere/src clone https://github.com/org/repo.git /Volumes/KIOXIA/repo`, "/Volumes/KIOXIA/repo"},
		{"git -C absolute folds too", `git -C /elsewhere/src clone https://github.com/org/repo.git ../repo`, "/elsewhere/repo"},
		{"git -C attached", `git -C/elsewhere/src clone https://github.com/org/repo.git ../repo`, "/elsewhere/repo"},
		{"no destination is not the parent", `git clone https://github.com/org/repo.git`, ""},
		{"bare clone has no destination", `git clone --bare https://github.com/org/repo.git`, ""},
		{"a derived name is not the destination", `git clone --mirror /outside/source.git`, ""},
		{"unknown option", `git clone --frobnicate x https://github.com/org/repo.git ../repo`, ""},
		{"dynamic destination", `git clone https://github.com/org/repo.git "$DEST"`, ""},
		{"glob destination", `git clone https://github.com/org/repo.git ../repo*`, ""},
		{"dynamic git -C", `git -C "$DIR" clone https://github.com/org/repo.git ../repo`, ""},
		{"quoted tilde is not the home directory", `git clone https://github.com/org/repo.git "~/repo"`, ""},
		{"three positionals", `git clone https://github.com/org/repo.git ../a ../b`, ""},
		{"option value eats the destination", `git clone --depth 1 https://github.com/org/repo.git`, ""},
		{"not a clone", `git status --short`, ""},
		{"the subcommand must be clone", `git config clone.url`, ""},
		{"a clone is not named by another program", `echo git clone https://github.com/org/repo.git ../repo`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := cloneTarget(t, c.command); got != c.want {
				t.Errorf("destination of %q = %q, want %q", c.command, got, c.want)
			}
		})
	}
}

// TestCloneInsideAnInlineShellProgram: `sh -c 'git clone … ../repo'` is a command
// line inside one word, and the boundary reader follows it — the same recursion
// that finds the path in `sh -c 'cat /outside/x'`. The write side has to follow it
// too, or a destination reached this way would be reported as a path and then
// handled as a read.
func TestCloneInsideAnInlineShellProgram(t *testing.T) {
	const command = `sh -c 'git clone https://github.com/org/repo.git ../repo'`
	args, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	got := bashPaths(args)
	if len(got) != 1 || got[0] != "../repo" {
		t.Fatalf("boundary paths = %v, want exactly [../repo]", got)
	}
	if !cloneCreates(command, "../repo") {
		t.Error("the write side did not follow the inline program the reader followed")
	}
}

// TestCloneBoundaryPathsAreOnlyTheDestination: the boundary gate asks about what
// the command names, so a clone must contribute exactly one path — its
// destination. The repository URL and the proxy address are the two words a
// length-based reader would have offered as local directories, and neither is
// something this process may grant.
func TestCloneBoundaryPathsAreOnlyTheDestination(t *testing.T) {
	for _, c := range []struct {
		name, command, want string
	}{
		{"agent-s", cloneAgentS, "../Agent-S"},
		{"browser-use", cloneBrowserUse, "../browser-use"},
	} {
		t.Run(c.name, func(t *testing.T) {
			args, err := json.Marshal(map[string]string{"command": c.command})
			if err != nil {
				t.Fatal(err)
			}
			got := bashPaths(args)
			if len(got) != 1 || got[0] != c.want {
				t.Fatalf("boundary paths = %v, want exactly [%q]", got, c.want)
			}
			for _, p := range got {
				if strings.Contains(p, cloneProxyPrefix) || strings.Contains(p, "github.com") {
					t.Errorf("a URL reached the local-path list: %q", p)
				}
			}
		})
	}
}

// TestCloneGrantablePathResolvesAgainstCwd: the escalation fallback offers the
// destination, which is a RELATIVE word, so it has to be resolved against the
// run's directory before it can be granted — a step that used to drop it
// entirely, leaving the refusal with no directory to name.
func TestCloneGrantablePathResolvesAgainstCwd(t *testing.T) {
	const cwd = "/Users/example/project"
	cases := []struct{ name, command, want string }{
		{"relative sibling", cloneBrowserUse, "/Users/example/browser-use"},
		{"nested relative", `git clone https://github.com/org/repo.git ../vendor/repo`, "/Users/example/vendor/repo"},
		{"inside the workspace is not offered", `git clone https://github.com/org/repo.git repo`, ""},
		{"no destination is not offered", `git clone https://github.com/org/repo.git`, ""},
		{"dynamic destination is not offered", `git clone https://github.com/org/repo.git "$DEST"`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := GrantablePathFromText("command: "+c.command, "the user asked for this clone", cwd)
			if got != c.want {
				t.Errorf("GrantablePathFromText(%q) = %q, want %q", c.command, got, c.want)
			}
		})
	}
}

// TestPathWriteNeedForACloneDestination: the absence signal already calls a
// clone destination a write — but only the write-side reader knows that, and the
// dialog has to lead with the row that actually unblocks the command.
func TestPathWriteNeedForACloneDestination(t *testing.T) {
	target := filepath.Join(t.TempDir(), "repo")
	if !PathWriteNeed(target, cloneBrowserUse) {
		t.Error("a clone destination does not exist yet, so it can only be needed for writing")
	}
}

// cloneBoundaryOpts builds the boundary gate around a workspace under the home
// directory, with a dialog the test answers. The workspace is a real directory
// under $HOME rather than t.TempDir(), because the temp tree is a system path the
// boundary gate deliberately never asks about — a fixture detail, not a decision
// under test.
func cloneBoundaryOpts(t *testing.T, answer func(ApprovalRequest) ApprovalAnswer) (BoundaryOpts, string) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	base := filepath.Join(home, fmt.Sprintf("golder-clone-gate-test-%d", os.Getpid()))
	root := filepath.Join(base, "workspace")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("workspace: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	opts := BoundaryOpts{WorkspaceRoot: root}
	if answer != nil {
		opts.Confirm = func(_ context.Context, req ApprovalRequest) ApprovalAnswer { return answer(req) }
	}
	return opts, base
}

// TestBoundaryGateAsksForTheCloneDestination is the regression the report is
// about. A clone into a sibling of the workspace must reach the boundary gate as
// a WRITE on that sibling directory — not as a read, and not widened to the tree
// holding every sibling — and the same call's network side must still be put to
// the person in the same prompt.
func TestBoundaryGateAsksForTheCloneDestination(t *testing.T) {
	grantstest.Install(t)
	for _, c := range []struct {
		name, command, destination string
	}{
		{"agent-s", cloneAgentS, "Agent-S"},
		{"browser-use", cloneBrowserUse, "browser-use"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var asked []ApprovalRequest
			opts, base := cloneBoundaryOpts(t, func(req ApprovalRequest) ApprovalAnswer {
				asked = append(asked, req)
				return ApprovalAnswer{Approve: true, Answered: true, Network: true, WriteRoot: req.BoundaryDir}
			})
			var granted string
			opts.GrantWritable = func(root string, persist bool) (bool, string) {
				granted = root
				return true, ""
			}
			gate := BoundaryGate(opts)
			if gate == nil {
				t.Fatal("no boundary gate")
			}
			args, _ := json.Marshal(map[string]string{"command": c.command, "network": "on"})
			dec := gate(context.Background(), agentcore.AgentToolCall{ID: "clone-1", Name: "bash", Arguments: args})
			if dec != nil && dec.Block {
				t.Fatalf("an approved clone was blocked: %+v", dec)
			}
			if dec != nil && dec.Direct {
				t.Errorf("approving a directory must not grant an unconfined run: %+v", dec)
			}
			if len(asked) != 1 {
				t.Fatalf("asked %d questions, want exactly one", len(asked))
			}
			req := asked[0]
			if req.Kind != ApprovalBoundary {
				t.Errorf("kind = %q, want %q", req.Kind, ApprovalBoundary)
			}
			if !req.BoundaryWrite {
				t.Error("a clone creates its destination, so the question must be about writing")
			}
			// The gate resolves through symlinks, so the expected spelling is the
			// canonical one (§:"/Users" is a firmlink on macOS).
			want := permissions.Canonical(filepath.Join(base, c.destination))
			if req.BoundaryDir != want {
				t.Errorf("offered directory = %q, want the destination itself (%q)", req.BoundaryDir, want)
			}
			if req.BoundaryDir == permissions.Canonical(base) {
				t.Errorf("offered directory widened to the tree holding the workspace: %q", req.BoundaryDir)
			}
			if !req.NetworkRequest {
				t.Error("the clone asked for network, so the same prompt must carry the network side")
			}
			if granted != want {
				t.Errorf("registered writable root = %q, want %q", granted, want)
			}
		})
	}
}

// TestBoundaryGateRefusalStillBlocksAClone: the destination is now visible, so a
// "no" is a decision about it rather than a silent nothing — and the message
// names the directory and the command that would grant it.
func TestBoundaryGateRefusalStillBlocksAClone(t *testing.T) {
	grantstest.Install(t)
	opts, base := cloneBoundaryOpts(t, func(ApprovalRequest) ApprovalAnswer {
		return ApprovalAnswer{Answered: true}
	})
	gate := BoundaryGate(opts)
	args, _ := json.Marshal(map[string]string{"command": cloneBrowserUse})
	dec := gate(context.Background(), agentcore.AgentToolCall{ID: "clone-2", Name: "bash", Arguments: args})
	if dec == nil || !dec.Block {
		t.Fatalf("a refused destination must block, got %+v", dec)
	}
	got := agentcore.ContentToText(*dec.Content)
	want := permissions.Canonical(filepath.Join(base, "browser-use"))
	if !strings.Contains(got, want) {
		t.Errorf("refusal = %q, want it to name %q", got, want)
	}
	if !strings.Contains(got, "/boundary writable add") {
		t.Errorf("refusal = %q, want the write-side fix named", got)
	}
}

// TestBoundaryGateCloneNoPromptFailsClosed: a driver with no dialog cannot ask,
// so the clone must be refused with the directory named — never run on the
// strength of the user's natural-language request for it.
func TestBoundaryGateCloneNoPromptFailsClosed(t *testing.T) {
	grantstest.Install(t)
	opts, base := cloneBoundaryOpts(t, nil)
	gate := BoundaryGate(opts)
	args, _ := json.Marshal(map[string]string{"command": cloneBrowserUse})
	dec := gate(context.Background(), agentcore.AgentToolCall{ID: "clone-3", Name: "bash", Arguments: args})
	if dec == nil || !dec.Block {
		t.Fatalf("no prompt must fail closed, got %+v", dec)
	}
	got := agentcore.ContentToText(*dec.Content)
	if !strings.Contains(got, permissions.Canonical(filepath.Join(base, "browser-use"))) {
		t.Errorf("refusal = %q, want the directory that needs authorization", got)
	}
	if !strings.Contains(got, "no prompt is available") {
		t.Errorf("refusal = %q, want it to say nobody could be asked", got)
	}
}

// TestCloneWithNoDestinationNamesNothingToGrant: with no explicit destination git
// derives the directory from the repository name, which this layer cannot
// compute. The answer must be "no path" — never the parent tree — so the evasion
// of naming a destination cannot widen a grant.
func TestCloneWithNoDestinationNamesNothingToGrant(t *testing.T) {
	const cwd = "/Users/example/project"
	if got := GrantablePathFromText("command: git clone https://github.com/org/repo.git", "", cwd); got != "" {
		t.Errorf("grantable path = %q, want none", got)
	}
	if got := bashPaths(json.RawMessage(`{"command":"git clone https://github.com/org/repo.git"}`)); len(got) != 0 {
		t.Errorf("boundary paths = %v, want none", got)
	}

	// The refused escape must not offer the parent directory as the smaller fix.
	rec := &noteRecorder{}
	asked := false
	gate := PermissionGate(permissions.New(permissions.Auto), GateOpts{
		Classifier: &stubClassifier{v: Verdict{Level: Allow, Reasons: []string{"downloads a public repository"}}},
		Sandboxed:  func(string) bool { return true },
		Notify:     rec.emit,
		Cwd:        cwd,
		Confirm: func(context.Context, ApprovalRequest) ApprovalAnswer {
			asked = true
			return ApprovalAnswer{Approve: true, Answered: true}
		},
	})
	args := json.RawMessage(`{"command":"git clone https://github.com/org/repo.git","sandbox_permissions":"require_escalated","justification":"please"}`)
	dec := gate(context.Background(), agentcore.AgentToolCall{ID: "clone-4", Name: "bash", Arguments: args})
	if dec == nil || !dec.Block {
		t.Fatalf("an escape with no destination must be refused, got %+v", dec)
	}
	if asked {
		t.Error("there was no path to offer, so no dialog should have opened")
	}
	got := agentcore.ContentToText(*dec.Content)
	if strings.Contains(got, cwd) || strings.Contains(got, "/Users/example\"") {
		t.Errorf("refusal = %q, want it not to name a directory that was never asked about", got)
	}
	if !strings.Contains(got, "switch /permissions to ask") {
		t.Errorf("refusal = %q, want the mode-switch advice kept", got)
	}
}

// TestEscalatedCloneFallsBackToNarrowGrant is the acceptance criterion the report
// ends on: a reviewer that grades the command safe but does NOT grant Direct must
// leave the clone runnable inside the sandbox with its destination written —
// instead of the only outcomes being "run the whole call unconfined" and "fail".
func TestEscalatedCloneFallsBackToNarrowGrant(t *testing.T) {
	grantstest.Install(t)
	const cwd = "/Users/example/project"
	rec := &noteRecorder{}
	var asked []ApprovalRequest
	var granted []string
	opts := GateOpts{
		Classifier: &stubClassifier{v: Verdict{
			Level: Allow, Risk: "low", Authorization: "high",
			Reasons: []string{"clones a public repository the user asked for"},
		}},
		Sandboxed: func(name string) bool { return name == "bash" },
		Notify:    rec.emit,
		Cwd:       cwd,
		Confirm: func(_ context.Context, req ApprovalRequest) ApprovalAnswer {
			asked = append(asked, req)
			return ApprovalAnswer{Approve: true, Answered: true, WritableRoot: req.ReadRoot}
		},
		GrantWritable: func(root string, persist bool) (bool, string) {
			granted = append(granted, root)
			return true, ""
		},
	}
	gate := PermissionGate(permissions.New(permissions.Auto), opts)
	dec := gate(context.Background(), agentcore.AgentToolCall{
		ID: "clone-5", Name: "bash", Arguments: json.RawMessage(cloneEscalated),
	})
	if dec == nil || dec.Block {
		t.Fatalf("the narrower grant must keep the clone runnable, got %+v", dec)
	}
	if !dec.Sandbox {
		t.Errorf("the call must stay contained, got %+v", dec)
	}
	if dec.Direct {
		t.Errorf("a directory grant must never carry Direct, got %+v", dec)
	}
	if len(asked) != 1 {
		t.Fatalf("asked %d questions, want one re-admission", len(asked))
	}
	req := asked[0]
	if req.Kind != ApprovalReAdmit {
		t.Fatalf("kind = %q, want %q (the smaller fix, not the escape)", req.Kind, ApprovalReAdmit)
	}
	if want := "/Users/example/browser-use"; req.ReadRoot != want {
		t.Errorf("offered path = %q, want the clone destination %q", req.ReadRoot, want)
	}
	if !req.NeedsWrite {
		t.Error("a clone destination is a write, and the dialog must lead with that row")
	}
	if len(granted) != 1 || granted[0] != "/Users/example/browser-use" {
		t.Errorf("granted = %v, want the destination registered for writing", granted)
	}
	last := rec.notes[len(rec.notes)-1]
	if last.Kind != NoteSandboxed || !strings.Contains(last.Rationale, "browser-use") {
		t.Errorf("note = %+v, want the contained outcome naming the granted path", last)
	}
}

// TestCloneEscapeRefusalNamesTheDestination: the same request with the grant
// declined must still tell the model where the smaller fix is, which is the
// difference between "make it work inside the sandbox" and "here is the
// directory to authorize".
func TestCloneEscapeRefusalNamesTheDestination(t *testing.T) {
	grantstest.Install(t)
	rec := &noteRecorder{}
	gate := PermissionGate(permissions.New(permissions.Auto), GateOpts{
		Classifier: &stubClassifier{v: Verdict{Level: Allow, Reasons: []string{"clones a public repository"}}},
		Sandboxed:  func(string) bool { return true },
		Notify:     rec.emit,
		Cwd:        "/Users/example/project",
		Confirm: func(context.Context, ApprovalRequest) ApprovalAnswer {
			return ApprovalAnswer{Answered: true}
		},
	})
	dec := gate(context.Background(), agentcore.AgentToolCall{
		ID: "clone-6", Name: "bash", Arguments: json.RawMessage(cloneEscalated),
	})
	if dec == nil || !dec.Block {
		t.Fatalf("a declined grant must refuse the escape, got %+v", dec)
	}
	got := agentcore.ContentToText(*dec.Content)
	if !strings.Contains(got, "/Users/example/browser-use") {
		t.Errorf("refusal = %q, want the destination named", got)
	}
	if !strings.Contains(got, "sandbox escape declined") {
		t.Errorf("refusal = %q, want it to say which decision was refused", got)
	}
}

// TestCloneNetworkIsStillItsOwnDecision: granting the destination says nothing
// about the network. The same prompt carries both sides, and an answer that
// approves the path but not the network must leave the call refused — never run
// with an unapproved outbound connection.
func TestCloneNetworkIsStillItsOwnDecision(t *testing.T) {
	grantstest.Install(t)
	opts, base := cloneBoundaryOpts(t, func(ApprovalRequest) ApprovalAnswer {
		// The person says yes to the path and no to the network.
		return ApprovalAnswer{Approve: true, Answered: true}
	})
	_ = base
	opts.GrantWritable = func(string, bool) (bool, string) { return true, "" }
	coord := NewAskCoord()
	opts.AskCoord = coord
	boundary := BoundaryGate(opts)
	if boundary == nil {
		t.Fatal("no boundary gate")
	}
	args, _ := json.Marshal(map[string]string{"command": cloneBrowserUse, "network": "on"})
	call := agentcore.AgentToolCall{ID: "clone-7", Name: "bash", Arguments: args}
	if dec := boundary(context.Background(), call); dec != nil && dec.Block {
		t.Fatalf("the approved directory was blocked: %+v", dec)
	}

	// The combined prompt already answered the network side, so neither gate may
	// open a second dialog about it.
	networkAsked := false
	perm := PermissionGate(permissions.New(permissions.Auto), GateOpts{
		Classifier: &stubClassifier{v: Verdict{Level: Allow}},
		Sandboxed:  func(string) bool { return true },
		AskCoord:   coord,
		Confirm: func(context.Context, ApprovalRequest) ApprovalAnswer {
			networkAsked = true
			return ApprovalAnswer{Answered: true, Network: true}
		},
	})
	if dec := perm(context.Background(), call); dec == nil || !dec.Block {
		t.Fatalf("an unapproved network must refuse the call, got %+v", dec)
	}

	// And the other way round: an approved network arrives with the grant.
	coord2 := NewAskCoord()
	opts2, _ := cloneBoundaryOpts(t, func(ApprovalRequest) ApprovalAnswer {
		return ApprovalAnswer{Approve: true, Answered: true, Network: true}
	})
	opts2.GrantWritable = func(string, bool) (bool, string) { return true, "" }
	opts2.AskCoord = coord2
	gate2 := BoundaryGate(opts2)
	rec := &noteRecorder{}
	permGrant := PermissionGate(permissions.New(permissions.Auto), GateOpts{
		Classifier: &stubClassifier{v: Verdict{Level: Allow}},
		Sandboxed:  func(string) bool { return true },
		Notify:     rec.emit,
		AskCoord:   coord2,
		Confirm: func(context.Context, ApprovalRequest) ApprovalAnswer {
			networkAsked = true
			return ApprovalAnswer{Answered: true}
		},
	})
	call2 := agentcore.AgentToolCall{ID: "clone-8", Name: "bash", Arguments: args}
	if dec := gate2(context.Background(), call2); dec != nil && dec.Block {
		t.Fatalf("boundary refused an approved clone: %+v", dec)
	}
	dec := permGrant(context.Background(), call2)
	if dec == nil || dec.Block || !dec.Network {
		t.Fatalf("the approved network must reach the call, got %+v", dec)
	}
	if dec.Direct {
		t.Errorf("a path and network approval is not an escape: %+v", dec)
	}
	if networkAsked {
		t.Error("the network question was asked again after the combined prompt had answered it")
	}
}
