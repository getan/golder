package judge

import "testing"

// The consent gate needs program-specific file operands even though EVERY
// approved bash command now runs in the OS sandbox. Test the second-file flags
// explicitly: a static reader that drops these cannot ask for the user's
// intended read before the sandbox blocks it.
func TestFileFlagDeclarationsAreReachable(t *testing.T) {
	for _, c := range []struct{ cmd, want string }{
		{`grep -f /outside/pat /inside/data`, "/outside/pat"},
		{`grep --file /outside/pat /inside/data`, "/outside/pat"},
		{`grep --file=/outside/pat /inside/data`, "/outside/pat"},
		{`grep -f/outside/pat /inside/data`, "/outside/pat"},
		{`date -r /outside/file`, "/outside/file"},
		{`date --reference=/outside/file`, "/outside/file"},
		{`sort --files0-from=/outside/list`, "/outside/list"},
		{`wc --files0-from /outside/list`, "/outside/list"},
		{`diff --from-file=/outside/a /inside/b`, "/outside/a"},
		{`file -m /outside/magic /inside/x`, "/outside/magic"},
	} {
		got := commandPaths(c.cmd)
		found := false
		for _, p := range got {
			found = found || p == c.want
		}
		if !found {
			t.Errorf("commandPaths(%q) = %v, want file operand %q", c.cmd, got, c.want)
		}
	}
	if got := commandPaths(`rg -n TODO /outside/dir`); len(got) != 1 || got[0] != "/outside/dir" {
		t.Errorf("rg pattern is not a file operand: %v", got)
	}
	if got := commandPaths(`grep -f /outside/pat /outside/data`); len(got) != 2 || got[0] != "/outside/pat" || got[1] != "/outside/data" {
		t.Errorf("grep -f must report pattern file AND data file: %v", got)
	}
}
