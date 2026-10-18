package judge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/golderhome"
	"github.com/getan/golder/internal/grants/grantstest"
	"github.com/getan/golder/internal/permissions"
)

// TestPathsFromDenialReadsTheRefusedPath: the OS message names the path it
// refused — for reads, for writes, and in the several shapes the common tools
// use — and that is the path a dialog must offer. Everything else the denial
// says is not evidence about paths.
func TestPathsFromDenialReadsTheRefusedPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	cwd := t.TempDir()
	cases := []struct {
		name string
		out  string
		want string
	}{
		{"read", "sed: " + filepath.Join(home, "notes", "todo.md") + ": Operation not permitted\n",
			filepath.Join(home, "notes", "todo.md")},
		{"write create", "cp: cannot create regular file '" + filepath.Join(home, "cache", "out") + "': Operation not permitted\n",
			filepath.Join(home, "cache", "out")},
		{"write touch", "touch: cannot touch '" + filepath.Join(home, "cache", "stamp") + "': Read-only file system\n",
			filepath.Join(home, "cache", "stamp")},
		{"write dd", "dd: failed to open '" + filepath.Join(home, "cache", "img") + "': Permission denied\n",
			filepath.Join(home, "cache", "img")},
	}
	for _, c := range cases {
		if got := PathsFromDenial(c.out, cwd); got != c.want {
			t.Errorf("%s: PathsFromDenial = %q, want %q", c.name, got, c.want)
		}
	}

	// A denial that names nothing grantable, and one about a path inside the
	// workspace (an ordinary failure the sandbox had nothing to do with): no
	// grant is offered, which is the fail-closed direction.
	if got := PathsFromDenial("sh: Operation not permitted\n", cwd); got != "" {
		t.Errorf("nameless denial = %q, want no path", got)
	}
	inside := filepath.Join(cwd, "build", "out")
	if got := PathsFromDenial("cat: "+inside+": Permission denied\n", cwd); got != "" {
		t.Errorf("in-workspace denial = %q, want no path", got)
	}
	// macOS reports SIP-protected system files as "Operation not permitted".
	// That is not a sandbox path a grant can fix, and it must never become the
	// dialog's subject.
	if got := PathsFromDenial("ssh: Error opening /private/var/select/sh: Operation not permitted\n", cwd); got != "" {
		t.Errorf("SIP-protected system path = %q, want no path", got)
	}
	// SSH host keys are credential material: the static floor refuses them, so
	// a one-click re-admission is not a valid answer either.
	knownHosts := filepath.Join(home, ".ssh", "known_hosts")
	if got := PathsFromDenial("hostkeys: "+knownHosts+": Operation not permitted\n", cwd); got != "" {
		t.Errorf("credential path = %q, want no path", got)
	}
}

// TestDenialExplanationNamesTheRealBlocker: when no path is grantable, the
// message must say why and point at the one-call unisolated approval instead of
// offering a misleading path.
func TestDenialExplanationNamesTheRealBlocker(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	cwd := t.TempDir()
	knownHosts := filepath.Join(home, ".ssh", "known_hosts")
	out := "ssh: Error opening /private/var/select/sh: Operation not permitted\n" +
		"hostkeys: " + knownHosts + ": Operation not permitted\n"
	got := DenialExplanation(out, cwd)
	for _, want := range []string{"known_hosts", "credential material", "unisolated run"} {
		if !strings.Contains(got, want) {
			t.Errorf("explanation = %q, want %q", got, want)
		}
	}
	systemOnly := "ssh: Error opening /private/var/select/sh: Operation not permitted\n"
	if got := DenialExplanation(systemOnly, cwd); !strings.Contains(got, "SIP") || !strings.Contains(got, "unisolated") {
		t.Errorf("system-only explanation = %q, want the SIP diagnosis and the unisolated approval", got)
	}
}

// TestPlausibleWritableRootRefusesOnlyTheRootsThatAreNotChoices: "/" and "//"
// are never grants, but a root that does not exist yet is a normal target — a
// build directory, a volume that is not mounted this minute — and refusing
// those would hide the row for the paths people actually ask about. The
// backreference garbage that reached a dialog once is killed by the token rule
// above, not by looking at the disk.
func TestPlausibleWritableRootRefusesOnlyTheRootsThatAreNotChoices(t *testing.T) {
	cwd := t.TempDir()
	if PlausibleWritableRoot("/", cwd) || PlausibleWritableRoot("//", cwd) {
		t.Error("/ and // must never be grantable")
	}
	if PlausibleWritableRoot("/g", cwd) {
		t.Error(`/g is sed's s///g flag, not a path: a nonexistent direct child of "/" is never a grant`)
	}
	if !PlausibleWritableRoot("/nonexistent-top-level-xyz/build", cwd) {
		t.Error("a path under a not-yet-existing root is still a grant to offer")
	}
	if PlausibleWritableRoot("/private/var/select/sh", cwd) {
		t.Error("SIP-protected macOS system path must never be offered as a grant")
	}
	if fi, err := os.Stat("/Users"); err == nil && fi.IsDir() {
		if !PlausibleWritableRoot("/Users", cwd) {
			t.Error("an existing top-level directory is still a grant to offer")
		}
	}
}

