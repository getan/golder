package cli

// Tests for rebuilding a resumed session's system prompt: our own text (guide,
// environment, AGENTS.md, skills) comes from the launching binary, the user's
// own inputs are carried over from the session header, and a rebuild failure
// falls back to the stored prompt.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/runtime"
	"github.com/getan/golder/internal/session"
)

// testPromptInputs builds launch inputs for a temp working directory with no
// AGENTS.md and no skills, so the rebuilt prompt is the plain default guide.
func testPromptInputs(t *testing.T, base string, appends ...string) runtime.PromptInputs {
	t.Helper()
	return runtime.PromptInputs{Base: base, Appends: appends, WorkingDir: t.TempDir()}
}

// TestResumeSystemPromptRebuildsGuide is the point of the change: a session
// whose stored prompt carries an obsolete guide is resumed with the CURRENT
// one, so the model is not advised about a tool surface that has since moved.
func TestResumeSystemPromptRebuildsGuide(t *testing.T) {
	// The markers are deliberately unlike anything in the current guide: a
	// substring like "/old" collides with the patch guide's "path/old.go".
	stale := "You are golder, a helpful coding agent.\n\n[obsolete guide: use bash for a scoped search]\n\nEnvironment:\n- Working directory: /stale-project-dir\n- Date: 2026-09-28"
	h := session.SessionHeader{SystemPrompt: stale}

	got := ResumeSystemPrompt(h, testPromptInputs(t, ""))

	if got == stale {
		t.Fatal("the stored prompt must not be reused verbatim")
	}
	if !strings.HasPrefix(got, runtime.DefaultBaseInstruction) {
		t.Errorf("rebuilt prompt should open with the current guide, got:\n%.200s", got)
	}
	if strings.Contains(got, "obsolete guide") || strings.Contains(got, "Date: 2026-09-28") {
		t.Errorf("rebuilt prompt must not carry the stored guide or date:\n%s", got)
	}
	// The environment block is rebuilt for the launching directory, not the
	// recorded one.
	if strings.Contains(got, "stale-project-dir") {
		t.Errorf("rebuilt prompt must not carry the recorded working directory:\n%s", got)
	}
	if !strings.Contains(got, "Environment:") {
		t.Errorf("rebuilt prompt should still carry an environment block:\n%s", got)
	}
}

// TestResumeSystemPromptKeepsSessionInputs: the user's own words survive the
// rebuild — a session created with --system-prompt runs under that base
// whenever it is resumed.
func TestResumeSystemPromptKeepsSessionInputs(t *testing.T) {
	h := session.SessionHeader{
		SystemPrompt:       "custom base\n\nEnvironment:\n- Date: 2026-09-28",
		BaseInstruction:    "custom base",
		AppendInstructions: []string{"session appendix"},
	}

	got := ResumeSystemPrompt(h, testPromptInputs(t, ""))

	if !strings.HasPrefix(got, "custom base") {
		t.Errorf("the session's custom base must be preserved, got:\n%.200s", got)
	}
	if strings.Contains(got, runtime.DefaultBaseInstruction) && !strings.Contains(runtime.DefaultBaseInstruction, "custom base") {
		t.Errorf("a custom base must replace the default guide:\n%.200s", got)
	}
	if !strings.Contains(got, "session appendix") {
		t.Errorf("the session's appended block must be preserved:\n%s", got)
	}
}

// TestResumeSystemPromptLaunchWins: an explicit flag on the resuming command
// line is the user telling us now, so it beats the session's recorded value.
func TestResumeSystemPromptLaunchWins(t *testing.T) {
	h := session.SessionHeader{SystemPrompt: "stored", BaseInstruction: "stored base", AppendInstructions: []string{"stored appendix"}}

	got := ResumeSystemPrompt(h, testPromptInputs(t, "launch base", "launch appendix"))

	if !strings.HasPrefix(got, "launch base") {
		t.Errorf("this launch's base must win, got:\n%.200s", got)
	}
	if strings.Contains(got, "stored base") {
		t.Errorf("the stored base must not leak in alongside the launch one:\n%.200s", got)
	}
	// Appends layer: the session's block first, then this launch's.
	iStored := strings.Index(got, "stored appendix")
	iLaunch := strings.Index(got, "launch appendix")
	if iStored < 0 || iLaunch < 0 {
		t.Fatalf("both appended blocks must survive (stored=%d launch=%d):\n%s", iStored, iLaunch, got)
	}
	if iStored > iLaunch {
		t.Errorf("the session's appendix should come before this launch's (stored=%d launch=%d)", iStored, iLaunch)
	}
}

// TestResumeSystemPromptFallsBackOnError: an AGENTS.md that exists but cannot
// be read makes the rebuild fail; a resume must then use the stored prompt
// rather than start with an empty one.
func TestResumeSystemPromptFallsBackOnError(t *testing.T) {
	dir := t.TempDir()
	// A directory named AGENTS.md: ReadFile returns an error that is not
	// IsNotExist, which is the build's only failure path.
	if err := os.Mkdir(filepath.Join(dir, "AGENTS.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := session.SessionHeader{SystemPrompt: "stored prompt"}

	got := ResumeSystemPrompt(h, runtime.PromptInputs{WorkingDir: dir})

	if got != "stored prompt" {
		t.Errorf("a failed rebuild must fall back to the stored prompt, got:\n%.200s", got)
	}
}

// TestResumeSystemPromptAdvertisesSkills: the rebuilt prompt still carries the
// installed skills, which is why a resume must not simply reuse a stored
// prompt nor drop the launch's skill set.
func TestResumeSystemPromptAdvertisesSkills(t *testing.T) {
	skills := []*runtime.Skill{{Frontmatter: runtime.SkillFrontmatter{Name: "weather", Description: "get weather"}, Path: "/skills/weather.md"}}
	in := runtime.PromptInputs{WorkingDir: t.TempDir(), Skills: skills, Tools: []agentcore.AgentTool{&agenttool.ReadTool{}}}

	got := ResumeSystemPrompt(session.SessionHeader{SystemPrompt: "stale"}, in)

	if !strings.Contains(got, "<available_skills>") || !strings.Contains(got, "weather") {
		t.Errorf("the rebuilt prompt must advertise the launch's skills:\n%s", got)
	}
}
