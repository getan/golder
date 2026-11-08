package mygoui

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// The native renderer intentionally accepts only an unstyled directed tree.
// Reject everything else rather than silently drawing a different graph.
type mermaidNode struct {
	id, label string
	shape     string
	explicit  bool
}
type mermaidEdge struct {
	from, to int
	label    string
}
type mermaidGraph struct {
	horizontal bool
	nodes      []mermaidNode
	edges      []mermaidEdge
	root       int
}

var mermaidNodePattern = regexp.MustCompile(`^([A-Za-z_][A-Za-z_0-9]*)(?:\[([^\[\]\r\n]+)\])?$`)
var mermaidGraphNodePattern = regexp.MustCompile(`^([A-Za-z_][A-Za-z_0-9]*)(?:\[([^\[\]\r\n]+)\]|\{([^{}\r\n]+)\}|\(([^()\r\n]+)\))?$`)

func mermaidPlainLabel(text string) bool {
	if utf8.RuneCountInString(text) > 24 {
		return false
	}
	for _, r := range text {
		if !unicode.IsLetter(r) && !unicode.IsNumber(r) && !strings.ContainsRune(" -_.,!?():", r) {
			return false
		}
	}
	return true
}

func parseSimpleMermaid(src string) *mermaidGraph {
	if len(src) == 0 || len(src) > 4096 {
		return nil
	}
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	if len(lines) > 20 {
		return nil
	}
	var graph *mermaidGraph
	indices := map[string]int{}
	var incoming []int
	edges := map[[2]int]bool{}
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "%%{") {
			return nil
		}
		if line == "" || strings.HasPrefix(line, "%%") {
			continue
		}
		if graph == nil {
			fields := strings.Fields(line)
			if len(fields) != 2 || (fields[0] != "graph" && fields[0] != "flowchart") {
				return nil
			}
			if fields[1] != "LR" && fields[1] != "TD" && fields[1] != "TB" {
				return nil
			}
			graph = &mermaidGraph{horizontal: fields[1] == "LR"}
			continue
		}
		line = strings.TrimSpace(strings.TrimSuffix(line, ";"))
		segments := strings.Split(line, "-->")
		if len(segments) < 2 || len(segments) > 8 {
			return nil
		}
		parse := func(raw string) (mermaidNode, bool) {
			m := mermaidGraphNodePattern.FindStringSubmatch(strings.TrimSpace(raw))
			if m == nil || utf8.RuneCountInString(m[1]) > 32 {
				return mermaidNode{}, false
			}
			label, shape := m[2], "rect"
			if m[3] != "" {
				label, shape = m[3], "diamond"
			} else if m[4] != "" {
				label, shape = m[4], "round"
			}
			if strings.HasPrefix(label, `"`) && strings.HasSuffix(label, `"`) && len(label) > 2 {
				label = label[1 : len(label)-1]
			}
			if !mermaidPlainLabel(label) {
				return mermaidNode{}, false
			}
			return mermaidNode{id: m[1], label: label, shape: shape, explicit: label != ""}, true
		}
		previous := -1
		for _, segment := range segments {
			segment = strings.TrimSpace(segment)
			edgeLabel := ""
			if strings.HasPrefix(segment, "|") {
				if previous < 0 {
					return nil
				}
				label, rest, ok := strings.Cut(segment[1:], "|")
				if !ok || label == "" || !mermaidPlainLabel(label) {
					return nil
				}
				edgeLabel, segment = label, rest
			}
			node, ok := parse(segment)
			if !ok {
				return nil
			}
			index, exists := indices[node.id]
			if !exists {
				index = len(graph.nodes)
				indices[node.id] = index
				if node.label == "" {
					node.label = node.id
				}
				graph.nodes = append(graph.nodes, node)
				incoming = append(incoming, 0)
			} else if node.explicit {
				old := &graph.nodes[index]
				if old.explicit && (old.label != node.label || old.shape != node.shape) {
					return nil
				}
				old.label, old.shape, old.explicit = node.label, node.shape, true
			}
			if len(graph.nodes) > 8 {
				return nil
			}
			if previous >= 0 {
				key := [2]int{previous, index}
				if previous == index || edges[key] || incoming[index] != 0 {
					return nil
				}
				edges[key] = true
				incoming[index]++
				graph.edges = append(graph.edges, mermaidEdge{from: previous, to: index, label: edgeLabel})
			}
			previous = index
		}
	}
	if graph == nil || len(graph.nodes) < 2 || len(graph.edges) != len(graph.nodes)-1 {
		return nil
	}
	root := -1
	for i, n := range incoming {
		if n == 0 {
			if root != -1 {
				return nil
			}
			root = i
		}
	}
	if root < 0 {
		return nil
	}
	children := make([][]int, len(graph.nodes))
	for _, edge := range graph.edges {
		children[edge.from] = append(children[edge.from], edge.to)
	}
	visited := make([]bool, len(graph.nodes))
	var walk func(int)
	walk = func(i int) {
		if visited[i] {
			return
		}
		visited[i] = true
		for _, child := range children[i] {
			walk(child)
		}
	}
	walk(root)
	for _, ok := range visited {
		if !ok {
			return nil
		}
	}
	graph.root = root
	return graph
}

type mermaidPreviewKey struct{}
type mermaidPreviewState struct {
	source string
	graph  *mermaidGraph
	seq    *mermaidSequence
	rich   richPreviewState
}

