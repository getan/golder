package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/extensions"
)

// The status row answers "can this be used now". A failed attempt belongs in
// its own history line and must not repaint the status or replace its text.
func TestExtensionRowShowsCurrentStateAndHistorySeparately(t *testing.T) {
	at := time.Date(2026, 11, 18, 14, 32, 0, 0, time.Local)
	row := extensionSourceRow(extensions.Snapshot{
		Ref: extensions.SourceRef{Kind: "mcp", ID: "lark"}, Type: "MCP · stdio",
		State: "on demand", Trust: "approved", Busy: true,
		LastError: "boom", LastErrorScope: "call", LastErrorAt: &at,
	})
	if row.Status != "on demand · in use now" {
		t.Errorf("status = %q, want the current readiness plus the live lock", row.Status)
	}
	if row.StatusTone != pickOK {
		t.Errorf("tone = %v, want OK: a past failure is history, not the current state", row.StatusTone)
	}
	last := ""
	for _, line := range row.Info {
		if line.Label == "Last attempt" {
			last = line.Value
		}
	}
	for _, want := range []string{"2026-11-18 14:32", "call: boom"} {
		if !strings.Contains(last, want) {
			t.Errorf("last attempt = %q, want it to contain %q", last, want)
		}
	}
}
