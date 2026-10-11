package judge

// Tests for the bash path scanner (bashpaths.go): what a command line exposes
// to the read-scope gate, and — just as important — what it must stay quiet
// about. The corpus is written against a fabricated sandbox root so the cases
// read like real commands without naming anything on the machine.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sandboxPaths builds three fabricated trees under one temp root and returns
// their paths: the workspace, an unrelated repo next to it, and a fake home.
func sandboxPaths(t *testing.T) (ws, out, home string) {
	t.Helper()
	root := t.TempDir()
	ws = filepath.Join(root, "workspace")
	out = filepath.Join(root, "outside-repo")
	home = filepath.Join(root, "fakehome")
	for _, dir := range []string{
		filepath.Join(ws, "internal"),
		filepath.Join(out, "src"),
		filepath.Join(home, ".config", "ghostty"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return ws, out, home
}

// TestBashPathCandidates pins the scanner's sight: the shapes it must see, the
// shapes it must stay quiet about, and the ones it is known to miss.
func TestBashPathCandidates(t *testing.T) {
	ws, out, home := sandboxPaths(t)

	cases := []struct {
		name string
		cmd  string
		want []string // nil means "no candidate at all"
	}{
		// The shape this exists for: reading outside the workspace.
		{"read config", "cat " + home + "/.config/ghostty/config", []string{home + "/.config/ghostty/config"}},
		{"two operands", "diff " + out + "/src/a.go " + out + "/src/b.go",
			[]string{out + "/src/a.go", out + "/src/b.go"}},
		{"parent escape", "cat ../outside-repo/logs/app.log", []string{"../outside-repo/logs/app.log"}},
		{"variable prefix", "FOO=1 cat " + out + "/src/a.go", []string{out + "/src/a.go"}},
		{"env wrapper", "env cat " + out + "/src/a.go", []string{out + "/src/a.go"}},
		{"sudo prefix", "sudo cat " + out + "/src/a.go", []string{out + "/src/a.go"}},
		{"pipeline segment", "cat " + out + "/src/a.go | wc -l", []string{out + "/src/a.go"}},
		{"and segment", "grep -c . " + out + "/src/a.go && echo done", []string{out + "/src/a.go"}},
		{"input redirect", "cat < " + out + "/src/a.go", []string{out + "/src/a.go"}},
		{"find path", "find " + out + " -name '*.go'", []string{out}},
		{"command substitution", "cat $(echo " + home + "/notes.md)", []string{home + "/notes.md)"}},
		{"glob", "cat " + home + "/*", []string{home + "/*"}},

		// $HOME is the one variable whose value is knowable here.
		{"HOME var", "cat $HOME/.zshrc", []string{filepath.Join(homeOf(t), ".zshrc")}},
		{"braced HOME var", "cat ${HOME}/.zshrc", []string{filepath.Join(homeOf(t), ".zshrc")}},
		{"unknown var stays unknown", "cat $LOG_DIR/app.log", nil},

		// Interpreters: an inline program's embedded path is a real read.
		{"python program", `python3 -c 'open("` + out + `/src/a.go")'`, []string{out + "/src/a.go"}},
		{"node program", `node -e 'require("` + out + `/src/a.go")'`, []string{out + "/src/a.go"}},
		{"shell program", "bash -c 'cat " + out + "/src/a.go'", []string{out + "/src/a.go"}},
		{"shell flag cluster", `sh -lc "cat ` + home + `/notes.md"`, []string{home + "/notes.md"}},

		// Must stay quiet.
		{"relative path", "cat internal/main.go", nil},
		{"bare command", "make -j8", nil},
		{"printer renders, does not read", "echo /etc/hosts", nil},
		{"printer with variable", "echo $HOME", nil},
		{"git message", `git commit -m "fix /outside/thing"`, nil},
		{"url is not a path", "curl https://example.com/api", nil},
		{"pattern position of a search", `rg -n '/usr/local' internal`, nil},
		{"quoted prose in a non-reader", `printf 'see /etc/hosts\n'`, nil},
		// A python program with no path in it asks nothing.
		{"program without a path", `python3 -c 'print(1+1)'`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := bashPathCandidates(c.cmd)
			if len(got) != len(c.want) {
				t.Fatalf("candidates = %v, want %v\ncommand: %s", got, c.want, c.cmd)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("candidate %d = %q, want %q", i, got[i], c.want[i])
				}
			}
		})
	}

	// The workspace path itself is a candidate when a command names it — the
	// GATE decides it is inside, not the scanner. Asserting that keeps the two
	// responsibilities apart.
	if got := bashPathCandidates("cat " + ws + "/build.sh"); len(got) != 1 || got[0] != ws+"/build.sh" {
		t.Errorf("workspace path = %v, want it reported (the gate filters, not the scanner)", got)
	}
}

