package judge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/shellfacts"
)

// The two commands from the report, verbatim: the clone that was approved and
// the clone that was blocked had the same shape, and the only difference the
// gate could see was which one the reviewer happened to grant Direct.
const (
	cloneAgentS      = `git -c http.proxy=http://127.0.0.1:7897 clone https://github.com/simular-ai/Agent-S.git ../Agent-S`
	cloneBrowserUse  = `git -c http.proxy=http://127.0.0.1:7897 clone https://github.com/browser-use/browser-use.git ../browser-use`
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
