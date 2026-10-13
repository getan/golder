package tui

// The tuios manifest matches golder's prompts on their own words: a rule
// recognises "Approve once", and the answers press the keys those rows print.
// That makes a reworded button a silent failure — the pane keeps reporting its
// state, but the Inbox stops being able to answer the prompt, and nothing
// anywhere says so.
//
// This test is the tripwire. It renders every dialog the manifest claims to
// read and fails when a string the manifest matches on is no longer on the
// screen, so rewording a row is fine only if the manifest is reworded with it
// (and ManifestVersion bumped, so an installed copy is replaced).
//
// It deliberately does not import tuios: the manifest is golder's file, and the
// contract it has to satisfy is golder's own rendering. That the rules are
// *accepted* by a real daemon is checked separately, by hand, with
// `tuios doctor agents` and `tuios explain-agent-screen`.

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/getan/golder/internal/hostcompat"
	"github.com/getan/golder/internal/judge"
)

// manifestRules is the part of the shipped manifest this test reads: the
// predicates that have to keep matching, and the answers that have to keep
// existing.
type manifestRules struct {
	Screen struct {
		Rule []struct {
			State   string         `toml:"state"`
			All     []string       `toml:"all"`
			Any     []string       `toml:"any"`
			Regex   []string       `toml:"regex"`
			Answers map[string]any `toml:"answers"`
		} `toml:"rule"`
	} `toml:"screen"`
}

// dialogFrames renders every screen the manifest makes a claim about. Each
// frame is what the pane would show, built through the same functions the TUI
// uses, so a reworded row changes this input the same way it changes the pane.
func dialogFrames(t *testing.T) map[string]string {
	t.Helper()
	approval := func(req judge.ApprovalRequest) string {
		m := NewModel(Options{})
		m.openApproval(req, make(chan judge.ApprovalAnswer, 1))
		return stripANSI(m.approvalView(120))
	}

	picker := NewModel(Options{})
	picker.openTrustApproval()

	spinner := newSpinner(DefaultTheme())
	// The weakest shape of the line: no token count and no effort level, which
	// is what the first second of a turn looks like.
	spinner.begin(time.Now(), "")

	return map[string]string{
		"tool approval": approval(judge.ApprovalRequest{
			Kind: judge.ApprovalConfirm, Tool: "bash", Summary: "npm install",
			Rationale: "installs dependencies",
		}),
		"read outside the workspace": approval(judge.ApprovalRequest{
			Kind: judge.ApprovalReadScope, Tool: "read", Summary: "/etc/hosts",
			ReadScope: "/etc",
		}),
		"mid-run trust, command": approval(judge.ApprovalRequest{
			Kind: judge.ApprovalTrust, Tool: "bash", Summary: "rm -rf /tmp/x",
			TrustDir: "/tmp/project",
		}),
		"mid-run trust, read tool": approval(judge.ApprovalRequest{
			Kind: judge.ApprovalTrust, Tool: "read", Summary: "read a.go",
			TrustDir: "/tmp/project",
		}),
		"launch-time trust picker": stripANSI(picker.approvalView(120)),
		"composer at rest":         stripANSI(NewModel(Options{}).input.View()),
		"working spinner":          stripANSI(spinner.view(120)),
	}
}

// TestManifestMarkersAreStillOnScreen is the tripwire: every substring the
// manifest matches on has to appear in one of the frames it reads.
func TestManifestMarkersAreStillOnScreen(t *testing.T) {
	var m manifestRules
	if _, err := toml.Decode(hostcompat.ManifestSource(), &m); err != nil {
		t.Fatalf("the shipped manifest does not parse: %v", err)
	}
	if len(m.Screen.Rule) == 0 {
		t.Fatal("the shipped manifest has no screen rules")
	}

	frames := dialogFrames(t)
	for _, rule := range m.Screen.Rule {
		for _, marker := range append(append([]string{}, rule.All...), rule.Any...) {
			found := false
			for _, frame := range frames {
				if strings.Contains(strings.ToLower(frame), strings.ToLower(marker)) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("the manifest matches on %q, which no dialog shows any more: "+
					"that rule can never match, so tuios cannot answer this prompt.\n"+
					"Reword the manifest with the dialog (and bump ManifestVersion), "+
					"or put the dialog's words back.", marker)
			}
		}
	}
}

// TestManifestRegexesStillMatch pins the two pattern rules the same way. They
// are separated from the substrings above because a pattern is about the shape
// of a rendered line, not a word in it.
func TestManifestRegexesStillMatch(t *testing.T) {
	var m manifestRules
	if _, err := toml.Decode(hostcompat.ManifestSource(), &m); err != nil {
		t.Fatalf("the shipped manifest does not parse: %v", err)
	}

	// Which frame each pattern reads. A pattern that reads a frame nobody
	// renders would be asserted against the wrong input, so the pairing is
	// spelled out rather than guessed from the rule's own text.
	frames := dialogFrames(t)
	cases := []struct {
		state string
		frame string
	}{
		{"idle", "composer at rest"},
		{"working", "working spinner"},
	}
	for _, c := range cases {
		var rules []string
		for _, rule := range m.Screen.Rule {
			if rule.State == c.state {
				rules = append(rules, rule.Regex...)
			}
		}
		if len(rules) == 0 {
			t.Errorf("the manifest has no %s rule with a pattern", c.state)
			continue
		}
		frame := frames[c.frame]
		if frame == "" {
			t.Fatalf("the %s frame rendered empty", c.frame)
		}
		for _, pattern := range rules {
			re, err := regexp.Compile("(?m)" + pattern)
			if err != nil {
				t.Errorf("the manifest's %s pattern %q does not compile: %v", c.state, pattern, err)
				continue
			}
			if !re.MatchString(frame) {
				t.Errorf("the manifest's %s pattern %q no longer matches what the pane draws:\n%s",
					c.state, pattern, frame)
			}
		}
	}
}

// TestEveryNeedsInputRuleCanBeAnswered: a needs_input rule with no answers
// still puts the pane on needs_input, but the Inbox can only offer "go to the
// pane" for it — which is exactly the gap these rules exist to close. A rule
// added without an answers block is a rule that half works, so it is refused
// here rather than discovered by a person who cannot answer.
func TestEveryNeedsInputRuleCanBeAnswered(t *testing.T) {
	var m manifestRules
	if _, err := toml.Decode(hostcompat.ManifestSource(), &m); err != nil {
		t.Fatalf("the shipped manifest does not parse: %v", err)
	}
	for i, rule := range m.Screen.Rule {
		if rule.State != "needs_input" {
			continue
		}
		if len(rule.Answers) == 0 {
			t.Errorf("screen rule %d reads a prompt and declares no answers, "+
				"so the Inbox can see the pane wait but cannot answer it", i)
		}
	}
}
