package tui

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/getan/golder/internal/redact"
)

// Mobile snapshots are regenerated on the UI loop. Bound both the content
// copied to a snapshot and the source inspected while building it, independently
// of the WebSocket's final encoded-size limit.
const (
	mobileToolSectionBytes = 8 << 10
	mobileToolScanBytes    = 64 << 10
	mobileToolOutputBytes  = 48 << 10
	mobileToolTotalScan    = 256 << 10
)

type mobileToolBudget struct {
	output   int
	scan     int
	redactor *redact.Redactor
}

func newMobileToolBudget() *mobileToolBudget {
	return &mobileToolBudget{output: mobileToolOutputBytes, scan: mobileToolTotalScan, redactor: redact.Default()}
}

// The TUI already publishes snapshots on every update, including animation
// ticks. Cache the bounded projections so unchanged cards do not repeatedly
// scan and allocate their output on the UI loop. Card completion replaces its
// response slice; its nodes are not modified in place by the transcript.
type mobileToolCache struct {
	seq     uint64
	entries map[*toolCard]mobileToolCacheEntry
}

type mobileToolCacheEntry struct {
	name, diff               string
	state                    cardState
	response                 []respNode
	redactor                 *redact.Redactor
	revision, seen           uint64
	outputBefore, scanBefore int
	outputAfter, scanAfter   int
	text                     string
}

func (c *mobileToolCache) begin() {
	c.seq++
	if c.seq == 0 {
		clear(c.entries)
		c.seq++
	}
	if c.entries == nil {
		c.entries = make(map[*toolCard]mobileToolCacheEntry)
	}
}

func (c *mobileToolCache) prune() {
	for card, entry := range c.entries {
		if entry.seen != c.seq {
			delete(c.entries, card)
		}
	}
}

func (c *mobileToolCache) project(card *toolCard, budget *mobileToolBudget) string {
	if c == nil {
		return budget.mobileToolText(card)
	}
	revision := budget.redactor.Revision()
	entry, ok := c.entries[card]
	sameResponse := len(entry.response) == len(card.response) && (len(card.response) == 0 || &entry.response[0] == &card.response[0])
	if ok && entry.name == card.name && entry.diff == card.diff && entry.state == card.state && sameResponse &&
		entry.redactor == budget.redactor && entry.revision == revision && entry.outputBefore == budget.output && entry.scanBefore == budget.scan {
		entry.seen = c.seq
		c.entries[card] = entry
		budget.output, budget.scan = entry.outputAfter, entry.scanAfter
		return entry.text
	}
	outputBefore, scanBefore := budget.output, budget.scan
	text := budget.mobileToolText(card)
	if budget.redactor.Revision() != revision {
		// Never cache or publish a projection made across a policy change.
		budget.output, budget.scan = outputBefore, scanBefore
		delete(c.entries, card)
		return "[Tool output temporarily unavailable: redaction policy changed.]"
	}
	c.entries[card] = mobileToolCacheEntry{
		name: card.name, diff: card.diff, state: card.state, response: card.response,
		redactor: budget.redactor, revision: revision, seen: c.seq,
		outputBefore: outputBefore, scanBefore: scanBefore, outputAfter: budget.output, scanAfter: budget.scan, text: text,
	}
	return text
}

// mobileToolText projects only the tool name, lifecycle, textual output and
// diff. In particular it never serializes input arguments or arbitrary Details.
// A user-invoked !shell is not an agent tool: its local output stays on the Mac.
func (b *mobileToolBudget) mobileToolText(card *toolCard) string {
	name, _ := mobilePrefix(card.name, 256)
	name = strings.Join(strings.Fields(mobilePlainText(name)), " ")
	if name == "" {
		name = "tool"
	}
	state := "running"
	switch card.state {
	case cardSuccess:
		state = "completed"
	case cardWarn:
		state = "failed or interrupted"
	}
	var text strings.Builder
	fmt.Fprintf(&text, "%s · %s", name, state)
	if card.isUserShell() {
		text.WriteString("\n[Local shell output remains on the Mac.]")
		return text.String()
	}
	if len(card.response) > 0 {
		text.WriteString("\n\nResult:\n")
		if b.scan <= 0 || b.output <= 0 {
			text.WriteString("[Result omitted: mobile snapshot limit.]")
		} else {
			raw, truncated := mobileResponseText(card.response, min(mobileToolScanBytes, b.scan))
			text.WriteString(b.section(card.name, "Result", raw, truncated))
		}
	}
	if card.diff != "" {
		text.WriteString("\n\nDiff:\n")
		if b.scan <= 0 || b.output <= 0 {
			text.WriteString("[Diff omitted: mobile snapshot limit.]")
		} else {
			raw, truncated := mobilePrefix(card.diff, min(mobileToolScanBytes, b.scan))
			text.WriteString(b.section(card.name, "Diff", raw, truncated))
		}
	}
	if len(card.response) == 0 && card.diff == "" && card.state != cardRunning {
		text.WriteString("\n(no output)")
	}
	return text.String()
}

func (b *mobileToolBudget) section(tool, label, raw string, sourceTruncated bool) string {
	b.scan -= len(raw)
	// Do not scan only a prefix when redaction is configured: a credential or
	// private-key block could cross the scan boundary and evade detection.
	if sourceTruncated && b.redactor != nil {
		return fmt.Sprintf("[%s omitted: too large to safely redact; view it on the Mac.]", label)
	}
	text := mobilePlainText(raw)
	if b.redactor != nil {
		text = b.redactor.PreviewText(tool, text)
	}
	text, clipped := mobilePrefix(text, min(mobileToolSectionBytes, b.output))
	b.output -= len(text)
	if clipped || sourceTruncated {
		text += fmt.Sprintf("\n[%s truncated; view the full content on the Mac.]", label)
	}
	return text
}

// mobileResponseText reconstructs the plain response tree without ever
// materializing an unbounded tool result or a pathological indentation level.
func mobileResponseText(nodes []respNode, limit int) (string, bool) {
	var out strings.Builder
	appendPart := func(part string) bool {
		prefix, clipped := mobilePrefix(part, max(limit-out.Len(), 0))
		out.WriteString(prefix)
		return clipped
	}
	for i, node := range nodes {
		if i > 0 && appendPart("\n") {
			return out.String(), true
		}
		depth := max(node.depth, 0)
		if depth > (limit-out.Len())/2 {
			return out.String(), true
		}
		if appendPart(strings.Repeat("  ", depth)) || appendPart(node.text) {
			return out.String(), true
		}
	}
	return out.String(), false
}

// mobilePrefix cuts only at UTF-8 boundaries; invalid input is normalized by
// mobilePlainText before it is published.
func mobilePrefix(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	end := max(limit, 0)
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end], true
}

// Tool output is untrusted. Render plain text rather than terminal escape
// sequences, and do so before redaction so escapes cannot split a secret.
func mobilePlainText(s string) string {
	s = ansi.Strip(strings.ToValidUTF8(s, "�"))
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
}
