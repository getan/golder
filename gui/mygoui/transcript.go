package mygoui

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"github.com/getan/golder/gui/appsession"
	"github.com/getan/golder/internal/judge"
)

// itemView renders one transcript row.
func (a *App) itemView(c *ui.Context, it appsession.Item) {
	switch it.Kind {
	case appsession.ItemUser:
		a.userBubble(c, it)
	case appsession.ItemAssistant:
		a.assistantBlock(c, it)
	case appsession.ItemThinking:
		a.thinkingBlock(c, it)
	case appsession.ItemTool:
		a.toolCard(c, it)
	case appsession.ItemNote:
		a.noteLine(c, it)
	default:
		a.noticeLine(c, it)
	}
}

func (a *App) userBubble(c *ui.Context, it appsession.Item) {
	t := c.Theme()
	ui.Row(c.Key(it.ID)).Children(func() {
		ui.Spacer(c)
		ui.Text(c, it.Text).Selectable().FontSize(14).Padding(9, 13).Radius(12).
			Background(t.Surface).MaxWidth(620)
	})
}

func (a *App) assistantBlock(c *ui.Context, it appsession.Item) {
	root := ui.Column(c.Key(it.ID)).MaxWidth(820)
	state := *ui.Local(root, transcriptMarkdownKey{}, func() *transcriptMarkdownState { return &transcriptMarkdownState{} })
	if state.options == nil || state.project != a.project {
		state.project = a.project
		state.options = transcriptMarkdownOptions(a.project)
	}
	root.Children(func() {
		if it.Done {
			markdown.View(c, it.Text, state.options)
			return
		}
		markdown.Stream(c, it.Text, state.options)
	})
}

func (a *App) thinkingBlock(c *ui.Context, it appsession.Item) {
	t := c.Theme()
	open := a.thinkingOpen[it.ID]
	ui.Collapsible(c.Key(it.ID), "Thinking", &open, func() {
		ui.Text(c, it.Text).FontSize(12).TextColor(t.TextMuted).Selectable()
	})
	a.thinkingOpen[it.ID] = open
}

// toolCard renders a tool call, collapsed to its headline by default.
func (a *App) toolCard(c *ui.Context, it appsession.Item) {
	a.toolCardDetails(c, it, nil, nil)
}

func (a *App) toolCardDetails(c *ui.Context, it appsession.Item, notes, approvals []int) {
	t := c.Theme()
	st := it.Tool
	if st == nil {
		return
	}
	head := toolHeadline(*st)
	if len(approvals) > 0 {
		head += " · approval required"
	}
	var outcomes []string
	for _, index := range notes {
		outcome := reviewOutcome(a.items[index])
		if outcome != "" && !slices.Contains(outcomes, outcome) {
			outcomes = append(outcomes, outcome)
		}
	}
	if len(outcomes) > 0 {
		head += " · " + strings.Join(outcomes, ", ")
	}
	open, known := a.toolOpen[it.ID]
	if !known && len(approvals) > 0 {
		open = true
	}
	ui.Collapsible(c.Key(it.ID), head, &open, func() {
		for _, index := range approvals {
			a.approvalCard(c, a.pending[index])
		}
		for _, index := range notes {
			a.noteLine(c, a.items[index])
		}
		patchRendered, patchComplete := false, false
		if st.Name == "apply_patch" {
			patchRendered, patchComplete = renderApplyPatch(c, st.Input)
		}
		if len(st.Input) > 0 && !patchRendered {
			ui.Text(c, formatInput(st.Input)).FontSize(11.5).TextColor(t.TextMuted).Selectable()
		}
		if st.Output != "" {
			output := st.Output
			if patchComplete && !st.Running && !it.Failed && !st.Error {
				output = applyPatchDisplayOutput(output, st.Details)
			}
			if output != "" {
				renderToolOutput(c, truncate(output, 12000), it.Failed || st.Error)
			}
		} else if st.Running && len(approvals) > 0 {
			ui.Text(c, "Waiting for approval…").FontSize(12).TextColor(t.TextMuted)
		} else if st.Running && st.AwaitingStart {
			ui.Text(c, "Waiting to run…").FontSize(12).TextColor(t.TextMuted)
		} else if st.Running {
			ui.Text(c, "Running…").FontSize(12).TextColor(t.TextMuted)
		} else if it.Failed || st.Error {
			renderToolOutput(c, "The command failed without diagnostic output.", true)
		}
		if id := toolTTYID(st.Details, st.Input); id != "" {
			if subtleButton(c, "Open Terminal").Tooltip("Attach to " + id).Clicked() {
				a.openTTYTerminal(id)
			}
		}
	})
	a.toolOpen[it.ID] = open
}

