package shellfacts

// Tests for the facts layer. The first block is the reason the package exists:
// each case is one of the documented shell-quoting bypasses that defeats a
// pattern matcher over the raw string, and the test asserts that the parsed
// facts expose it. These are facts, not verdicts — the assertions say what the
// command IS, never whether it is safe.

import (
	"strings"
	"testing"
)

// Program returns the first command's program name, failing the test when there
// is no command at all.
func program(t *testing.T, p *Plan) string {
	t.Helper()
	if len(p.Commands) == 0 {
		t.Fatalf("no commands in %q", p.Raw)
	}
	name, literal := p.Commands[0].Program()
	if !literal {
		return "<dynamic>"
	}
	return name
}

// TestBypassFactsAreVisible pins the class-A/B/C/D/E bypasses from the GuardFall
// research as facts the parser exposes. A string matcher sees the raw text and
// misses all of them; these assertions are what a caller gets instead.
func TestBypassFactsAreVisible(t *testing.T) {
	t.Run("class A: quote removal merges tokens", func(t *testing.T) {
		// `r''m` is two tokens to a regex and one word to bash: rm.
		p := Parse(`r''m -rf /`)
		if !p.Parsed {
			t.Fatalf("parse failed: %s", p.ParseError)
		}
		if got := program(t, p); got != "rm" {
			t.Errorf("program = %q, want \"rm\" (quotes removed)", got)
		}
		if p.Commands[0].Argv[0].Dynamic {
			t.Error("a quoted literal word was reported as dynamic")
		}
	})

	t.Run("class A: backslash escape", func(t *testing.T) {
		p := Parse(`r\m -rf /`)
		if got := program(t, p); got != "rm" {
			t.Errorf("program = %q, want \"rm\"", got)
		}
	})

	t.Run("class B: $IFS is a variable part, not whitespace", func(t *testing.T) {
		// The fact that matters: the word contains a variable, so its argv value
		// is decided at runtime. This package reports the NAME, never the value,
		// because resolving $IFS would mean running the expansion.
		p := Parse(`rm$IFS-rf$IFS/`)
		if !p.Parsed {
			t.Fatalf("parse failed: %s", p.ParseError)
		}
		w := p.Commands[0].Argv[0]
		if !w.Dynamic {
			t.Fatal("a word with $IFS was reported as static")
		}
		var names []string
		for _, part := range w.Parts {
			if part.Kind == VariablePart {
				names = append(names, part.Text)
			}
		}
		if len(names) != 2 || names[0] != "IFS" || names[1] != "IFS" {
			t.Errorf("variable names = %v, want [IFS IFS]", names)
		}
	})

	t.Run("class C: command substitution in command position", func(t *testing.T) {
		// `$(echo rm) -rf /` has no literal program at all: the program name is
		// computed. A matcher looking for `rm` finds it in the wrong place.
		p := Parse(`$(echo rm) -rf /`)
		// One top-level command (its command word is the substitution); the
		// `echo` is reachable only through AllCommands, which is why deciding
		// callers must use that enumeration rather than Commands.
		if len(p.Commands) != 1 {
			t.Fatalf("top-level commands = %d, want 1", len(p.Commands))
		}
		if name, literal := p.Commands[0].Program(); literal {
			t.Errorf("command word reported as literal %q, want dynamic", name)
		}
		all := p.AllCommands()
		if len(all) != 2 {
			t.Fatalf("AllCommands = %d, want 2 (the substitution and the outer command)", len(all))
		}
		// A substitution runs first, so its command comes first.
		if got := program(t, &Plan{Commands: []Command{all[0]}}); got != "echo" {
			t.Errorf("AllCommands[0] = %q, want \"echo\" (substitutions run first)", got)
		}
		if got := all[1].Argv[0].Text; got != "" {
			t.Errorf("AllCommands[1] command word = %q, want the dynamic substitution word", got)
		}
	})

	t.Run("class C: substitution inside a quoted argument", func(t *testing.T) {
		// The variant that defeats the strongest tokenizers: the outer command
		// is a harmless `echo`, and the dangerous command is inside a word.
		p := Parse(`echo "$(rm -rf /x)"`)
		if len(p.Commands) == 0 {
			t.Fatal("no commands")
		}
		outer := p.Commands[0]
		if outer.Argv[0].Text != "echo" {
			t.Fatalf("outer program = %q, want \"echo\"", outer.Argv[0].Text)
		}
		if len(outer.Nested) == 0 {
			t.Fatal("the substitution inside the quoted argument was not reported")
		}
		// The nested plan is where `rm -rf /x` lives; a listing that stops at the
		// outer argv never sees it.
		if got := program(t, outer.Nested[0]); got != "rm" {
			t.Errorf("nested program = %q, want \"rm\"", got)
		}
	})

	t.Run("class D: pipeline into an interpreter is visible as stage order", func(t *testing.T) {
		p := Parse(`echo cm0 | base64 -d | sh`)
		if len(p.Commands) != 3 {
			t.Fatalf("got %d commands, want 3 pipeline stages", len(p.Commands))
		}
		for i, want := range []string{"echo", "base64", "sh"} {
			if got := program(t, &Plan{Commands: []Command{p.Commands[i]}}); got != want {
				t.Errorf("stage %d = %q, want %q", i, got, want)
			}
		}
		// Every stage but the last feeds another: the fact that the content
		// flowing into `sh` is computed, not written.
		for i := 0; i < len(p.Commands)-1; i++ {
			if !p.Commands[i].InPipeline {
				t.Errorf("stage %d was not marked as feeding a pipeline", i)
			}
		}
	})

	t.Run("class E: destructive flags stay in argv for the caller to judge", func(t *testing.T) {
		// This layer cannot know that -delete is destructive; what it CAN do is
		// hand over the argv faithfully so a caller with program knowledge can.
		p := Parse(`find /x -delete`)
		var args []string
		for _, w := range p.Commands[0].Args() {
			args = append(args, w.Text)
		}
		if strings.Join(args, " ") != "/x -delete" {
			t.Errorf("args = %v, want [/x -delete]", args)
		}
	})
}

