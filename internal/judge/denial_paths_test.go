package judge

import (
	"os"
	"path/filepath"
	"testing"
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
	if fi, err := os.Stat("/Users"); err == nil && fi.IsDir() {
		if !PlausibleWritableRoot("/Users", cwd) {
			t.Error("an existing top-level directory is still a grant to offer")
		}
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