func renderToolOutput(c *ui.Context, output string, failed bool) {
	t := c.Theme()
	if !failed {
		ui.Text(c, output).MarginStart(18).FontSize(12).TextColor(t.Text).Selectable().Wrap()
		return
	}
	ui.Column(c).MarginStart(18).Padding(8, 10).Gap(5).Radius(7).
		Background(t.Danger.Alpha(.06)).Border(1, t.Danger.Alpha(.22)).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "×").FontSize(11).FontWeight(700).TextColor(t.Danger)
			ui.Text(c, "Command failed").FontSize(11).FontWeight(600).TextColor(t.Text)
		})
		ui.Text(c, output).Font("monospace").FontSize(11.5).TextColor(t.Text).Selectable().Wrap()
	})
}

func (a *App) noteLine(c *ui.Context, it appsession.Item) {
	t := c.Theme()
	color := t.TextMuted
	if it.Failed {
		color = t.Danger
	}
	text := it.Text
	if it.Note != nil && it.Note.Summary != "" && !strings.Contains(text, it.Note.Summary) {
		text = it.Note.Summary + "\n" + text
	}
	open := a.approvalOpen[it.ID]
	head := "Review · " + reviewOutcome(it)
	if it.Note != nil && it.Note.Origin != "" {
		head += " · from " + oneLine(it.Note.Origin, 80)
	}
	ui.Collapsible(c.Key(it.ID), head, &open, func() {
		ui.Text(c, text).FontSize(12).TextColor(color).Selectable().Wrap()
	}).TextColor(color)
	a.approvalOpen[it.ID] = open
}

// reviewOutcome keeps the security-relevant disposition visible when folded.
func reviewOutcome(it appsession.Item) string {
	if it.Note == nil {
		return "decision"
	}
	switch judge.NoteKind(it.Note.Kind) {
	case judge.NoteApproved:
		return "approved"
	case judge.NoteSandboxed:
		return "sandboxed"
	case judge.NoteEscaped:
		return "outside sandbox"
	case judge.NoteDenied:
		return "denied"
	case judge.NoteUnavailable:
		return "review unavailable"
	case judge.NoteBlockedNoPrompt, judge.NoteReadOnly:
		return "blocked"
	case judge.NoteBoundaryGranted:
		return "outside access allowed"
	case judge.NoteBoundaryDenied, judge.NoteBoundaryNoPrompt:
		return "outside access denied"
	default:
		return "decision"
	}
}

func (a *App) noticeLine(c *ui.Context, it appsession.Item) {
	t := c.Theme()
	color := t.TextMuted
	if it.Failed {
		color = t.Danger
	}
	ui.Text(c.Key(it.ID), it.Text).FontSize(12).TextColor(color).Selectable()
}

// toolHeadline summarizes a call the way a reader scans it: the command for a
// shell call, the path for a file call, otherwise the tool and its first
// argument.
func toolHeadline(st appsession.ToolState) string {
	name := st.Name
	if name == "" {
		name = "tool"
	}
	status := "done"
	switch {
	case st.Running && st.AwaitingStart:
		status = "pending"
	case st.Running:
		status = "running"
	case st.Error:
		status = "failed"
	}
	subject := toolSubject(st)
	if subject == "" {
		return fmt.Sprintf("%s · %s", name, status)
	}
	return fmt.Sprintf("%s · %s · %s", name, oneLine(subject, 110), status)
}

func toolSubject(st appsession.ToolState) string {
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := st.Input[k]; ok {
				if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
					return s
				}
			}
		}
		return ""
	}
	switch st.Name {
	case "bash", "shell":
		return pick("command")
	case "apply_patch":
		return applyPatchSubject(st.Input)
	case "read", "write", "edit", "multiedit":
		if p := pick("path", "file_path", "file"); p != "" {
			return p
		}
	case "grep", "find", "ls":
		if q := pick("pattern", "query", "path"); q != "" {
			return q
		}
	case "webfetch", "websearch":
		if q := pick("url", "query"); q != "" {
			return q
		}
	case "task":
		return pick("description", "prompt")
	}
	keys := make([]string, 0, len(st.Input))
	for k := range st.Input {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if s, ok := st.Input[k].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func formatInput(in map[string]any) string {
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s: %v\n", k, in[k])
	}
	return strings.TrimRight(b.String(), "\n")
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	if i := strings.LastIndexByte(cut, '\n'); i > max/2 {
		cut = cut[:i]
	}
	return cut + fmt.Sprintf("\n… (%d bytes truncated)", len(s)-len(cut))
}
