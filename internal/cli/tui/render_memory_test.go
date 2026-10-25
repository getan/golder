package tui

import (
	"fmt"
	"strings"
	"testing"
	"unsafe"

	"github.com/charmbracelet/glamour"
)

func freshRenderCaches(t *testing.T) {
	t.Helper()
	diffRenderMu.Lock()
	oldDiff, oldDiffBytes := diffRenderCache, diffCachedBytes
	diffRenderCache, diffCachedBytes = map[string]string{}, 0
	diffRenderMu.Unlock()
	cmdHLMu.Lock()
	oldHL, oldHLBytes, oldLexers := cmdHLCache, cmdHLCachedBytes, lexerMatchCache
	cmdHLCache, cmdHLCachedBytes, lexerMatchCache = map[string][]hlSpan{}, 0, map[string]string{}
	cmdHLMu.Unlock()
	mdMu.Lock()
	oldMD := mdCache
	mdCache = map[int]*glamour.TermRenderer{}
	mdMu.Unlock()
	t.Cleanup(func() {
		diffRenderMu.Lock()
		diffRenderCache, diffCachedBytes = oldDiff, oldDiffBytes
		diffRenderMu.Unlock()
		cmdHLMu.Lock()
		cmdHLCache, cmdHLCachedBytes, lexerMatchCache = oldHL, oldHLBytes, oldLexers
		cmdHLMu.Unlock()
		mdMu.Lock()
		mdCache = oldMD
		mdMu.Unlock()
	})
}

func TestCollapsedCardReleasesExpandedRenderBuffers(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(100, 24)
	card := &toolCard{id: "memory-output", name: "bash", state: cardSuccess, expanded: true}
	card.complete(true, strings.Repeat("diagnostic output\n", 6000)+"last row is still available", nil)
	tr.addToolCard(card)
	if len(tr.contentLines()) < 6000 {
		t.Fatal("fixture never rendered a large expanded card")
	}
	tr.toggleCardExpanded(card)
	tr.reflow()
	for name, lines := range map[string][]string{"prefix": tr.prefixLines, "viewport": tr.lines} {
		for _, stale := range lines[len(lines):cap(lines)] {
			if stale != "" {
				t.Fatalf("%s still retains discarded expanded lines beyond its length", name)
			}
		}
		if cap(lines) > 4096 {
			t.Fatalf("%s still retains a huge pointer array after collapsing: %d", name, cap(lines))
		}
	}
	// Only derived renders were dropped: reopening the card is lossless.
	tr.toggleCardExpanded(card)
	tr.reflow()
	if !strings.Contains(strings.Join(tr.contentLines(), "\n"), "last row is still available") {
		t.Fatal("memory trimming lost the original tool result")
	}
}

func TestDiffRenderCacheHasAByteBudget(t *testing.T) {
	freshRenderCaches(t)
	var lastDiff, lastOut string
	for i := 0; i < diffRenderCacheMax; i++ {
		lastDiff = fmt.Sprintf("--- a/f%d.go\n+++ b/f%d.go\n@@ -0,0 +1,1000 @@\n", i, i) +
			strings.Repeat("+var value = 1\n", 1000)
		lastOut = renderDiff(DefaultTheme(), lastDiff, 120)
	}
	diffRenderMu.Lock()
	bytes := 0
	for k, v := range diffRenderCache {
		bytes += len(k) + len(v)
	}
	accounted := diffCachedBytes
	diffRenderMu.Unlock()
	if bytes != accounted || bytes > diffRenderCacheBytes {
		t.Fatalf("diff cache retained %d bytes (accounted %d), budget %d", bytes, accounted, diffRenderCacheBytes)
	}
	if out := renderDiff(DefaultTheme(), lastDiff, 120); out != lastOut {
		t.Fatal("a cache hit changed the rendered diff")
	}
	big := "--- a/large.go\n+++ b/large.go\n@@ -0,0 +1,512 @@\n" + strings.Repeat("+var value = 1\n", 512)
	out := renderDiff(DefaultTheme(), big, 2048)
	if len(out) <= maxCachedDiffBytes || !strings.Contains(out, "value") {
		t.Fatal("oversize fixture was not rendered in full")
	}
	diffRenderMu.Lock()
	_, cached := diffRenderCache[diffRenderKey(big, 2048, syntaxDark())]
	diffRenderMu.Unlock()
	if cached {
		t.Fatal("oversize diff was retained in the global cache")
	}
}

func TestHighlightCacheOwnsItsTextAndBoundsMemory(t *testing.T) {
	freshRenderCaches(t)
	// A short diff line borrowed from a huge result must not pin that result.
	source := strings.Repeat("x", 4<<20) + "short borrowed line"
	text := source[len(source)-len("short borrowed line"):]
	spans := highlightCached("missing-lexer-for-memory-test", text, true)
	cmdHLMu.Lock()
	var key string
	for k := range cmdHLCache {
		key = k
	}
	cmdHLMu.Unlock()
	if len(spans) != 1 || spans[0].text != text || key == "" {
		t.Fatal("plain-text highlight fixture failed")
	}
	owned := key[len(key)-len(text):]
	if unsafe.StringData(spans[0].text) != unsafe.StringData(owned) ||
		unsafe.StringData(spans[0].text) == unsafe.StringData(text) {
		t.Fatal("cached token still refers to the original large result rather than its owned key")
	}
	for i := 0; i < 40; i++ {
		_ = highlightCached("missing-lexer-for-memory-test", fmt.Sprint(i)+strings.Repeat("x", 60<<10), true)
	}
	cmdHLMu.Lock()
	bytes := cmdHLCachedBytes
	cmdHLMu.Unlock()
	if bytes > cmdHLCacheBytes {
		t.Fatalf("highlight cache retained %d bytes, budget %d", bytes, cmdHLCacheBytes)
	}
	big := strings.Repeat("z", cmdHLCacheBytes)
	got := highlightCached("missing-lexer-for-memory-test", big, true)
	if len(got) != 1 || got[0].text != big {
		t.Fatal("an oversized uncached highlight lost text")
	}
}

func TestMarkdownRendererCacheBoundsResizeHistory(t *testing.T) {
	freshRenderCaches(t)
	for width := 40; width < 100; width++ {
		if rendererFor(width) == nil {
			t.Fatalf("cannot build renderer for width %d", width)
		}
		mdMu.Lock()
		n := len(mdCache)
		mdMu.Unlock()
		if n > 4 {
			t.Fatalf("renderer cache grows with every resize: %d entries", n)
		}
	}
}
