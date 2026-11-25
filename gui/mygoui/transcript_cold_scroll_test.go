package mygoui

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getan/golder/gui/appsession"
)

func coldBashApp() *App {
	a := testApp()
	a.status.SessionID = "cold-scroll"
	for i := range 240 {
		id := fmt.Sprintf("bash-%03d", i)
		a.items = append(a.items, appsession.Item{
			ID: id, Kind: appsession.ItemTool, Done: true,
			Tool: &appsession.ToolState{
				ID: "call-" + id, Name: "bash",
				Input: map[string]any{"command": fmt.Sprintf("echo synthetic-%03d", i)}, Output: "ok",
			},
		})
		a.index[id] = len(a.items) - 1
	}
	a.list = nil
	a.transcriptRowsDirty = true
	return a
}

func coldTitle(idx int) string {
	return fmt.Sprintf("bash · echo synthetic-%03d · done", idx)
}

func scrollMetrics(t *testing.T, a *App, tt *ui.Tester, label string, rows int, titleAt func(int) string) (steps, anomalies int, worstReverse, worstSettled float32) {
	t.Helper()
	scroll := func() {
		tt.Scroll(1150, 200, 0, -48)
		tt.Frame()
	}
	scroll()
	if a.list.follow {
		t.Fatalf("%s: first upward scroll did not leave follow-mode", label)
	}
	for ; steps < 140; steps++ {
		first, last := a.list.Visible()
		var title string
		var before ui.Rect
		found := false
		if key := a.list.anchorKey; key != "" {
			if idx, ok := a.list.index[key]; ok && idx >= 0 && idx < rows {
				title = titleAt(idx)
				if before, found = tt.Find(title); found {
				}
			}
		}
		if !found {
			for _, idx := range []int{(first + last) / 2, first + 1, last - 1} {
				if idx < first || idx > last || idx < 0 || idx >= rows {
					continue
				}
				title = titleAt(idx)
				if before, found = tt.Find(title); found {
					break
				}
			}
		}
		if !found {
			break
		}
		scroll()
		after, ok := tt.Find(title)
		if !ok {
			continue
		}
		delta := after.Y - before.Y
		reverse := max(-delta, 0)
		settled := abs32(delta - 48)
		if reverse > 1 || settled > 1 {
			anomalies++
			worstReverse = max(worstReverse, reverse)
			worstSettled = max(worstSettled, settled)
		}
	}
	t.Logf("%s: steps=%d anomalies=%d reverse=%.2f settled=%.2f", label, steps, anomalies, worstReverse, worstSettled)
	return steps, anomalies, worstReverse, worstSettled
}

func TestColdBashScrollPosition(t *testing.T) {
	a := coldBashApp()
	tt := ui.NewTester(a.view, 1200, 600)
	tt.Frame()
	_, coldAnomalies, coldReverse, coldSettled := scrollMetrics(t, a, tt, "cold", 240, coldTitle)
	a.list.jumpToBottom()
	tt.Frame()
	_, warmAnomalies, warmReverse, warmSettled := scrollMetrics(t, a, tt, "warm", 240, coldTitle)
	if coldAnomalies != 0 || warmAnomalies != 0 || coldReverse > 1 || coldSettled > 1 || warmReverse > 1 || warmSettled > 1 {
		t.Fatalf("scroll jumped: cold anomalies=%d reverse=%.2f settled=%.2f; warm anomalies=%d reverse=%.2f settled=%.2f",
			coldAnomalies, coldReverse, coldSettled, warmAnomalies, warmReverse, warmSettled)
	}
}

func TestTranscriptWindowMixedHeightsKeepAnchor(t *testing.T) {
	s := &transcriptWindow{}
	s.sync([]transcriptRow{{key: "bash"}, {key: "markdown"}, {key: "tool"}, {key: "tail"}}, false)
	s.started, s.follow, s.first, s.last, s.scroll.Y = true, false, 2, 3, 1000
	s.anchorKey = s.keys[2]
	s.anchorOffset = 1000 - transcriptPadding - s.prefix(2)
	s.anchorValid = true
	before := s.scroll.Y
	if !s.measure(0, 24) {
		t.Fatal("first mixed-height measurement was not applied")
	}
	if !s.measure(1, 180) {
		t.Fatal("second mixed-height measurement was not applied")
	}
	if s.scroll.Y == before {
		t.Fatal("mixed row heights did not move the anchor")
	}
}

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}
