package mygoui

import (
	"regexp"
	"strings"

	"github.com/egoist/mygo/ui"
)

type mermaidMessage struct {
	from, to int
	text     string
	dashed   bool
}

type mermaidSequence struct {
	participants []string
	messages     []mermaidMessage
}

var mermaidMessagePattern = regexp.MustCompile(`^([A-Za-z_][A-Za-z_0-9]*)\s*(-->>|->>)\s*([A-Za-z_][A-Za-z_0-9]*):\s*(.+)$`)

func parseSimpleMermaidSequence(src string) *mermaidSequence {
	if len(src) == 0 || len(src) > 4096 {
		return nil
	}
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	if len(lines) > 32 {
		return nil
	}
	seq := &mermaidSequence{}
	started, inMessages := false, false
	index := map[string]int{}
	add := func(id string) int {
		if i, ok := index[id]; ok {
			return i
		}
		index[id] = len(seq.participants)
		seq.participants = append(seq.participants, id)
		return len(seq.participants) - 1
	}
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "%%{") {
			return nil
		}
		if line == "" || strings.HasPrefix(line, "%%") {
			continue
		}
		if !started {
			if line != "sequenceDiagram" {
				return nil
			}
			started = true
			continue
		}
		if strings.HasPrefix(line, "participant ") {
			id := strings.TrimSpace(strings.TrimPrefix(line, "participant "))
			m := mermaidNodePattern.FindStringSubmatch(id)
			if inMessages || m == nil || m[2] != "" || len(id) > 32 {
				return nil
			}
			if _, ok := index[id]; ok {
				return nil
			}
			add(id)
		} else {
			m := mermaidMessagePattern.FindStringSubmatch(line)
			if m == nil || m[1] == m[3] || len(m[1]) > 32 || len(m[3]) > 32 || !mermaidPlainLabel(m[4]) || len(seq.messages) >= 16 {
				return nil
			}
			inMessages = true
			seq.messages = append(seq.messages, mermaidMessage{from: add(m[1]), to: add(m[3]), text: m[4], dashed: m[2] == "-->>"})
		}
		if len(seq.participants) > 6 {
			return nil
		}
	}
	if len(seq.participants) < 2 || len(seq.messages) == 0 {
		return nil
	}
	return seq
}

func renderSimpleMermaidSequence(c *ui.Context, seq *mermaidSequence) {
	const pad, headH, rowH = float32(16), float32(34), float32(40)
	column := float32(140)
	for _, id := range seq.participants {
		width, _ := c.MeasureText(0, ui.Span{Text: id, Size: 13})
		column = max(column, width+32)
	}
	for _, msg := range seq.messages {
		width, _ := c.MeasureText(0, ui.Span{Text: msg.text, Size: 12})
		column = max(column, width+32)
	}
	width := float32(len(seq.participants))*column + pad*2
	height := headH + float32(len(seq.messages))*rowH + pad*3
	ui.Column(c).Padding(8).Radius(7).Background(c.Theme().Surface).Children(func() {
		ui.Text(c, "Mermaid preview (simple sequence)").FontSize(11).TextColor(c.Theme().TextMuted)
		ui.ScrollHorizontal(c).AlignSelf(ui.Stretch).Children(func() {
			ui.Box(c).Width(width).Height(height).Label("Mermaid sequence preview").Draw(func(p *ui.Painter, r ui.Rect) {
				t := p.Theme()
				for i, id := range seq.participants {
					x := r.X + pad + (float32(i)+.5)*column
					y := r.Y + pad
					p.Stroke(ui.Rect{X: x - column*.4, Y: y, W: column * .8, H: headH}, t.TextMuted, 5, 1)
					w, h := p.MeasureText(0, ui.Span{Text: id, Size: 13})
					p.Text(x-w/2, y+(headH-h)/2, id, 13, t.Text)
					line := (&ui.Path{}).MoveTo(x, y+headH).LineTo(x, r.Y+height-pad).Dash(0, 4, 4)
					p.StrokePath(line, 1, t.TextMuted)
				}
				for i, msg := range seq.messages {
					x1 := r.X + pad + (float32(msg.from)+.5)*column
					x2 := r.X + pad + (float32(msg.to)+.5)*column
					y := r.Y + pad + headH + float32(i+1)*rowH
					line := (&ui.Path{}).MoveTo(x1, y).LineTo(x2, y)
					if msg.dashed {
						line.Dash(0, 5, 4)
					}
					p.StrokePath(line, 1.5, t.TextMuted)
					dir := float32(1)
					if x2 < x1 {
						dir = -1
					}
					arrow := (&ui.Path{}).MoveTo(x2-dir*7, y-4).LineTo(x2, y).LineTo(x2-dir*7, y+4)
					p.StrokePath(arrow, 1.5, t.TextMuted)
					textW, _ := p.MeasureText(0, ui.Span{Text: msg.text, Size: 12})
					p.Text((x1+x2-textW)/2, y-19, msg.text, 12, t.Text)
				}
			})
		})
	})
}
