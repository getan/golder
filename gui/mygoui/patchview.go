package mygoui

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

const maxRenderedPatchLines = 1000

type patchLineKind uint8

const (
	patchContext patchLineKind = iota
	patchAdd
	patchDelete
	patchHunk
	patchMeta
)

type patchViewLine struct {
	kind patchLineKind
	text string
}

type patchViewFile struct {
	action string
	path   string
	lines  []patchViewLine
}

func applyPatchInput(input map[string]any) string {
	for _, key := range []string{"patch", "input"} {
		if value, ok := input[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func parseApplyPatch(raw string) []patchViewFile {
	var files []patchViewFile
	current := -1
	lineCount := 0
	start := func(action, path string) {
		if lineCount >= maxRenderedPatchLines {
			return
		}
		path = strings.TrimSpace(path)
		if path == "" {
			path = "Unknown file"
		}
		files = append(files, patchViewFile{action: action, path: path})
		current = len(files) - 1
	}
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "*** Update File:"):
			start("Modified", strings.TrimPrefix(line, "*** Update File:"))
			continue
		case strings.HasPrefix(line, "*** Add File:"):
			start("Added", strings.TrimPrefix(line, "*** Add File:"))
			continue
		case strings.HasPrefix(line, "*** Delete File:"):
			start("Deleted", strings.TrimPrefix(line, "*** Delete File:"))
			continue
		case line == "*** Begin Patch" || line == "*** End Patch":
			continue
		}
		if current < 0 {
			if strings.TrimSpace(line) == "" {
				continue
			}
			start("Patch", "Patch")
		}
		if lineCount >= maxRenderedPatchLines {
			continue
		}
		viewLine := patchViewLine{kind: patchContext, text: line}
		switch {
		case strings.HasPrefix(line, "@@"):
			viewLine.kind = patchHunk
		case strings.HasPrefix(line, "***"):
			viewLine.kind = patchMeta
		case strings.HasPrefix(line, "+"):
			viewLine.kind, viewLine.text = patchAdd, strings.TrimPrefix(line, "+")
		case strings.HasPrefix(line, "-"):
			viewLine.kind, viewLine.text = patchDelete, strings.TrimPrefix(line, "-")
		case strings.HasPrefix(line, " "):
			viewLine.text = strings.TrimPrefix(line, " ")
		}
		files[current].lines = append(files[current].lines, viewLine)
		lineCount++
	}
	return files
}

func applyPatchSubject(input map[string]any) string {
	files := parseApplyPatch(applyPatchInput(input))
	switch len(files) {
	case 0:
		return "patch"
	case 1:
		return files[0].path
	default:
		return fmt.Sprintf("%d files", len(files))
	}
}

func renderApplyPatch(c *ui.Context, input map[string]any) bool {
	files := parseApplyPatch(applyPatchInput(input))
	if len(files) == 0 {
		return false
	}
	t := c.Theme()
	ui.Column(c).MarginStart(18).Gap(8).Children(func() {
		for fileIndex, file := range files {
			file := file
			ui.Column(c.Key(fmt.Sprintf("patch-file:%d:%s", fileIndex, file.path))).Radius(8).Border(1, t.Border).Clip().Children(func() {
				ui.Row(c).Padding(6, 10).Gap(8).AlignItems(ui.Center).Background(t.SurfacePressed).Children(func() {
					ui.Text(c, file.path).FontSize(12).FontWeight(600).TextColor(t.Text).Grow(1).SingleLine().Ellipsis("…").Tooltip(file.path)
					ui.Text(c, file.action).FontSize(10.5).TextColor(t.TextMuted)
				})
				for lineIndex, line := range file.lines {
					line := line
					row := ui.Row(c.Key(fmt.Sprintf("patch-line:%d:%d", fileIndex, lineIndex))).Padding(2, 8).Gap(6).AlignItems(ui.Start)
					marker, markerColor, textColor := " ", t.TextMuted, t.Text
					switch line.kind {
					case patchAdd:
						marker, markerColor = "+", t.Success
						row.Background(t.Success.Alpha(.10))
					case patchDelete:
						marker, markerColor = "−", t.Danger
						row.Background(t.Danger.Alpha(.08))
					case patchHunk:
						marker, textColor = "@@", t.TextMuted
						row.Background(t.Surface)
					case patchMeta:
						marker, textColor = "·", t.TextMuted
					}
					row.Children(func() {
						ui.Text(c, marker).Width(22).Font("monospace").FontSize(11).TextColor(markerColor).TextAlign(ui.Center)
						text := line.text
						if text == "" {
							text = " "
						}
						ui.Text(c, text).Grow(1).Font("monospace").FontSize(11.5).TextColor(textColor).Selectable().Wrap()
					})
				}
			})
		}
	})
	return true
}
