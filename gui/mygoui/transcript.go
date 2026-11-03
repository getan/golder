package mygoui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"github.com/getan/golder/gui/appsession"
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
	ui.Column(c.Key(it.ID)).MaxWidth(820).Children(func() {
		if it.Done {
			markdown.View(c, it.Text, &a.md)
			return
		}
		markdown.Stream(c, it.Text, &a.md)
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
	t := c.Theme()
	st := it.Tool
	if st == nil {
		return
	}
	head := toolHeadline(*st)
	open := a.toolOpen[it.ID]
	el := ui.Collapsible(c.Key(it.ID), head, &open, func() {
		if len(st.Input) > 0 {
			ui.Text(c, formatInput(st.Input)).FontSize(11.5).TextColor(t.TextMuted).Selectable()
		}
		if st.Output != "" {
			ui.Text(c, truncate(st.Output, 12000)).FontSize(12).Selectable()
		} else if st.Running {
			ui.Text(c, "Running…").FontSize(12).TextColor(t.TextMuted)
		}
	})
	if it.Failed {
		el.TextColor(t.Danger)
	}
	a.toolOpen[it.ID] = open
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
	ui.Row(c.Key(it.ID)).Gap(6).Children(func() {
		ui.Text(c, "⚠").FontSize(12).TextColor(color)
		ui.Text(c, text).FontSize(12).TextColor(color).Selectable().Grow(1)
	})
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
	case "read", "write", "edit", "apply_patch", "multiedit":
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
