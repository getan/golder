package tui

// Tests for how a write_stdin card reads. The tool addresses a shell session by
// an internal id (bash_355), and showing that id as the headline told a reader
// nothing — a run of three polls repeating the same number looks like noise.
// The headline now names the command the session is running, and says what the
// call did to it.

import (
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/agentcore"
)

// wsCard builds a write_stdin card in the given state with a session label.
func wsCard(state cardState, label string, input map[string]any) toolCard {
	return toolCard{name: "write_stdin", state: state, sessionLabel: label, input: input}
}

// TestWriteStdinRendersAsASentence checks what the user actually sees: the
// rendered card line, not just the title string.
func TestWriteStdinRendersAsASentence(t *testing.T) {
	cases := []struct {
		name string
		card toolCard
		want string
	}{
		{"poll", wsCard(cardSuccess, "sleep 90; echo done", map[string]any{"bash_id": "bash_355"}), "Waited for sleep 90; echo done"},
		{"input", wsCard(cardSuccess, "python -i", map[string]any{"bash_id": "bash_355", "chars": "print(1)\n"}), `Sent "print(1)\n" to python -i`},
		{"interrupt", wsCard(cardSuccess, "python -i", map[string]any{"bash_id": "bash_355", "chars": "\u0003"}), "Interrupted python -i"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripANSI(tc.card.render(DefaultTheme(), 80))
			t.Logf("rendered: %s", got)
			if !strings.Contains(got, tc.want) {
				t.Errorf("render = %q, want it to contain %q", got, tc.want)
			}
			if strings.Contains(got, "bash_355") {
				t.Errorf("render = %q, must not show the session id", got)
			}
		})
	}
}

func TestWriteStdinTitlePoll(t *testing.T) {
	const cmd = "sleep 90; echo done"
	running := wsCard(cardRunning, cmd, map[string]any{"bash_id": "bash_355"})
	done := wsCard(cardSuccess, cmd, map[string]any{"bash_id": "bash_355"})

	if got := running.title(); got != "Waiting for "+cmd {
		t.Errorf("running poll title = %q, want %q", got, "Waiting for "+cmd)
	}
	if got := done.title(); got != "Waited for "+cmd {
		t.Errorf("finished poll title = %q, want %q", got, "Waited for "+cmd)
	}
}

// TestWriteStdinTitleNeverShowsTheSessionID is the regression the change is
// about: whatever else the headline says, it must not be the session id.
func TestWriteStdinTitleNeverShowsTheSessionID(t *testing.T) {
	cases := []struct {
		name  string
		state cardState
		label string
		input map[string]any
	}{
		{"poll without a known command", cardRunning, "", map[string]any{"bash_id": "bash_355"}},
		{"finished poll without a known command", cardSuccess, "", map[string]any{"bash_id": "bash_355"}},
		{"input without a known command", cardSuccess, "", map[string]any{"bash_id": "bash_355", "chars": "ls\n"}},
		{"interrupt without a known command", cardSuccess, "", map[string]any{"bash_id": "bash_355", "chars": "\u0003"}},
		{"poll with a known command", cardSuccess, "make test", map[string]any{"bash_id": "bash_355"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wsCard(tc.state, tc.label, tc.input).title()
			if strings.Contains(got, "bash_355") {
				t.Errorf("headline %q must not show the session id", got)
			}
			if got == "" {
				t.Error("headline is empty; the card still needs a title")
			}
		})
	}
}

func TestWriteStdinTitleInput(t *testing.T) {
	card := wsCard(cardSuccess, "python -i", map[string]any{"chars": "ls\n"})
	got := card.title()
	if !strings.HasPrefix(got, `Sent "ls\n" to `) {
		t.Errorf("input title = %q, want it to quote what was sent", got)
	}
	if !strings.Contains(got, "python -i") {
		t.Errorf("input title = %q, want the session's command", got)
	}
}

func TestWriteStdinTitleInterrupt(t *testing.T) {
	for _, chars := range []string{"\u0003", `\u0003`, "^C", "\ue002"} {
		card := wsCard(cardSuccess, "python -i", map[string]any{"chars": chars})
		if got := card.title(); !strings.HasPrefix(got, "Interrupted ") {
			t.Errorf("chars %q title = %q, want an interrupt sentence", chars, got)
		}
	}
	running := wsCard(cardRunning, "python -i", map[string]any{"chars": "\u0003"})
	if got := running.title(); !strings.HasPrefix(got, "Interrupting ") {
		t.Errorf("running interrupt title = %q, want the present tense", got)
	}
}

// TestBashSessionLabelsWriteStdin drives the live sequence: a bash call hands
// back a session id, and the polls that follow name its command.
func TestBashSessionLabelsWriteStdin(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 80, Height: 24})

	m = apply(t, m, toolAnnounceMsg{id: "b1", name: "bash", input: map[string]any{"command": "sleep 90; echo done"}})
	m = apply(t, m, toolEndMsg{
		id: "b1", ok: true, result: "[bash_7: running]",
		details: map[string]any{"bash_id": "bash_7", "status": "running"},
	})
	m = apply(t, m, toolAnnounceMsg{id: "w1", name: "write_stdin", input: map[string]any{"bash_id": "bash_7"}})

	card, ok := m.toolCards["w1"]
	if !ok {
		t.Fatal("write_stdin card was not created")
	}
	if card.sessionLabel != "sleep 90; echo done" {
		t.Fatalf("sessionLabel = %q, want the bash command", card.sessionLabel)
	}
	if got := card.title(); strings.Contains(got, "bash_7") {
		t.Errorf("poll headline %q must not show the session id", got)
	}
}

// TestReplayLabelsWriteStdin covers a resumed session: the replayed cards read
// the same way, using the bash call recorded above them.
func TestReplayLabelsWriteStdin(t *testing.T) {
	call := func(id, name, args string) agentcore.ToolCallContent {
		return agentcore.NewToolCallContent(id, name, json.RawMessage(args))
	}
	bashCall := call("b1", "bash", `{"command":"make test"}`)
	pollCall := call("w1", "write_stdin", `{"bash_id":"bash_9"}`)
	history := agentcore.MessageList{
		agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{bashCall, pollCall}},
		agentcore.ToolResultMessage{
			RoleField: agentcore.RoleToolResult, ToolCallID: "b1", ToolName: "bash",
			Content: agentcore.ContentList{agentcore.NewTextContent("[bash_9: running]")},
			Details: map[string]any{"bash_id": "bash_9", "status": "running"},
		},
		agentcore.ToolResultMessage{
			RoleField: agentcore.RoleToolResult, ToolCallID: "w1", ToolName: "write_stdin",
			Content: agentcore.ContentList{agentcore.NewTextContent("[bash_9: running]")},
			Details: map[string]any{"bash_id": "bash_9", "status": "running"},
		},
	}

	tr := newTranscript(DefaultTheme())
	seedTranscript(&tr, history)

	var titled []string
	for _, b := range tr.blocks {
		if b.role == roleTool && b.card != nil && b.card.name == "write_stdin" {
			titled = append(titled, b.card.title())
		}
	}
	if len(titled) != 1 {
		t.Fatalf("replayed write_stdin cards = %d, want 1", len(titled))
	}
	if got := titled[0]; got != "Waited for make test" {
		t.Errorf("replayed poll title = %q, want %q", got, "Waited for make test")
	}
}
