package agenttool

// The visual tool covers the data-analysis side of a session: a chart drawn
// from a CSV/JSON file, a Mermaid diagram rendered through tuios' sketch, and
// a TML dashboard that stays live in a pane.
//
// chart is pure Go and works without tuios: it renders terminal cells, which
// the model reads directly and a person can keep in the transcript. diagram
// shells out to the installed tuios (sketch is a large dependency). dashboard
// speaks the daemon's TML verbs.

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/tuiosctl"
)

// NewVisualTool builds the facade. allowControl=false keeps chart only: a
// sub-agent may analyse data, not open panes or write artifacts.
func NewVisualTool(cwd string, allowControl bool) *VisualTool {
	return &VisualTool{cwd: cwd, allowControl: allowControl}
}

// VisualTool draws charts and diagrams and opens TML dashboards.
type VisualTool struct {
	cwd          string
	allowControl bool
	newClient    func(context.Context) (*tuiosctl.Client, error)
}

func (t *VisualTool) Name() string { return "visual" }

func (t *VisualTool) Description() string {
	if !hasTuiosBinary() {
		return "Draw data as terminal charts (bar/line/sparkline/histogram/pie/gauge/heatmap) " +
			"from inline JSON or a workspace file. Diagrams and dashboards need tuios installed."
	}
	if t.allowControl {
		return "Draw data as terminal charts (bar/line/sparkline/histogram/pie/gauge/heatmap), " +
			"render Mermaid diagrams through tuios, and keep a TML dashboard live in a pane. " +
			"Data is inline JSON or a CSV/JSON file in the workspace."
	}
	return "Draw data as terminal charts (bar/line/sparkline/histogram/pie/gauge/heatmap) from inline JSON or a workspace file."
}

func (t *VisualTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionParallel
}

const visualReadSchema = `{
  "type":"object","additionalProperties":false,"required":["action"],
  "properties":{
    "action":{"type":"string","enum":["chart"]},
    "kind":{"type":"string","enum":["bar","line","sparkline","histogram","pie","gauge","heatmap"],"description":"Chart kind."},
    "data":{"description":"Inline data: an array of numbers, or an array of row objects."},
    "file":{"type":"string","description":"A CSV or JSON file in the workspace, instead of data."},
    "x":{"type":"string","description":"Label field for row data."},
    "y":{"type":"string","description":"Value field, or a comma list of fields for several series."},
    "value":{"type":"number","description":"For gauge: the value."},
    "max":{"type":"number","description":"For gauge: the maximum; default 100."},
    "bins":{"type":"integer","minimum":1,"maximum":50,"description":"For histogram."},
    "width":{"type":"integer","minimum":10,"maximum":200,"description":"Columns; default 60."},
    "height":{"type":"integer","minimum":3,"maximum":60,"description":"Rows; default 12."},
    "title":{"type":"string","description":"Caption above the chart."}
  }
}`

const visualControlSchema = `{
  "type":"object","additionalProperties":false,"required":["action"],
  "properties":{
    "action":{"type":"string","enum":["chart","diagram","dashboard_open","dashboard_patch","dashboard_close"]},
    "kind":{"type":"string","enum":["bar","line","sparkline","histogram","pie","gauge","heatmap"],"description":"For chart: kind."},
    "data":{"description":"Inline data: an array of numbers, or an array of row objects."},
    "file":{"type":"string","description":"For chart: a CSV or JSON file in the workspace."},
    "x":{"type":"string","description":"For chart: label field for row data."},
    "y":{"type":"string","description":"For chart: value field, or a comma list for several series."},
    "value":{"type":"number","description":"For gauge: the value."},
    "max":{"type":"number","description":"For gauge: the maximum; default 100."},
    "bins":{"type":"integer","minimum":1,"maximum":50,"description":"For histogram."},
    "width":{"type":"integer","minimum":10,"maximum":200,"description":"Columns; default 60."},
    "height":{"type":"integer","minimum":3,"maximum":60,"description":"Rows; default 12."},
    "title":{"type":"string","description":"Caption above the chart."},
    "out":{"type":"string","description":"For chart/diagram: write the result under the workspace."},
    "format":{"type":"string","enum":["text","svg","png"],"description":"For diagram: SVG/PNG file, or text in the result."},
    "source":{"type":"string","description":"For diagram: Mermaid source."},
    "surface":{"type":"string","description":"For dashboard_patch/close: the surface id from dashboard_open."},
    "tml":{"description":"For dashboard_open: a TML document as TML text or TML JSON."},
    "ops":{"description":"For dashboard_patch: an RFC 6902 JSON Patch array."},
    "target":{"type":"string","enum":["popup","pane"],"description":"For dashboard_open: where to show it; default pane."},
    "name":{"type":"string","description":"For dashboard_open: pane or popup name."}
  }
}`

