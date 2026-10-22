package agenttool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

func runVisual(t *testing.T, tool *VisualTool, raw string) agentcore.AgentToolResult {
	t.Helper()
	res, err := tool.Execute(context.Background(), "1", json.RawMessage(raw), nil)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// A bar chart from inline numbers renders in the result, so the model reads
// the data immediately without a file or a pane.
func TestVisualChartRendersInlineNumbers(t *testing.T) {
	tool := NewVisualTool(t.TempDir(), true)
	res := runVisual(t, tool, `{"action":"chart","kind":"bar","data":[3,1,5],"title":"counts"}`)
	if res.IsError {
		t.Fatalf("chart failed: %s", agentcore.ContentToText(res.Content))
	}
	text := agentcore.ContentToText(res.Content)
	if !strings.Contains(text, "counts") {
		t.Errorf("chart = %q, want the title", text)
	}
	if !strings.Contains(text, "███") {
		t.Errorf("chart = %q, want bars for the values", text)
	}
}

// A CSV in the workspace is the data-analysis path: header names select the
// label and the value column.
func TestVisualChartReadsCSV(t *testing.T) {
	root := t.TempDir()
	csv := "month,revenue\nJan,10\nFeb,30\nMar,20\n"
	if err := os.WriteFile(filepath.Join(root, "sales.csv"), []byte(csv), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewVisualTool(root, true)
	res := runVisual(t, tool, `{"action":"chart","kind":"bar","file":"sales.csv","x":"month","y":"revenue"}`)
	if res.IsError {
		t.Fatalf("chart failed: %s", agentcore.ContentToText(res.Content))
	}
	text := agentcore.ContentToText(res.Content)
	for _, want := range []string{"Jan", "Feb", "Mar", "30"} {
		if !strings.Contains(text, want) {
			t.Errorf("chart = %q, want %q", text, want)
		}
	}
}

// A file outside the workspace is refused before it is read.
func TestVisualChartRefusesOutsideFile(t *testing.T) {
	tool := NewVisualTool(t.TempDir(), true)
	res := runVisual(t, tool, `{"action":"chart","kind":"bar","file":"/etc/passwd"}`)
	if !res.IsError {
		t.Fatalf("chart outside the workspace must be refused, got %s", agentcore.ContentToText(res.Content))
	}
}

// A gauge and a sparkline are the two one-line shapes.
func TestVisualChartGaugeAndSparkline(t *testing.T) {
	tool := NewVisualTool(t.TempDir(), true)
	res := runVisual(t, tool, `{"action":"chart","kind":"gauge","value":42,"max":100}`)
	if res.IsError {
		t.Fatalf("gauge failed: %s", agentcore.ContentToText(res.Content))
	}
	g := agentcore.ContentToText(res.Content)
	if !strings.Contains(g, "42/100") || !strings.Contains(g, "█") {
		t.Errorf("gauge = %q, want a filled bar and the value", g)
	}
	res = runVisual(t, tool, `{"action":"chart","kind":"sparkline","data":[1,2,3,4]}`)
	if res.IsError {
		t.Fatalf("sparkline failed: %s", agentcore.ContentToText(res.Content))
	}
	if s := agentcore.ContentToText(res.Content); !strings.ContainsAny(s, "▁▂▃▄▅▆▇█") {
		t.Errorf("sparkline = %q, want block glyphs", s)
	}
}

// A sub-agent may chart, and nothing else.
func TestVisualSubAgentOnlyCharts(t *testing.T) {
	tool := NewVisualTool(t.TempDir(), false)
	if strings.Contains(string(tool.Schema()), `"dashboard_open"`) {
		t.Error("the read schema must not advertise dashboards")
	}
	res := runVisual(t, tool, `{"action":"diagram","source":"graph TD; A-->B"}`)
	if !res.IsError {
		t.Fatal("a sub-agent diagram must be refused")
	}
	res = runVisual(t, tool, `{"action":"chart","kind":"bar","data":[1,2]}`)
	if res.IsError {
		t.Fatalf("a sub-agent chart should work: %s", agentcore.ContentToText(res.Content))
	}
}

// The chart writes a text artifact when asked, and refuses a path outside.
func TestVisualChartWritesArtifactInsideWorkspace(t *testing.T) {
	root := t.TempDir()
	tool := NewVisualTool(root, true)
	res := runVisual(t, tool, `{"action":"chart","kind":"bar","data":[1,2],"out":"artifacts/chart.txt"}`)
	if res.IsError {
		t.Fatalf("chart failed: %s", agentcore.ContentToText(res.Content))
	}
	if _, err := os.Stat(filepath.Join(root, "artifacts", "chart.txt")); err != nil {
		t.Fatalf("chart artifact missing: %v", err)
	}
	res = runVisual(t, tool, `{"action":"chart","kind":"bar","data":[1,2],"out":"/tmp/x.txt"}`)
	if !res.IsError {
		t.Fatal("a chart artifact outside the workspace must be refused")
	}
}