func renderMermaidCodeBlock(c *ui.Context, b *markdown.CodeBlockView) {
	if b.Open {
		markdown.DefaultCodeBlock(c, b)
		return
	}
	box := ui.Column(c).AlignSelf(ui.Stretch).Gap(6)
	state := *ui.Local(box, mermaidPreviewKey{}, func() *mermaidPreviewState { return &mermaidPreviewState{} })
	if src := b.Code(); src != state.source {
		state.source, state.graph = src, parseSimpleMermaid(src)
		state.seq = parseSimpleMermaidSequence(src)
	}
	box.Children(func() {
		bitmap, _ := state.rich.preview(c, state.source, true)
		switch {
		case bitmap != nil:
			drawRichBitmap(c, bitmap, "Mermaid preview")
		case state.graph != nil:
			renderSimpleMermaid(c, state.graph)
		case state.seq != nil:
			renderSimpleMermaidSequence(c, state.seq)
		default:
			ui.Text(c, "Mermaid preview unavailable; showing source").FontSize(11).TextColor(c.Theme().TextMuted)
		}
		markdown.DefaultCodeBlock(c, b)
	})
}

func renderSimpleMermaid(c *ui.Context, graph *mermaidGraph) {
	const height, gap, pad = float32(38), float32(34), float32(16)
	width := float32(100)
	for _, node := range graph.nodes {
		w, _ := c.MeasureText(0, ui.Span{Text: node.label, Size: 13})
		if node.shape == "diamond" {
			w *= 1.6
		}
		width = max(width, w+24)
	}
	children := make([][]int, len(graph.nodes))
	for _, edge := range graph.edges {
		children[edge.from] = append(children[edge.from], edge.to)
	}
	levels := make([]int, len(graph.nodes))
	positions := make([]float32, len(graph.nodes))
	leaves, maxLevel := 0, 0
	var place func(int, int) float32
	place = func(i, level int) float32 {
		levels[i] = level
		maxLevel = max(maxLevel, level)
		if len(children[i]) == 0 {
			positions[i] = float32(leaves)
			leaves++
			return positions[i]
		}
		first, last := float32(0), float32(0)
		for j, child := range children[i] {
			pos := place(child, level+1)
			if j == 0 {
				first = pos
			}
			last = pos
		}
		positions[i] = (first + last) / 2
		return positions[i]
	}
	place(graph.root, 0)
	canvasW := float32(leaves)*width + float32(leaves-1)*gap + 2*pad
	canvasH := float32(maxLevel+1)*height + float32(maxLevel)*gap + 2*pad
	if graph.horizontal {
		canvasW = float32(maxLevel+1)*width + float32(maxLevel)*gap + 2*pad
		canvasH = float32(leaves)*height + float32(leaves-1)*gap + 2*pad
	}
	coords := func(i int, r ui.Rect) (float32, float32) {
		if graph.horizontal {
			return r.X + pad + float32(levels[i])*(width+gap), r.Y + pad + positions[i]*(height+gap)
		}
		return r.X + pad + positions[i]*(width+gap), r.Y + pad + float32(levels[i])*(height+gap)
	}
	ui.Column(c).Padding(8).Radius(7).Background(c.Theme().Surface).Children(func() {
		ui.Text(c, "Mermaid preview (simple flowchart)").FontSize(11).TextColor(c.Theme().TextMuted)
		ui.ScrollHorizontal(c).AlignSelf(ui.Stretch).Children(func() {
			ui.Box(c).Width(canvasW).Height(canvasH).Label("Mermaid flowchart preview").Draw(func(p *ui.Painter, r ui.Rect) {
				t := p.Theme()
				for _, edge := range graph.edges {
					x1, y1 := coords(edge.from, r)
					x2, y2 := coords(edge.to, r)
					path := &ui.Path{}
					if graph.horizontal {
						x1, y1, y2 = x1+width, y1+height/2, y2+height/2
						path.MoveTo(x1, y1).LineTo((x1+x2)/2, y1).LineTo((x1+x2)/2, y2).LineTo(x2, y2)
						arrow := (&ui.Path{}).MoveTo(x2-6, y2-4).LineTo(x2, y2).LineTo(x2-6, y2+4)
						p.StrokePath(arrow, 1.5, t.TextMuted)
					} else {
						x1, x2, y1 = x1+width/2, x2+width/2, y1+height
						path.MoveTo(x1, y1).LineTo(x1, (y1+y2)/2).LineTo(x2, (y1+y2)/2).LineTo(x2, y2)
						arrow := (&ui.Path{}).MoveTo(x2-4, y2-6).LineTo(x2, y2).LineTo(x2+4, y2-6)
						p.StrokePath(arrow, 1.5, t.TextMuted)
					}
					p.StrokePath(path, 1.5, t.TextMuted)
					if edge.label != "" {
						labelWidth, _ := p.MeasureText(0, ui.Span{Text: edge.label, Size: 11})
						x, y := (x1+x2-labelWidth)/2, (y1+y2)/2-16
						p.Fill(ui.Rect{X: x - 2, Y: y, W: labelWidth + 4, H: 15}, t.Surface, 0)
						p.Text(x, y, edge.label, 11, t.Text)
					}
				}
				for i, node := range graph.nodes {
					x, y := coords(i, r)
					nodeRect := ui.Rect{X: x, Y: y, W: width, H: height}
					if node.shape == "diamond" {
						path := (&ui.Path{}).MoveTo(x+width/2, y).LineTo(x+width, y+height/2).LineTo(x+width/2, y+height).LineTo(x, y+height/2).Close()
						p.StrokePath(path, 1, t.TextMuted)
					} else {
						radius := float32(0)
						if node.shape == "round" {
							radius = 12
						}
						p.Stroke(nodeRect, t.TextMuted, radius, 1)
					}
					textW, textH := p.MeasureText(0, ui.Span{Text: node.label, Size: 13})
					p.Text(x+(width-textW)/2, y+(height-textH)/2, node.label, 13, t.Text)
				}
			})
		})
	})
}