type visualArgs struct {
	Action  string          `json:"action"`
	Kind    string          `json:"kind,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
	File    string          `json:"file,omitempty"`
	X       string          `json:"x,omitempty"`
	Y       string          `json:"y,omitempty"`
	Value   float64         `json:"value,omitempty"`
	Max     float64         `json:"max,omitempty"`
	Bins    int             `json:"bins,omitempty"`
	Width   int             `json:"width,omitempty"`
	Height  int             `json:"height,omitempty"`
	Title   string          `json:"title,omitempty"`
	Out     string          `json:"out,omitempty"`
	Format  string          `json:"format,omitempty"`
	Source  string          `json:"source,omitempty"`
	Surface string          `json:"surface,omitempty"`
	TML     json.RawMessage `json:"tml,omitempty"`
	Ops     json.RawMessage `json:"ops,omitempty"`
	Target  string          `json:"target,omitempty"`
	Name    string          `json:"name,omitempty"`
}

func (t *VisualTool) Schema() json.RawMessage {
	if !t.allowControl {
		if !hasTuiosBinary() {
			return trimActionEnum(visualReadSchema, []string{"chart"})
		}
		return json.RawMessage(visualReadSchema)
	}
	if !hasTuiosBinary() {
		return trimActionEnum(visualControlSchema, []string{"chart"})
	}
	return json.RawMessage(visualControlSchema)
}

// ValidationSchema keeps the registry's validator at the widest form, so a
// diagram or dashboard call validates even if tuios was installed after the
// session started.
func (t *VisualTool) ValidationSchema() json.RawMessage {
	if !t.allowControl {
		return json.RawMessage(visualReadSchema)
	}
	return json.RawMessage(visualControlSchema)
}

func (t *VisualTool) Execute(ctx context.Context, _ string, raw json.RawMessage, _ agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	var a visualArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return visualError(err), nil
		}
	}
	if !t.allowControl && a.Action != "chart" {
		return visualError(errors.New("only chart is available to sub-agents")), nil
	}
	switch a.Action {
	case "chart":
		return t.chart(a)
	case "diagram":
		return t.diagram(a)
	case "dashboard_open":
		return t.dashboardOpen(ctx, a)
	case "dashboard_patch":
		return t.dashboardPatch(ctx, a)
	case "dashboard_close":
		return t.dashboardClose(ctx, a)
	case "":
		return visualError(errors.New("action is required")), nil
	default:
		return visualError(fmt.Errorf("unknown action %q", a.Action)), nil
	}
}

func (t *VisualTool) chart(a visualArgs) (agentcore.AgentToolResult, error) {
	kind := a.Kind
	if kind == "" {
		kind = "bar"
	}
	width, height := a.Width, a.Height
	if width <= 0 {
		width = 60
	}
	if width > 200 {
		width = 200
	}
	if height <= 0 {
		height = 12
	}
	if height > 60 {
		height = 60
	}
	// A gauge is one number, not a series; asking it for data would reject the
	// shape the schema documents.
	if kind == "gauge" {
		text := renderGauge(a.Value, a.Max, width)
		if a.Title != "" {
			text = a.Title + "\n" + text
		}
		return agentcore.AgentToolResult{
			Content: agentcore.ContentList{agentcore.NewTextContent(text)},
			Details: map[string]any{"kind": kind},
		}, nil
	}
	labels, series, names, err := t.loadSeries(a)
	if err != nil {
		return visualError(err), nil
	}
	var text string
	switch kind {
	case "bar":
		text = renderBars(labels, series, names, width)
	case "line":
		text = renderLines(labels, series, names, width, height)
	case "sparkline":
		text = renderSparkline(series)
	case "histogram":
		text = renderHistogram(series, a.Bins, width)
	case "pie":
		text = renderPie(labels, series)
	case "gauge":
		text = renderGauge(a.Value, a.Max, width)
	case "heatmap":
		text = renderHeatmap(labels, series, names)
	default:
		return visualError(fmt.Errorf("unknown chart kind %q", kind)), nil
	}
	if a.Title != "" {
		text = a.Title + "\n" + text
	}
	out := ""
	if a.Out != "" {
		if out, err = artifactPath(t.cwd, "charts", a.Out, "txt"); err != nil {
			return visualError(err), nil
		}
		if err := os.WriteFile(out, []byte(text+"\n"), 0o644); err != nil {
			return visualError(err), nil
		}
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(text)},
		Details: map[string]any{"kind": kind, "series": len(series), "path": out},
	}, nil
}

// loadSeries reads inline data or a workspace file into labels and one or more
// numeric series. Numbers become a single series; row objects use x/y; CSV
// uses its header.
func (t *VisualTool) loadSeries(a visualArgs) ([]string, [][]float64, []string, error) {
	if strings.TrimSpace(a.File) != "" {
		path := a.File
		if !filepath.IsAbs(path) {
			path = filepath.Join(t.cwd, path)
		}
		path = filepath.Clean(path)
		if !insidePath(t.cwd, path) {
			return nil, nil, nil, fmt.Errorf("%s is outside the workspace", path)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, nil, err
		}
		if strings.HasSuffix(strings.ToLower(path), ".csv") {
			return parseCSVSeries(raw, a.X, a.Y)
		}
		return parseJSONSeries(raw, a.X, a.Y)
	}
	if len(a.Data) == 0 {
		return nil, nil, nil, errors.New("give data or file")
	}
	return parseJSONSeries(a.Data, a.X, a.Y)
}

func parseJSONSeries(raw []byte, xField, yFields string) ([]string, [][]float64, []string, error) {
	var numbers []float64
	if err := json.Unmarshal(raw, &numbers); err == nil {
		labels := make([]string, len(numbers))
		for i := range numbers {
			labels[i] = strconv.Itoa(i + 1)
		}
		return labels, [][]float64{numbers}, []string{"value"}, nil
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, nil, nil, fmt.Errorf("data must be an array of numbers or row objects: %w", err)
	}
	return seriesFromRows(rows, xField, yFields)
}

func parseCSVSeries(raw []byte, xField, yFields string) ([]string, [][]float64, []string, error) {
	r := csv.NewReader(strings.NewReader(string(raw)))
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil || len(records) < 2 {
		return nil, nil, nil, fmt.Errorf("CSV needs a header and at least one row: %v", err)
	}
	header := records[0]
	rows := make([]map[string]any, 0, len(records)-1)
	for _, rec := range records[1:] {
		row := map[string]any{}
		for i, name := range header {
			if i < len(rec) {
				row[name] = rec[i]
			}
		}
		rows = append(rows, row)
	}
	return seriesFromRows(rows, xField, yFields)
}

func seriesFromRows(rows []map[string]any, xField, yFields string) ([]string, [][]float64, []string, error) {
	if len(rows) == 0 {
		return nil, nil, nil, errors.New("no rows")
	}
	fields := []string{}
	for _, f := range strings.Split(yFields, ",") {
		if f = strings.TrimSpace(f); f != "" {
			fields = append(fields, f)
		}
	}
	if len(fields) == 0 {
		// Pick every numeric field except the label, in a stable order.
		keys := make([]string, 0, len(rows[0]))
		for k := range rows[0] {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if k == xField {
				continue
			}
			if _, ok := toFloat(rows[0][k]); ok {
				fields = append(fields, k)
			}
		}
	}
	if len(fields) == 0 {
		return nil, nil, nil, errors.New("no numeric field; name one with y")
	}
	labels := make([]string, 0, len(rows))
	series := make([][]float64, len(fields))
	for i, row := range rows {
		label := ""
		if xField != "" {
			label = fmt.Sprint(row[xField])
		} else {
			label = strconv.Itoa(i + 1)
		}
		labels = append(labels, label)
		for j, f := range fields {
			v, _ := toFloat(row[f])
			series[j] = append(series[j], v)
		}
	}
	return labels, series, fields, nil
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil
	}
	return 0, false
}

func renderBars(labels []string, series [][]float64, names []string, width int) string {
	var b strings.Builder
	barWidth := width - 24
	if barWidth < 10 {
		barWidth = 10
	}
	for i, label := range labels {
		for s := range series {
			v := 0.0
			if i < len(series[s]) {
				v = series[s][i]
			}
			n := int(v)
			if n > barWidth {
				n = barWidth
			}
			if n < 0 {
				n = 0
			}
			name := ""
			if len(names) > 1 {
				name = names[s] + " "
			}
			fmt.Fprintf(&b, "%-12s %s%s %g\n", label, name, strings.Repeat("█", n), v)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderLines(labels []string, series [][]float64, names []string, width, height int) string {
	all := []float64{}
	for _, s := range series {
		all = append(all, s...)
	}
	if len(all) == 0 {
		return "no data"
	}
	minV, maxV := all[0], all[0]
	for _, v := range all {
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}
	if maxV == minV {
		maxV = minV + 1
	}
	cols := width - 10
	if cols < 10 {
		cols = 10
	}
	grid := make([][]rune, height)
	for y := range grid {
		grid[y] = make([]rune, cols)
		for x := range grid[y] {
			grid[y][x] = ' '
		}
	}
	marks := []rune{'●', '○', '◆', '◇'}
	for s, vals := range series {
		mark := marks[s%len(marks)]
		for i, v := range vals {
			x := 0
			if len(vals) > 1 {
				x = i * (cols - 1) / (len(vals) - 1)
			}
			y := height - 1 - int((v-minV)/(maxV-minV)*float64(height-1))
			if y < 0 {
				y = 0
			}
			if y >= height {
				y = height - 1
			}
			grid[y][x] = mark
		}
	}
	var b strings.Builder
	for y := range grid {
		label := ""
		if y == 0 {
			label = fmt.Sprintf("%6.4g", maxV)
		} else if y == height-1 {
			label = fmt.Sprintf("%6.4g", minV)
		}
		fmt.Fprintf(&b, "%7s │%s\n", label, string(grid[y]))
	}
	fmt.Fprintf(&b, "%7s └%s\n", "", strings.Repeat("─", cols))
	if len(names) > 0 {
		fmt.Fprintf(&b, "        %s\n", strings.Join(names, ", "))
	}
	if len(labels) > 0 {
		fmt.Fprintf(&b, "        x: %s … %s\n", labels[0], labels[len(labels)-1])
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderSparkline(series [][]float64) string {
	marks := []rune("▁▂▃▄▅▆▇█")
	var b strings.Builder
	for _, vals := range series {
		if len(vals) == 0 {
			continue
		}
		minV, maxV := vals[0], vals[0]
		for _, v := range vals {
			if v < minV {
				minV = v
			}
			if v > maxV {
				maxV = v
			}
		}
		if maxV == minV {
			maxV = minV + 1
		}
		for _, v := range vals {
			idx := int((v - minV) / (maxV - minV) * float64(len(marks)-1))
			if idx < 0 {
				idx = 0
			}
			if idx >= len(marks) {
				idx = len(marks) - 1
			}
			b.WriteRune(marks[idx])
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderHistogram(series [][]float64, bins, width int) string {
	if len(series) == 0 || len(series[0]) == 0 {
		return "no data"
	}
	vals := series[0]
	if bins <= 0 {
		bins = 10
	}
	if bins > 50 {
		bins = 50
	}
	minV, maxV := vals[0], vals[0]
	for _, v := range vals {
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}
	if maxV == minV {
		maxV = minV + 1
	}
	counts := make([]int, bins)
	for _, v := range vals {
		i := int((v - minV) / (maxV - minV) * float64(bins))
		if i >= bins {
			i = bins - 1
		}
		counts[i]++
	}
	peak := 1
	for _, c := range counts {
		if c > peak {
			peak = c
		}
	}
	barWidth := width - 28
	if barWidth < 10 {
		barWidth = 10
	}
	var b strings.Builder
	for i, c := range counts {
		lo := minV + (maxV-minV)*float64(i)/float64(bins)
		hi := minV + (maxV-minV)*float64(i+1)/float64(bins)
		n := c * barWidth / peak
		fmt.Fprintf(&b, "%7.3g–%-7.3g %s %d\n", lo, hi, strings.Repeat("█", n), c)
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderPie(labels []string, series [][]float64) string {
	if len(series) == 0 {
		return "no data"
	}
	vals := series[0]
	total := 0.0
	for _, v := range vals {
		total += v
	}
	if total == 0 {
		return "no data"
	}
	var b strings.Builder
	for i, v := range vals {
		label := strconv.Itoa(i + 1)
		if i < len(labels) {
			label = labels[i]
		}
		pct := v / total * 100
		barWidth := 30
		n := int(pct / 100 * float64(barWidth))
		fmt.Fprintf(&b, "%-12s %s %5.1f%%\n", label, strings.Repeat("█", n), pct)
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderGauge(value, max float64, width int) string {
	if max <= 0 {
		max = 100
	}
	barWidth := width / 2
	if barWidth < 10 {
		barWidth = 10
	}
	frac := value / max
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	n := int(frac * float64(barWidth))
	return fmt.Sprintf("[%s%s] %g/%g (%.0f%%)", strings.Repeat("█", n), strings.Repeat("░", barWidth-n), value, max, frac*100)
}

func renderHeatmap(labels []string, series [][]float64, names []string) string {
	if len(series) == 0 {
		return "no data"
	}
	shades := []rune(" ░▒▓█")
	all := []float64{}
	for _, s := range series {
		all = append(all, s...)
	}
	if len(all) == 0 {
		return "no data"
	}
	minV, maxV := all[0], all[0]
	for _, v := range all {
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}
	if maxV == minV {
		maxV = minV + 1
	}
	var b strings.Builder
	for s, vals := range series {
		name := ""
		if s < len(names) {
			name = names[s]
		}
		fmt.Fprintf(&b, "%-10s ", name)
		for _, v := range vals {
			idx := int((v - minV) / (maxV - minV) * float64(len(shades)-1))
			b.WriteRune(shades[idx])
		}
		b.WriteByte('\n')
	}
	if len(labels) > 0 {
		fmt.Fprintf(&b, "%-10s %s … %s\n", "", labels[0], labels[len(labels)-1])
	}
	return strings.TrimRight(b.String(), "\n")
}

// diagram renders Mermaid source through the installed tuios (sketch).
func (t *VisualTool) diagram(a visualArgs) (agentcore.AgentToolResult, error) {
	source := strings.TrimSpace(a.Source)
	if source == "" {
		return visualError(errors.New("diagram needs source")), nil
	}
	bin, err := exec.LookPath("tuios")
	if err != nil {
		return visualError(errors.New("tuios is not installed; diagrams need it (brew install tuios) or use browser/websearch for a hosted renderer")), nil
	}
	format := a.Format
	if format == "" {
		format = "text"
	}
	if format != "text" && format != "svg" && format != "png" {
		return visualError(fmt.Errorf("unknown diagram format %q", format)), nil
	}
	src, err := artifactPath(t.cwd, "diagrams", "", "mmd")
	if err != nil {
		return visualError(err), nil
	}
	if err := os.WriteFile(src, []byte(source), 0o644); err != nil {
		return visualError(err), nil
	}
	out := ""
	args := []string{"mermaid", src, "-f", format}
	if format != "text" {
		out, err = artifactPath(t.cwd, "diagrams", a.Out, format)
		if err != nil {
			return visualError(err), nil
		}
		args = append(args, "-o", out)
	}
	cmd := exec.CommandContext(context.Background(), bin, args...)
	cmd.Env = append(os.Environ(), "TUIOS_NO_ALT_SCREEN=1")
	text, err := cmd.CombinedOutput()
	if err != nil {
		return visualError(fmt.Errorf("tuios mermaid: %v: %s", err, strings.TrimSpace(string(text)))), nil
	}
	if format == "text" {
		return agentcore.AgentToolResult{
			Content: agentcore.ContentList{agentcore.NewTextContent(strings.TrimRight(string(text), "\n"))},
			Details: map[string]any{"format": "text", "source": src},
		}, nil
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(tuiosJSON(map[string]any{
			"path": out, "format": format, "source": src,
			"hint": "view_image or open this path to inspect the diagram",
		}))},
		Details: map[string]any{"path": out, "format": format},
	}, nil
}

func (t *VisualTool) client(ctx context.Context) (*tuiosctl.Client, error) {
	if t.newClient != nil {
		return t.newClient(ctx)
	}
	return tuiosctl.Dial(ctx, tuiosctl.FromEnv())
}

func (t *VisualTool) dashboardOpen(ctx context.Context, a visualArgs) (agentcore.AgentToolResult, error) {
	if len(a.TML) == 0 {
		return visualError(errors.New("dashboard_open needs tml")), nil
	}
	c, err := t.client(ctx)
	if err != nil {
		return visualError(err), nil
	}
	defer c.Close()
	target := a.Target
	if target == "" {
		target = "pane"
	}
	var doc any = json.RawMessage(a.TML)
	var asString string
	if json.Unmarshal(a.TML, &asString) == nil {
		doc = asString
	}
	res, err := c.TMLOpen(ctx, tuiosctl.TMLOpenParams{Doc: doc, Target: target, Name: a.Name})
	if err != nil {
		return visualError(err), nil
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(tuiosJSON(map[string]any{
			"surface": res.Surface, "window_id": res.WindowID, "target": res.Target,
		}))},
		Details: map[string]any{"surface": res.Surface, "window_id": res.WindowID},
	}, nil
}

func (t *VisualTool) dashboardPatch(ctx context.Context, a visualArgs) (agentcore.AgentToolResult, error) {
	if a.Surface == "" {
		return visualError(errors.New("dashboard_patch needs surface")), nil
	}
	if len(a.Ops) == 0 {
		return visualError(errors.New("dashboard_patch needs ops")), nil
	}
	c, err := t.client(ctx)
	if err != nil {
		return visualError(err), nil
	}
	defer c.Close()
	res, err := c.TMLPatch(ctx, a.Surface, a.Ops)
	if err != nil {
		return visualError(err), nil
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(tuiosJSON(res))},
		Details: map[string]any{"surface": a.Surface, "version": res.Version},
	}, nil
}

func (t *VisualTool) dashboardClose(ctx context.Context, a visualArgs) (agentcore.AgentToolResult, error) {
	if a.Surface == "" {
		return visualError(errors.New("dashboard_close needs surface")), nil
	}
	c, err := t.client(ctx)
	if err != nil {
		return visualError(err), nil
	}
	defer c.Close()
	if err := c.TMLClose(ctx, a.Surface); err != nil {
		return visualError(err), nil
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(tuiosJSON(map[string]any{"closed": a.Surface}))},
		Details: map[string]any{"surface": a.Surface},
	}, nil
}

func visualError(err error) agentcore.AgentToolResult {
	return errorResult("visual: " + err.Error())
}
