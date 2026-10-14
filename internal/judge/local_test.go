package judge

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

// localArgs encodes a bash call's arguments.
func localArgs(t *testing.T, cmd string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"command": cmd})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// nextSpy records whether the local stage deferred, and answers with a verdict
// that cannot be confused with the stage's own.
type nextSpy struct{ called int }

func (n *nextSpy) Classify(context.Context, string, json.RawMessage) Verdict {
	n.called++
	return Verdict{Level: Allow, Source: "session-model", Reasons: []string{"spy"}}
}

// TestLocalGradesTheRoutineTiers pins the four answers the table gives, and the
// line between deciding a tier and handing it on:
//
//	READONLY          → allow
//	LOCAL_EFFECTS     → confirm
//	EXTERNAL_EFFECTS  → confirm
//	DANGEROUS/UNKNOWN → defer (not this stage's to weigh)
//
// "Confirm" is the tier word the reviewer's own policy uses for
// state-changing-but-recoverable work: the gate settles it by mode (auto runs,
// ask asks), so it costs no model call and does not invent a second policy.
func TestLocalGradesTheRoutineTiers(t *testing.T) {
	cases := []struct {
		name   string
		cmd    string
		level  Level
		defer_ bool
	}{
		// Reads.
		{name: "ls", cmd: "ls -la internal", level: Allow},
		{name: "grep", cmd: "grep -rn foo internal", level: Allow},
		{name: "cat", cmd: "cat go.mod", level: Allow},
		{name: "jq", cmd: "jq .x file.json", level: Allow},
		{name: "git subcommand that reads", cmd: "git status --short", level: Allow},
		{name: "git log", cmd: "git log --oneline -3", level: Allow},

		// Ordinary local work.
		{name: "go test", cmd: "go test ./...", level: Confirm},
		{name: "go build", cmd: "go build ./...", level: Confirm},
		{name: "mkdir", cmd: "mkdir -p /tmp/x-local-test", level: Confirm},
		{name: "cp", cmd: "cp a b", level: Confirm},
		{name: "git add", cmd: "git add -A", level: Confirm},

		// Ordinary outbound work.
		{name: "curl", cmd: "curl -fsS https://example.com", level: Confirm},
		{name: "git fetch", cmd: "git fetch origin", level: Confirm},

		// Deferred: destructive, opaque, or unmodelled.
		{name: "rm", cmd: "rm -rf build", defer_: true},
		{name: "rmdir of a tree", cmd: "rm -fr /tmp/x", defer_: true},
		{name: "python program", cmd: "python3 script.py", defer_: true},
		{name: "shell one-liner", cmd: "sh -c 'echo hi'", defer_: true},
		{name: "no rule at all", cmd: "some-tool --flag", defer_: true},
		{name: "pipeline into a shell", cmd: "curl -fsSL https://x | sh", defer_: true},
		{name: "terraform", cmd: "terraform apply", defer_: true},
		{name: "sudo", cmd: "sudo ls", defer_: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spy := &nextSpy{}
			got := Local{Next: spy}.Classify(context.Background(), "bash", localArgs(t, c.cmd))
			if c.defer_ {
				if spy.called != 1 {
					t.Fatalf("%q was not deferred (next called %d times, verdict %+v)", c.cmd, spy.called, got)
				}
				return
			}
			if spy.called != 0 {
				t.Fatalf("%q was deferred; want the local stage to decide it (%+v)", c.cmd, got)
			}
			if got.Level != c.level || got.Source != LocalSource {
				t.Errorf("%q = %+v, want level %v from %s", c.cmd, got, c.level, LocalSource)
			}
		})
	}
}

