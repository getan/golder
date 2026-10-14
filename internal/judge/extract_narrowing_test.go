package judge

// A path-SHAPED word is not always a path. Two readers see the words of a
// command — the hand-rolled scanner (bashpaths.go) and the facts reader
// (shellfacts, which parses with mvdan and deliberately reports every
// path-shaped ARGUMENT with an unknown operation, because whether one is a file
// is the program's business) — and commandPaths unions them. The union made the
// un-narrowed side win: `sed -n '/func X/,/^}/p' f.go` asked the user to consent
// to a read of `/func X/,/^}/p`, which is a sed address range, and `grep -rn
// '/usr/local' internal` asked about `/usr/local`, which is a search string.
//
// The rule that settles it (a search command's pattern, sed's and awk's script,
// a value flag's argument) lives in nonPathOperandIndices and is applied by both
// readers. These tests hold the union to it, in both directions: the scripts must
// go, and every genuine read must stay.

import (
	"slices"
	"strings"
	"testing"
)

func TestCommandPathsNarrowsPatternsAndScripts(t *testing.T) {
	_, out, home := sandboxPaths(t)
	target := out + "/src/a.go"

	cases := []struct {
		name    string
		cmd     string
		want    []string // must appear
		notWant []string // must not appear
	}{
		{
			// The shape that produced the dialog this rule exists for.
			name:    "sed address range is a script, not a path",
			cmd:     `sed -n '/func X/,/^}/p' ` + target,
			want:    []string{target},
			notWant: []string{"/func X/,/^}/p"},
		},
		{
			name:    "several inline scripts",
			cmd:     `sed -e '/a/p' -e '/b/p' ` + target,
			want:    []string{target},
			notWant: []string{"/a/p", "/b/p"},
		},
		{
			name:    "search pattern that looks absolute",
			cmd:     `grep -rn '/usr/local' ` + out,
			want:    []string{out},
			notWant: []string{"/usr/local"},
		},
		{
			name:    "regexp flag with a path-shaped pattern",
			cmd:     `rg -e '/etc/hosts' ` + out,
			want:    []string{out},
			notWant: []string{"/etc/hosts"},
		},
		{
			name:    "awk separator value",
			cmd:     `awk -F: '/x/{print}' ` + target,
			want:    []string{target},
			notWant: []string{"/x/{print}"},
		},
		{
			name:    "awk -v assignment",
			cmd:     `awk -v n=1 '/x/{print}' ` + target,
			want:    []string{target},
			notWant: []string{"/x/{print}"},
		},
		{
			// sed's -f names a script FILE, which is a read: the narrowing must
			// not swallow it, and declaring -f is what tells the rule that the
			// operands here hold no positional script.
			name: "sed -f script file is still a read",
			cmd:  `sed -f ` + out + `/prog.sed ` + target,
			want: []string{out + "/prog.sed", target},
		},
		{
			name: "ordinary outside read",
			cmd:  "cat " + target,
			want: []string{target},
		},
		{
			name: "outside read next to a script",
			cmd:  `awk '{print}' ` + target,
			want: []string{target},
		},
		{
			name: "credential material behind a pattern stays visible",
			cmd:  `grep -rn '/usr/local' ` + home + "/.config/ghostty",
			want: []string{home + "/.config/ghostty"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := commandPaths(c.cmd)
			for _, want := range c.want {
				if !slices.Contains(got, want) {
					t.Errorf("paths = %v, want %q among them\ncommand: %s", got, want, c.cmd)
				}
			}
			for _, bad := range c.notWant {
				if slices.Contains(got, bad) {
					t.Errorf("paths = %v, must not contain %q (not a path)\ncommand: %s", got, bad, c.cmd)
				}
			}
		})
	}
}

// TestNonPathOperandTextsAgreesWithTheScanner pins the two readers to one rule:
// whatever the scanner skips by position is what commandPaths strips by text, so
// neither can learn a shape the other does not know.
func TestNonPathOperandTextsAgreesWithTheScanner(t *testing.T) {
	_, out, _ := sandboxPaths(t)
	cmds := []string{
		`sed -n '/x/,/^}/p' ` + out + "/f.go",
		`sed -e '/a/p' -e '/b/p' ` + out + "/f.go",
		`grep -rn '/usr/local' ` + out,
		`awk -F: '{print}' ` + out + "/f.go",
		`cat ` + out + "/f.go",
	}
	for _, cmd := range cmds {
		skipped := nonPathOperandTexts(cmd)
		for text := range skipped {
			if slices.Contains(bashPathCandidates(cmd), text) {
				t.Errorf("both readers disagree: %q is skipped as a script yet reported as a path\ncommand: %s", text, cmd)
			}
		}
		// And the reverse reading, for the shapes this rule is about: a
		// path-shaped pattern must be in the skipped set.
		for _, cand := range bashPathCandidates(cmd) {
			if strings.Contains(cand, "usr/local") && !skipped[cand] {
				t.Errorf("scanner reports %q but the narrowing does not list it\ncommand: %s", cand, cmd)
			}
		}
	}
}