// ---- Structure -----------------------------------------------------------

func TestAssignmentIsSeparateFromArgv(t *testing.T) {
	// An assignment in command position is how a program runs with a changed
	// environment (`LD_PRELOAD=… ls`), which is a different fact from an
	// argument, so it is reported separately rather than mixed into argv.
	p := Parse(`LD_PRELOAD=/evil.so ls -la`)
	if !p.Parsed {
		t.Fatalf("parse failed: %s", p.ParseError)
	}
	c := p.Commands[0]
	if len(c.Assigns) != 1 || c.Assigns[0].Text != "LD_PRELOAD" {
		t.Fatalf("assigns = %+v, want one named LD_PRELOAD", c.Assigns)
	}
	if len(c.Argv) == 0 || c.Argv[0].Text != "ls" {
		t.Fatalf("argv[0] = %+v, want ls", c.Argv)
	}
}

func TestRedirectsAreReportedWithTheirOperation(t *testing.T) {
	cases := []struct {
		cmd    string
		op     string
		opWant Op
		fd     int
		target string
	}{
		{"ls > out.txt", ">", Write, 1, "out.txt"},
		{"ls >> out.txt", ">>", Write, 1, "out.txt"},
		{"ls 2> err.txt", ">", Write, 2, "err.txt"},
		{"cat < in.txt", "<", Read, 0, "in.txt"},
		{"> out.txt ls", ">", Write, 1, "out.txt"},
	}
	for _, c := range cases {
		p := Parse(c.cmd)
		if !p.Parsed {
			t.Errorf("%q: parse failed: %s", c.cmd, p.ParseError)
			continue
		}
		if len(p.Commands) == 0 || len(p.Commands[0].Redirs) == 0 {
			t.Errorf("%q: no redirect reported", c.cmd)
			continue
		}
		r := p.Commands[0].Redirs[0]
		if r.Op != c.op || r.Fd != c.fd || r.Target.Text != c.target {
			t.Errorf("%q: redirect = %+v, want op=%s fd=%d target=%s", c.cmd, r, c.op, c.fd, c.target)
		}
	}
}

