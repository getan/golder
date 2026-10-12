package judge

// The operand-coverage invariant: every program the read-only fast path can
// allow must have its operands DECLARED — either as paths the read-scope
// scanner collects (fileOperandCommands) or as deliberately not paths
// (readOnlyProgramsWithoutPathOperands), and its file-reading flags declared in
// fileArgFlags.
//
// This is the one test that makes the fast path trustworthy as a whole. Its
// safety argument is "a command the fast path allows reads only what the
// consent gate has already looked at", and that argument is only as good as the
// two tables being in step — they live in different files, and nothing else
// connects them. The failure this prevents is concrete: `cksum /outside/x` was
// whitelisted while `cksum` was absent from the operand table, so the read was
// neither asked about nor reviewed. A test is the only thing that turns that
// class of mistake into a red build instead of a silent hole.

import (
	"sort"
	"strings"
	"testing"
)

// TestOperandCoverageIsDeclaredForEveryWhitelistedProgram is the invariant.
func TestOperandCoverageIsDeclaredForEveryWhitelistedProgram(t *testing.T) {
	var undeclared []string
	for program := range readOnlyPrograms {
		if fileOperandCommands[program] {
			continue // declared: its operands are scanned as paths
		}
		if _, ok := readOnlyProgramsWithoutPathOperands[program]; ok {
			continue // declared: its operands are deliberately not paths
		}
		undeclared = append(undeclared, program)
	}
	sort.Strings(undeclared)
	if len(undeclared) > 0 {
		t.Errorf("whitelisted programs with no operand declaration: %v\n"+
			"Each one must be listed either in fileOperandCommands (its bare operands are paths the "+
			"consent gate should see) or in readOnlyProgramsWithoutPathOperands (they are not paths, "+
			"with a reason). A program in neither is a read the gate cannot see while the fast path "+
			"skips the review — the shape this test exists to catch.", undeclared)
	}
}

// TestOperandDeclarationsDoNotContradict pins the two directions that would make
// the declaration meaningless: a program claimed in both tables (the scan would
// be dead code, and the "no path operands" reason a lie), and a declaration for
// a program that is not whitelisted at all (a leftover after the whitelist
// shrank, which would silently keep covering nothing).
func TestOperandDeclarationsDoNotContradict(t *testing.T) {
	for program := range readOnlyProgramsWithoutPathOperands {
		if _, ok := readOnlyPrograms[program]; !ok {
			t.Errorf("%q is declared as having no path operands but is not whitelisted", program)
		}
		if fileOperandCommands[program] {
			t.Errorf("%q is declared in BOTH tables: the scanner would collect its operands "+
				"while the declaration says there are none", program)
		}
	}
	// Every reason must say something: an empty reason is a declaration nobody
	// can review.
	for program, reason := range readOnlyProgramsWithoutPathOperands {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("%q is declared with an empty reason", program)
		}
	}
}

// TestFileFlagDeclarationsAreReachable checks the flag half of the same idea:
// a file-reading flag is only covered if the scanner can actually extract it,
// in both spellings a shell allows. A declaration whose flag the scanner cannot
// parse would be documentation of a hole rather than a fix.
func TestFileFlagDeclarationsAreReachable(t *testing.T) {
	cases := []struct {
		cmd  string
		want string
	}{
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
	}
	for _, c := range cases {
		got := bashPathCandidates(c.cmd)
		found := false
		for _, g := range got {
			if g == c.want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("bashPathCandidates(%q) = %v, want it to include %q (the flag's file operand)",
				c.cmd, got, c.want)
		}
	}
	// And the flags that take a pattern still skip it rather than reporting the
	// pattern as a path.
	if got := bashPathCandidates(`rg -n TODO /outside/dir`); len(got) != 1 || got[0] != "/outside/dir" {
		t.Errorf(`rg -n TODO /outside/dir candidates = %v, want only the directory`, got)
	}
	// With patterns coming from a file, there is no pattern operand, so the
	// FIRST non-flag word is a file too.
	got := bashPathCandidates(`grep -f /outside/pat /outside/data`)
	if len(got) != 2 || got[0] != "/outside/pat" || got[1] != "/outside/data" {
		t.Errorf(`grep -f PAT FILE candidates = %v, want both the pattern file and the data file`, got)
	}
}

// TestEveryOperandTableEntryIsWhitelistedOrKnown is the reverse direction for
// the operand table: an entry for a program the fast path can never allow is
// harmless but misleading, and (more importantly) an entry that exists only
// because the whitelist shrank would hide the fact that nothing needs scanning.
// The table intentionally covers programs the whitelist does NOT have (the
// gate asks about `cp`/`tar`/interpreters too, which is the consent half),
// so this test pins the allowed set rather than demanding whitelist membership.
func TestEveryOperandTableEntryIsWhitelistedOrKnown(t *testing.T) {
	// Programs whose operands the gate scans although the fast path never allows
	// them: the scanner serves the consent gate for every command, not only for
	// the ones the whitelist knows.
	known := map[string]bool{
		"less": true, "more": true, "bat": true, "xxd": true, "ag": true,
		"ack": true, "awk": true, "gawk": true, "cp": true, "mv": true,
		"install": true, "rsync": true, "scp": true, "sftp": true, "tar": true,
		"zip": true, "unzip": true, "gzip": true, "gunzip": true, "python": true,
		"python3": true, "node": true, "nodejs": true, "perl": true, "ruby": true,
		"php": true, "bash": true, "sh": true, "zsh": true, "dash": true,
		"source": true,
	}
	for program := range fileOperandCommands {
		_, whitelisted := readOnlyPrograms[program]
		if whitelisted || known[program] {
			continue
		}
		t.Errorf("fileOperandCommands has %q, which is neither whitelisted nor in the "+
			"scanner-only set; add the reason it is scanned (or drop the entry)", program)
	}
	for program := range known {
		if !fileOperandCommands[program] {
			t.Errorf("%q is in the scanner-only set but no longer in fileOperandCommands", program)
		}
	}
}