// homeOf returns the home directory the variable expansion reads, so the test
// asserts against the same value the code substitutes.
func homeOf(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	return home
}

// TestBashPathCandidatesKnownLimits documents the blind spots rather than
// leaving them to be discovered. Each is a real read the scanner cannot see;
// the sandbox, not this scanner, is what constrains them.
func TestBashPathCandidatesKnownLimits(t *testing.T) {
	for _, cmd := range []string{
		"cat $UNKNOWN_VAR/secret",
		"./script-that-reads-files.sh",
		"jq '.path' " + "/tmp/x.json", // /tmp is whitelisted, but the principle: non-reader commands
	} {
		if got := bashPathCandidates(cmd); len(got) > 0 {
			t.Logf("%q -> %v (documented limit: expected to be invisible or exempt)", cmd, got)
		}
	}
}

// TestExpandKnownVars covers the substitution itself: $HOME in both spellings,
// and everything else untouched.
func TestExpandKnownVars(t *testing.T) {
	home := homeOf(t)
	for _, c := range []struct{ in, want string }{
		{"$HOME/.zshrc", home + "/.zshrc"},
		{"${HOME}/.zshrc", home + "/.zshrc"},
		{"$HOME", home},
		{"$OTHER/x", "$OTHER/x"},
		{"/abs/path", "/abs/path"},
	} {
		if got := expandKnownVars(c.in); got != c.want {
			t.Errorf("expandKnownVars(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestEmbeddedPaths covers the program-string scan: absolute paths are pulled
// out, relative fragments and prose are not.
func TestEmbeddedPaths(t *testing.T) {
	for _, c := range []struct {
		prog string
		want []string
	}{
		{`open("/outside/x")`, []string{"/outside/x"}},
		{`x = "/a/b"; y = open("/c")`, []string{"/a/b", "/c"}},
		{`print(1+1)`, nil},
		{`relative/path`, nil},
		{`re.match("/api/v1", u)`, []string{"/api/v1"}}, // the accepted false positive
	} {
		got := embeddedPaths(c.prog)
		if len(got) != len(c.want) {
			t.Fatalf("embeddedPaths(%q) = %v, want %v", c.prog, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("embeddedPaths(%q)[%d] = %q, want %q", c.prog, i, got[i], c.want[i])
			}
		}
	}
}

// TestPathShapedWord pins the word filter: what counts as a boundary-shaped
// path, and what does not.
func TestPathShapedWord(t *testing.T) {
	for _, c := range []struct {
		word string
		want bool
	}{
		{"/abs", true},
		{"~/home", true},
		{"../up", true},
		{"..", true},
		{"relative", false},
		{"./dotted", false},
		{"-flag", false},
		{"https://example.com/x", false},
		{"", false},
	} {
		if got := pathShapedWord(c.word); got != c.want {
			t.Errorf("pathShapedWord(%q) = %v, want %v", c.word, got, c.want)
		}
	}
}

// TestBashPathCandidatesNoTrailingCut keeps a reminder next to the glob case:
// a scanner that trims trailing punctuation would break the glob, so the raw
// word is passed through and the gate resolves it.
func TestBashPathCandidatesNoTrailingCut(t *testing.T) {
	if got := bashPathCandidates("cat /outside/*.log"); len(got) != 1 || !strings.HasSuffix(got[0], "*.log") {
		t.Errorf("glob candidate = %v, want the wildcard preserved", got)
	}
}