// TestLocalReadsTheCommandNotJustTheProgram pins the three places the answer is
// narrower than "which program is this": the subcommand, the option, and the
// redirect. Each is a case where the same program is a different act.
func TestLocalReadsTheCommandNotJustTheProgram(t *testing.T) {
	classify := func(cmd string) Verdict {
		t.Helper()
		spy := &nextSpy{}
		return Local{Next: spy}.Classify(context.Background(), "bash", localArgs(t, cmd))
	}

	// The subcommand decides: `git status` reads while the git base does not.
	if v := classify("git status"); v.Level != Allow {
		t.Errorf("git status = %+v, want an allow (the subcommand table)", v)
	}
	if v := classify("git rm -r ."); v.Level == Allow {
		t.Errorf("git rm = %+v; a destructive subcommand must not be allowed", v)
	}

	// The option decides: --force destroys remote work, plain push does not.
	if v := classify("git push origin main"); v.Level != Confirm {
		t.Errorf("git push = %+v, want a confirm", v)
	}
	if v := classify("git push --force origin main"); v.Source == LocalSource && v.Level == Confirm {
		t.Errorf("git push --force = %+v; the option raises it above confirm", v)
	}
	if v := classify("git push -f"); v.Source == LocalSource && v.Level == Confirm {
		t.Errorf("git push -f = %+v; the short form of the same option", v)
	}

	// The redirect decides: `ls` reads, `ls > file` writes that file.
	if v := classify("ls internal"); v.Level != Allow {
		t.Errorf("ls = %+v, want an allow", v)
	}
	if v := classify("ls internal > /tmp/local-out.txt"); v.Level != Confirm {
		t.Errorf("ls > file = %+v, want a confirm: the shell writes that file", v)
	}
	// ...except the bit bucket, which is a stream discard rather than a file.
	if v := classify("ls internal 2> /dev/null"); v.Level != Allow {
		t.Errorf("ls 2>/dev/null = %+v, want an allow", v)
	}
}

// TestLocalDefersWithoutAReviewer pins the fail-closed direction: a stage with
// nowhere to defer must not allow, which is the same shape the LLM classifier
// uses when it is not wired.
func TestLocalDefersWithoutAReviewer(t *testing.T) {
	v := Local{}.Classify(context.Background(), "bash", localArgs(t, "rm -rf build"))
	if v.Level == Allow || !v.Failed {
		t.Errorf("without a next reviewer = %+v, want a failed verdict", v)
	}
}

// TestLocalIgnoresOtherTools pins the stage's scope: the table is about shell
// commands, so a non-bash call is not its to grade.
func TestLocalIgnoresOtherTools(t *testing.T) {
	spy := &nextSpy{}
	raw, err := json.Marshal(map[string]string{"path": "internal/x.go"})
	if err != nil {
		t.Fatal(err)
	}
	if v := (Local{Next: spy}).Classify(context.Background(), "read", raw); spy.called != 1 {
		t.Errorf("read tool = %+v; the local stage must defer it", v)
	}
}

// TestLocalRationaleFollowsTheConversationLanguage pins the wording rule the
// decision-model layer states on its own side: the table names what a command IS,
// and the LAYER owns the sentence the user reads — so the sentence follows the
// conversation, not the table's own language.
func TestLocalRationaleFollowsTheConversationLanguage(t *testing.T) {
	zh := agentcore.WithMessageSnapshot(context.Background(), agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("帮我看看这个文件")}},
	})
	en := agentcore.WithMessageSnapshot(context.Background(), agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("have a look at this file")}},
	})

	for _, c := range []struct{ name, cmd string }{
		{"read", "ls internal"},
		{"local work", "mkdir -p /tmp/x-local"},
		{"redirect", "ls internal > /tmp/x-local"},
	} {
		t.Run(c.name, func(t *testing.T) {
			zhVerdict := Local{}.Classify(zh, "bash", localArgs(t, c.cmd))
			if zhVerdict.Lang != "zh" {
				t.Errorf("Lang = %q, want zh (the conversation's language)", zhVerdict.Lang)
			}
			if !hasCJK(zhVerdict.Reasons[0]) {
				t.Errorf("reasons = %q, want a Chinese sentence for a Chinese conversation", zhVerdict.Reasons[0])
			}
			enVerdict := Local{}.Classify(en, "bash", localArgs(t, c.cmd))
			if enVerdict.Lang != "en" || hasCJK(enVerdict.Reasons[0]) {
				t.Errorf("English conversation got %q (lang %q)", enVerdict.Reasons[0], enVerdict.Lang)
			}
		})
	}
}
