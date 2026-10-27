package tui

// This file pins the display half of the /resume contract: a session switched
// mid-flight keeps this process's live run configuration (its recorded
// Model/Provider are history, not a configuration to adopt — see
// runSession.switchTo), and every surface that shows that configuration — the
// status bar's provider/model blocks, its context gauge, and the splash panel —
// agrees with it and with /status.
//
// The mismatch it covers: the bar and the redrawn banner used to keep printing
// the launch provider/model while /status read the live config, so the two
// surfaces disagreed about the conversation in the pane.

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/cli/status"
	"github.com/getan/golder/internal/session"
)

func TestResumeKeepsLiveConfigEverywhere(t *testing.T) {
	store := newTestStore(t)
	// The target was created on another gateway/model: resuming it must not move
	// this process onto them.
	target := saveSessionWith(t, store, "gpt-4o", "openai", agentcore.MessageList{userMsg("carried over")})

	live := &cli.LiveConfig{
		Model:         "claude-sonnet-4-5",
		ProviderName:  "anthropic",
		ThinkingLevel: agentcore.ThinkingHigh,
		ContextWindow: 200000,
	}
	now := time.Now().UTC()
	m := NewModel(Options{Model: "claude-sonnet-4-5", ProviderName: "anthropic", ThinkingLevel: agentcore.ThinkingHigh})
	m.live = live
	m.session = &runSession{
		store:    store,
		header:   session.SessionHeader{ID: "current", CreatedAt: now, UpdatedAt: now, Model: "claude-sonnet-4-5", Provider: "anthropic"},
		agentCtx: &agentcore.AgentContext{},
		live:     live,
	}

	got, load := m.runSlash("/resume " + target)
	gm := got.(Model)
	if load == nil {
		t.Fatal("resume returned no background load")
	}
	gm = apply(t, gm, load())

	if gm.live.ProviderName != "anthropic" || gm.live.Model != "claude-sonnet-4-5" {
		t.Fatalf("live = (%q, %q), want it untouched at (anthropic, claude-sonnet-4-5)", gm.live.ProviderName, gm.live.Model)
	}
	if gm.session.header.ID != target {
		t.Fatalf("header.ID = %q, want the resumed session %q", gm.session.header.ID, target)
	}
	if gm.statusBar.provider != "anthropic" || gm.statusBar.model != "claude-sonnet-4-5" {
		t.Errorf("status bar = (%q, %q), want the live (anthropic, claude-sonnet-4-5)",
			gm.statusBar.provider, gm.statusBar.model)
	}
	if gm.statusBar.thinking != "high" {
		t.Errorf("thinking = %q, want the live level kept across a session switch", gm.statusBar.thinking)
	}
	// The gauge is re-seeded against the live window and the resumed context, so
	// the first frame after the switch is not blank.
	if gm.statusBar.contextPct < 0 {
		t.Errorf("context gauge = %d, want a reading for the resumed context", gm.statusBar.contextPct)
	}
	// /status and the bar read the same live config — the whole point.
	var buf bytes.Buffer
	status.RunStatus(&buf, gm.session)
	out := buf.String()
	if !strings.Contains(out, "model: claude-sonnet-4-5") || !strings.Contains(out, "provider: anthropic") {
		t.Errorf("/status should report the live config:\n%s", out)
	}
	if strings.Contains(out, "gpt-4o") {
		t.Errorf("/status reports the session's recorded model as if it were in use:\n%s", out)
	}
	// The splash panel is redrawn from the same config: it prints provider and
	// model rows, so anything else there reads as authoritative.
	panel := stripANSI(renderBannerFrame(gm.theme, gm.bannerOptions(), gm.cwd, logoFrames))
	if !strings.Contains(panel, "claude-sonnet-4-5") || !strings.Contains(panel, "anthropic") {
		t.Errorf("banner should show the live config:\n%s", panel)
	}
	if strings.Contains(panel, "gpt-4o") || strings.Contains(panel, "openai") {
		t.Errorf("banner shows the resumed session's record instead of the live config:\n%s", panel)
	}
}

// TestBannerOptionsLayerLiveOverLaunch pins the layering rule itself: the launch
// Options stay the floor (version, directory, prompt inputs), and the live
// config wins for the four fields the panel prints that a command can move.
func TestBannerOptionsLayerLiveOverLaunch(t *testing.T) {
	m := NewModel(Options{
		Version:       "1.2.3",
		Model:         "launch-model",
		ProviderName:  "launch-provider",
		Protocol:      "anthropic",
		ThinkingLevel: agentcore.ThinkingOff,
	})
	m.live.Model = "gpt-4o"
	m.live.ProviderName = "openai"
	m.live.Protocol = ""
	m.live.ThinkingLevel = agentcore.ThinkingMax

	opts := m.bannerOptions()
	if opts.Version != "1.2.3" {
		t.Errorf("Version = %q, want the launch value", opts.Version)
	}
	if opts.Model != "gpt-4o" || opts.ProviderName != "openai" {
		t.Errorf("model/provider = (%q, %q), want the live pair", opts.Model, opts.ProviderName)
	}
	if opts.Protocol != "" {
		t.Errorf("Protocol = %q, want the live (cleared) override", opts.Protocol)
	}
	if opts.ThinkingLevel != agentcore.ThinkingMax {
		t.Errorf("ThinkingLevel = %q, want the live level", opts.ThinkingLevel)
	}
	// The launch Options themselves are never mutated: they stay the record of
	// what this process was started with.
	if m.opts.Model != "launch-model" {
		t.Errorf("m.opts.Model = %q, launch Options must stay untouched", m.opts.Model)
	}
}