// TestReportedCommandYieldsNoPath: the command from the field report — rg over
// a credential file piped through sed with a backreference and a /g flag — is
// the shape that produced "/g" in a re-admission dialog. The credential path
// itself is inside golder's own state and never grantable, and nothing else in
// the line is a path.
func TestReportedCommandYieldsNoPath(t *testing.T) {
	// Derive the credential path from the state directory itself, so the test
	// keeps asserting "a path in golder's own state is never grantable" no
	// matter where that state lives (GOLDER_HOME or ~/.golder).
	gh := golderhome.Dir()
	if gh == "" {
		t.Skip("no golder home")
	}
	cwd := t.TempDir()
	command := `rg -n --color never '.' ` + filepath.Join(gh, "proxy.toml") +
		` 2>/dev/null | sed -E 's/(=|:)[[:space:]]*"[^"]*"/\1 "<redacted>"/g; s#(//)[^@]*@#\1<redacted>@#g' | head -20`
	if got := GrantablePathFromText("command: "+command, "", cwd); got != "" {
		t.Errorf("reported command = %q, want no path", got)
	}
}

// TestPermissionGateEscalationWithNoGrantablePathAsksNothing: when the command
// names no grantable path, the gate has nothing to offer — the question must
// not open (a Deny-only dialog is noise), and the escape request fails closed.
func TestPermissionGateEscalationWithNoGrantablePathAsksNothing(t *testing.T) {
	grantstest.Install(t)
	asked := 0
	opts := escalationGateOpts(&noteRecorder{})
	opts.Cwd = t.TempDir()
	opts.Classifier = &stubClassifier{v: Verdict{Level: Allow, Risk: "low", Authorization: "unknown"}}
	opts.Confirm = func(context.Context, ApprovalRequest) ApprovalAnswer {
		asked++
		return ApprovalAnswer{Answered: true}
	}
	gate := PermissionGate(permissions.New(permissions.Auto), opts)
	call := toolCall("bash", `{"command":"sed -E 's#x#y#g; /g' f.txt","sandbox_permissions":"require_escalated","justification":"verify the path offer"}`)
	dec := gate(context.Background(), call)
	if dec == nil || !dec.Block {
		t.Fatalf("an escape with nothing to re-admit must fail closed, got %+v", dec)
	}
	if asked != 0 {
		t.Errorf("asked = %d, want no question for a command with no grantable path", asked)
	}
}

// TestGrantablePathFromTextReadsTheCommandWithTheBoundaryParser: the command's
// paths come from the same reader the workspace boundary uses, so a script is
// one word rather than a bag of path-shaped tokens — and a real operand still
// comes through.
func TestGrantablePathFromTextReadsTheCommandWithTheBoundaryParser(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	cwd := t.TempDir()
	dest := filepath.Join(home, "backup", "out")
	if got := GrantablePathFromText("cp seed "+dest, "", cwd); got != dest {
		t.Errorf("operand = %q, want %q", got, dest)
	}
}

// TestPathTokensRejectShellExpressions: a token that starts like a path but
// carries shell syntax is an expression, not a literal path. The sed
// backreference in a s#…#…# command reached a dialog once as "/\1"; skipping it
// hides a row, keeping it grants something no one chose.
func TestPathTokensRejectShellExpressions(t *testing.T) {
	got := pathTokens(`foo=/\1 bar baz=/Volumes/KIOXIA/build`)
	if len(got) != 1 || got[0] != "/Volumes/KIOXIA/build" {
		t.Errorf("pathTokens = %v, want only the literal path", got)
	}
}

// TestGrantablePathFromTextPrefersTheJustification: the justification is prose
// written by the model, so its tokens are paths; a command's tokens are shell
// expressions. The command still backs it up for the calls that name their path
// plainly — and nothing here may invent one.
func TestGrantablePathFromTextPrefersTheJustification(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	cwd := t.TempDir()
	// The only path-shaped things in this command are expressions: the "//"
	// that normalizes to the root (never a grant) and the backreference, which
	// the token rule drops.
	command := `sed -E 's#(//)[^@]*@#/\1<redacted>@#g' file.txt`
	if got := GrantablePathFromText(command, "", cwd); got != "" {
		t.Errorf("command text alone = %q, want no path", got)
	}
	justification := "read " + filepath.Join(home, "notes", "todo.md") + " to see which port the proxy uses"
	if got, want := GrantablePathFromText(command, justification, cwd), filepath.Join(home, "notes", "todo.md"); got != want {
		t.Errorf("justification path = %q, want %q", got, want)
	}

	// The same class of token with no backreference: a sed global flag.
	if got := GrantablePathFromText(`sed -E 's#x#y#g; /g'`, "", cwd); got != "" {
		t.Errorf("sed flag = %q, want no path", got)
	}
}
