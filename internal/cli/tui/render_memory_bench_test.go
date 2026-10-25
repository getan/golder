package tui

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
)

// Keep the transcript alive across the collection: this measures retained
// render memory, not the temporary allocation count of painting a frame.
func BenchmarkCollapsedToolRetainedHeap(b *testing.B) {
	for i := 0; i < b.N; i++ {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		tr := newTranscript(DefaultTheme())
		tr.setSize(120, 32)
		card := &toolCard{id: "large-output", name: "bash", state: cardSuccess, expanded: true}
		card.complete(true, strings.Repeat("diagnostic: "+strings.Repeat("x", 140)+"\n", 20000), nil)
		tr.addToolCard(card)
		tr.toggleCardExpanded(card)
		tr.reflow()
		runtime.GC()
		runtime.ReadMemStats(&after)
		b.ReportMetric(float64(after.HeapAlloc-before.HeapAlloc), "retained-B")
		runtime.KeepAlive(tr)
	}
}

func BenchmarkDiffCacheRetainedBytes(b *testing.B) {
	for i := 0; i < b.N; i++ {
		diffRenderMu.Lock()
		clear(diffRenderCache)
		diffCachedBytes = 0
		diffRenderMu.Unlock()
		for j := 0; j < diffRenderCacheMax; j++ {
			diff := fmt.Sprintf("--- a/file%d.go\n+++ b/file%d.go\n@@ -0,0 +1,1000 @@\n", j, j) +
				strings.Repeat("+var value = 1\n", 1000)
			_ = renderDiff(DefaultTheme(), diff, 120)
		}
		diffRenderMu.Lock()
		bytes := 0
		for key, value := range diffRenderCache {
			bytes += len(key) + len(value)
		}
		diffRenderMu.Unlock()
		b.ReportMetric(float64(bytes), "cached-B")
	}
}