func TestDescriptorDuplicationNamesNoPath(t *testing.T) {
	p := Parse(`ls 2>&1 | head -3`)
	r := p.Commands[0].Redirs[0]
	if !r.Dup {
		t.Errorf("2>&1 was not reported as a descriptor duplication: %+v", r)
	}
	// A duplication must not appear in the access list: it names no path, and
	// asking the user about "1" would be nonsense.
	for _, a := range p.PathAccesses() {
		if a.Origin == "redirect" {
			t.Errorf("a descriptor duplication produced an access: %+v", a)
		}
	}
}

func TestHeredocIsReportedAsFurtherShellInput(t *testing.T) {
	p := Parse("cat <<EOF\nrm -rf /\nEOF\n")
	r := p.Commands[0].Redirs[0]
	if !r.Heredoc {
		t.Errorf("<<EOF was not reported as a heredoc: %+v", r)
	}
}

func TestConstructsAreNamed(t *testing.T) {
	cases := map[string]string{
		`(ls)`:                  "subshell",
		`{ ls; }`:               "block",
		`if true; then ls; fi`:  "if",
		`for i in a b; do ls; done`: "for",
		`while true; do ls; done`:   "while",
		`f() { ls; }`:               "function f",
	}
	for cmd, want := range cases {
		p := Parse(cmd)
		found := false
		for _, c := range p.Constructs {
			if strings.Contains(c, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%q: constructs = %v, want one containing %q", cmd, p.Constructs, want)
		}
	}
}

func TestBackgroundAndNegation(t *testing.T) {
	p := Parse(`ls &`)
	if !p.Commands[0].Background {
		t.Error("a trailing & was not reported as background")
	}
	p = Parse(`! ls`)
	if !p.Commands[0].Negated {
		t.Error("a leading ! was not reported as negation")
	}
}

// ---- Parse failure is a fact --------------------------------------------

func TestParseFailureIsReportedNotHidden(t *testing.T) {
	// An unterminated quote means the shell is still reading: the command is not
	// complete, so nothing about it may be treated as settled.
	for _, cmd := range []string{`ls "unterminated`, `ls | sed -n '1p`} {
		p := Parse(cmd)
		if p.Parsed {
			t.Errorf("%q parsed as complete, want a parse failure", cmd)
		}
		if p.ParseError == "" {
			t.Errorf("%q: Parsed=false but no error text", cmd)
		}
		if len(p.Commands) != 0 {
			t.Errorf("%q: commands reported for an unparsable line", cmd)
		}
	}
}

func TestRawIsPreserved(t *testing.T) {
	// The parser can be wrong; keeping the original text is what lets a caller
	// (and the model reviewer) compare the two.
	const cmd = `cd /a && r''m -rf /b > /c 2>&1`
	p := Parse(cmd)
	if p.Raw != cmd {
		t.Errorf("Raw = %q, want the input unchanged", p.Raw)
	}
}

// ---- Path accesses -------------------------------------------------------

func TestPathAccessesCoversRedirectsAndArguments(t *testing.T) {
	p := Parse(`cat /etc/hosts > /tmp/out`)
	got := map[string]Op{}
	for _, a := range p.PathAccesses() {
		got[a.Path] = a.Op
	}
	if got["/etc/hosts"] != UnknownOp {
		t.Errorf("/etc/hosts op = %v, want unknown (an argument's operation is the program's business)", got["/etc/hosts"])
	}
	// The redirect target is the access a listing of arguments alone would miss.
	if got["/tmp/out"] != Write {
		t.Errorf("/tmp/out op = %v, want write", got["/tmp/out"])
	}
}

func TestPathAccessesFindsNestedCommands(t *testing.T) {
	p := Parse(`echo "$(cat /etc/passwd)"`)
	var nested []Access
	for _, a := range p.PathAccesses() {
		if a.Nested {
			nested = append(nested, a)
		}
	}
	if len(nested) == 0 {
		t.Fatal("the path inside the substitution was not reported")
	}
	if nested[0].Path != "/etc/passwd" {
		t.Errorf("nested path = %q, want /etc/passwd", nested[0].Path)
	}
}

func TestPathAccessesMarksDynamicPaths(t *testing.T) {
	p := Parse(`ls "$HOME"/x`)
	var found bool
	for _, a := range p.PathAccesses() {
		if strings.HasSuffix(a.Path, "/x") {
			found = true
			if !a.Dynamic {
				t.Error("a path built from $HOME was not marked dynamic")
			}
		}
	}
	if !found {
		t.Errorf("no access reported for %q: %+v", p.Raw, p.PathAccesses())
	}
}

func TestPathShapedIsConservative(t *testing.T) {
	// The line this draws matters: a false positive asks the user about a file
	// they never touched, which is how a dialog gets dismissed unread.
	args := []struct {
		cmd  string
		want bool
	}{
		{`grep -n TODO internal/judge/readonly.go`, true},
		{`ls /tmp`, true},
		{`ls ~/work`, true},
		{`ls ../sibling`, true},
		{`git log --oneline -3`, false},   // flags
		{`rg -n "explore" codex-rs`, false}, // a bare word may be a pattern
		{`curl https://example.com`, false}, // a URL is not a local path
		{`git commit -m "fix: typo"`, false},
	}
	for _, c := range args {
		p := Parse(c.cmd)
		var paths []string
		for _, a := range p.PathAccesses() {
			paths = append(paths, a.Path)
		}
		if got := len(paths) > 0; got != c.want {
			t.Errorf("%q: reported paths %v (want any=%v)", c.cmd, paths, c.want)
		}
	}
}

func TestSummaryFoldsOnePathToOneRow(t *testing.T) {
	// `cat a > a` touches one path twice with different operations; a dialog
	// wants one row saying "read and write", not two identical rows.
	p := Parse(`cat /tmp/x > /tmp/x`)
	sum := p.Summary()
	if len(sum) != 1 {
		t.Fatalf("summary = %+v, want one row for one path", sum)
	}
	// The redirect contributes a write and the argument an unknown operation;
	// the row must carry both, not whichever arrived first.
	if sum[0].Op&Write == 0 {
		t.Errorf("summary op = %v, want it to include write", sum[0].Op)
	}
	if sum[0].Op&UnknownOp == 0 {
		t.Errorf("summary op = %v, want it to include the argument's operation", sum[0].Op)
	}
}

func TestMultiPathCommandKeepsEveryPath(t *testing.T) {
	// The design question this pins: a command naming several paths must report
	// all of them. Collapsing to a common parent would over-grant; keeping only
	// the first would under-report.
	p := Parse(`cp /data/a /data/b /dest/`)
	paths := map[string]bool{}
	for _, a := range p.PathAccesses() {
		paths[a.Path] = true
	}
	for _, want := range []string{"/data/a", "/data/b", "/dest/"} {
		if !paths[want] {
			t.Errorf("path %s was not reported (got %v)", want, paths)
		}
	}
}

func TestExecutionOrderIsPreserved(t *testing.T) {
	p := Parse(`cd /repo && rm -rf build && go build ./...`)
	if len(p.Commands) != 3 {
		t.Fatalf("got %d commands, want 3", len(p.Commands))
	}
	want := []string{"cd", "rm", "go"}
	for i, w := range want {
		if got := p.Commands[i].Argv[0].Text; got != w {
			t.Errorf("command %d = %q, want %q", i, got, w)
		}
		if p.Commands[i].Index != i {
			t.Errorf("command %d has Index %d", i, p.Commands[i].Index)
		}
	}
}
