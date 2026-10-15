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
	// A real file for the interpreter-program cases: a program's path is only a
	// path if it names something, which is the predicate those cases exercise.
	if err := os.WriteFile(filepath.Join(out, "src", "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
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
		// cd's operand is where everything after it reads from, so it is a
		// boundary question in its own right: without it the dominant shape of a
		// real command line carried no path the scanner could see.
		{"cd then read", "cd " + out + " && ls", []string{out}},
		{"cd then search", "cd " + out + "/src && rg -n x .", []string{out + "/src"}},
		{"find path", "find " + out + " -name '*.go'", []string{out}},
		{"command substitution", "cat $(echo " + home + "/notes.md)", []string{home + "/notes.md"}},
		{"glob", "cat " + home + "/*", []string{home + "/*"}},

		// $HOME is the one variable whose value is knowable here.
		{"HOME var", "cat $HOME/.zshrc", []string{filepath.Join(homeOf(t), ".zshrc")}},
		{"braced HOME var", "cat ${HOME}/.zshrc", []string{filepath.Join(homeOf(t), ".zshrc")}},
		{"unknown var stays unknown", "cat $LOG_DIR/app.log", nil},

		// The shell expands a leading unquoted ~ itself. Read literally, such
		// an operand resolved against the workspace — so a read of a file under
		// $HOME was silently treated as an in-workspace read.
		{"tilde operand", "ls ~/work", []string{filepath.Join(homeOf(t), "work")}},
		{"bare tilde", "ls ~", []string{homeOf(t)}},
		{"tilde then quoted tail", `ls ~/"work"`, []string{filepath.Join(homeOf(t), "work")}},
		{"quoted tilde stays literal", `cat "~/notes.md"`, []string{"~/notes.md"}},
		{"single-quoted tilde stays literal", `cat '~/notes.md'`, []string{"~/notes.md"}},
		{"another user's tilde is not ours", "cat ~other/notes.md", nil},

		// Interpreters: an inline program's embedded path is a real read.
		{"python program", `python3 -c 'open("` + out + `/src/a.go")'`, []string{out + "/src/a.go"}},
		{"node program", `node -e 'require("` + out + `/src/a.go")'`, []string{out + "/src/a.go"}},
		{"shell program", "bash -c 'cat " + out + "/src/a.go'", []string{out + "/src/a.go"}},
		{"shell flag cluster", `sh -lc "cat ` + home + `/notes.md"`, []string{home + "/notes.md"}},
		// A shell's inline program is a shell command line, so its ~ expands
		// exactly as the outer one would; a python program's does not.
		{"shell program tilde", `bash -c 'ls ~/work'`, []string{filepath.Join(homeOf(t), "work")}},
		// Python does not expand a tilde, so this opens a literal `~/x` — which
		// does not exist. It is not a read, and the existence test says so.
		{"python program tilde", `python3 -c 'open("~/x")'`, nil},

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
			got := commandPaths(c.cmd)
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
	// GATE decides it is inside, not this layer. Asserting that keeps the two
	// responsibilities apart.
	if got := commandPaths("cat " + ws + "/build.sh"); len(got) != 1 || got[0] != ws+"/build.sh" {
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
		if got := commandPaths(cmd); len(got) > 0 {
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

// TestPathShapedRuns covers the run extractor: absolute-path-shaped runs come
// out, relative fragments and prose do not. It is deliberately lexical — the
// answer about which runs are PATHS belongs to programPaths, tested below.
func TestPathShapedRuns(t *testing.T) {
	for _, c := range []struct {
		prog string
		want []string
	}{
		{`open("/outside/x")`, []string{"/outside/x"}},
		{`x = "/a/b"; y = open("/c")`, []string{"/a/b", "/c"}},
		{`print(1+1)`, nil},
		{`print("/2024/report.csv")`, []string{"/2024/report.csv"}},
		{`relative/path`, nil},
		{`re.match("/api/v1", u)`, []string{"/api/v1"}},
		// Python's glob is a string expression; splitting a run at '*' turns
		// "/*.jsonl" into "/", which always exists and invites the boundary gate
		// to offer the entire filesystem as a read root.
		{`glob.glob(p+"/*.jsonl")`, []string{"/*.jsonl"}},
		{`glob.glob("/outside/*.jsonl")`, []string{"/outside/*.jsonl"}},
		{`glob.glob("/outside/file?.jsonl")`, []string{"/outside/file?.jsonl"}},
		{`glob.glob("/[abc].jsonl")`, []string{"/[abc].jsonl"}},
		// Outside a quote the same characters are arithmetic, so no run is taken;
		// the existence test in programPaths then has less to reject.
		{`(n*med)/60`, nil},
	} {
		got := pathShapedRuns(c.prog)
		if len(got) != len(c.want) {
			t.Fatalf("pathShapedRuns(%q) = %v, want %v", c.prog, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("pathShapedRuns(%q)[%d] = %q, want %q", c.prog, i, got[i], c.want[i])
			}
		}
	}
}

// TestProgramPathsIsAVerifiablePredicate pins what replaced the lexical guess.
//
// A run extracted from a non-shell program counts as a path only if it exists,
// because this gate asks a read question and a path that does not exist cannot
// be read. The shape that motivated it is arithmetic after a closing paren —
// `(n*med)/60` runs to `/60` — and the same predicate disposes of every other
// shape producing a run that names nothing: `/`, `//`, `/api/v1` in a regex, a
// URL path inside a string.
func TestProgramPathsIsAVerifiablePredicate(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "data.csv")
	if err := os.WriteFile(real, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name        string
		interpreter string
		prog        string
		want        []string
	}{
		{
			name:        "arithmetic after a paren is not a path",
			interpreter: "python3",
			prog:        `print('耗时: %.0f' % ((n*2.16 - n*med)/60))`,
			want:        nil,
		},
		{name: "division with spaces", interpreter: "python3", prog: `print(total / 2)`, want: nil},
		{name: "floor division", interpreter: "python3", prog: `print(a // b)`, want: nil},
		{
			name:        "glob fragment is not the filesystem root",
			interpreter: "python3",
			prog:        `p=os.path.expanduser("~/.golder/sessions"); files=glob.glob(p+"/*.jsonl")`,
			want:        nil,
		},
		{
			name:        "a regex that looks like a route is not a path",
			interpreter: "python3",
			prog:        `re.match("/api/v1", u)`,
			want:        nil,
		},
		{
			name:        "an existing file opened by a program IS a path",
			interpreter: "python3",
			prog:        `print(open("` + real + `").read())`,
			want:        []string{real},
		},
		{
			// A path the program would CREATE does not exist yet, so this gate —
			// which asks about reads — does not report it. Writes are the
			// sandbox's and the escalation dialog's business.
			name:        "a path that does not exist yet is not a read",
			interpreter: "python3",
			prog:        `open("` + dir + `/not-created-yet.txt", "w")`,
			want:        nil,
		},
		{
			name:        "a tilde is not expanded in a non-shell language",
			interpreter: "python3",
			prog:        `open("~/notes.md")`,
			want:        nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := programPaths(c.interpreter, c.prog, 0)
			if len(got) != len(c.want) {
				t.Fatalf("programPaths(%q) = %v, want %v", c.prog, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("programPaths(%q)[%d] = %q, want %q", c.prog, i, got[i], c.want[i])
				}
			}
		})
	}
}

// TestShellProgramIsParsedNotScanned pins the other half: `sh -c '…'` is a
// command line, so it goes through the parser and the answer is exact. A
// one-liner that reads outside the workspace is seen, one whose text merely
// contains a slash is not, and the recursion is bounded.
func TestShellProgramIsParsedNotScanned(t *testing.T) {
	_, out, _ := sandboxPaths(t)
	target := out + "/src/a.go"

	for _, c := range []struct {
		name string
		prog string
		want []string
	}{
		{"a read is found", "cat " + target, []string{target}},
		{"a command substitution is descended into", "echo $(cat " + target + ")", []string{target}},
		{"no slash, no path", "echo a / b", nil},
		{"a relative path stays quiet", "cat internal/x.go", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := programPaths("bash", c.prog, 0)
			if len(got) != len(c.want) {
				t.Fatalf("programPaths(bash, %q) = %v, want %v", c.prog, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("programPaths(bash, %q)[%d] = %q, want %q", c.prog, i, got[i], c.want[i])
				}
			}
		})
	}

	if got := programPaths("bash", "cat "+target, maxProgramDepth); got != nil {
		t.Errorf("at the depth limit the recursion must stop, got %v", got)
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
	if got := commandPaths("cat /outside/*.log"); len(got) != 1 || !strings.HasSuffix(got[0], "*.log") {
		t.Errorf("glob candidate = %v, want the wildcard preserved", got)
	}
}
